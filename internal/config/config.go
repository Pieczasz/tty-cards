// Package config reads the server's environment into one validated Config. Every
// malformed value fails the boot: a typo that silently meant the default is how an
// operator ends up believing a setting is on.
package config

import (
	"cmp"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"net/url"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

// DefaultAPIPort is the stats API's port when API_PORT is unset. The container
// healthcheck runs without a loaded Config and has to agree with it.
const DefaultAPIPort = 6970

const (
	envProduction  = "production"
	envDevelopment = "development"
)

// Config is the whole of the server's configuration, as Load reads it.
type Config struct {
	Env                  string
	ServerHost           string
	ServerPort           int
	APIPort              int
	APIAllowOrigin       string
	APIRequestsPerMinute int
	APITrustProxy        bool
	MaxConnections       int
	SSHKeyPath           string
	DBHost               string
	DBPort               int
	DBUser               string
	DBName               string
	DBPassword           string
	DBSSLMode            string
	DBMaxOpenConnections int
	// AllowInsecureDB lets production use a plaintext DB_SSLMODE against a remote
	// host. Only for a network the operator trusts end to end.
	AllowInsecureDB bool
	OTelEndpoint    string
	OTelInsecure    bool
	ServiceVersion  string
	RateLimitCount  int
	RateLimitWindow time.Duration
	LogLevel        slog.Level
	// ProxyProtocol keeps the PROXY-header requirement on the ssh listener. True matches
	// the nginx deployment; a bare `ssh` client never sends the header, so local
	// development needs PROXY_PROTOCOL=false.
	ProxyProtocol bool
	// ProxyTrustedCIDRs, when set, are the only peers whose PROXY header is honored;
	// empty trusts every peer, which is only safe while 6969 is never published.
	ProxyTrustedCIDRs []netip.Prefix
	// RegistrationLimit accounts per client network per RegistrationWindow. Five an
	// hour in production; make loadtest needs far more.
	RegistrationLimit  int
	RegistrationWindow time.Duration
}

// IsProduction reports whether the production-only checks and defaults apply.
func (c *Config) IsProduction() bool { return c.Env == envProduction }

// IsDevelopment reports a local development run, the only one that logs every SQL
// statement: staging is meant to look like production.
func (c *Config) IsDevelopment() bool { return c.Env == envDevelopment }

// envReader parses env values and keeps every failure, so one boot reports every
// bad variable instead of the first. An unset or empty variable means the fallback.
type envReader struct {
	err error
}

func (r *envReader) fail(key string, err error) {
	r.err = errors.Join(r.err, fmt.Errorf("invalid %s: %w", key, err))
}

func (r *envReader) int(key string, fallback int) int {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		r.fail(key, err)
	}
	return v
}

func (r *envReader) duration(key string, fallback time.Duration) time.Duration {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback
	}
	v, err := time.ParseDuration(raw)
	if err != nil {
		r.fail(key, err)
	}
	return v
}

// bool takes what strconv.ParseBool does (1, t, true, 0, f, false, any case of
// those words). Anything else fails the boot: "yes" used to mean false for some
// variables and true for others.
func (r *envReader) bool(key string, fallback bool) bool {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		r.fail(key, err)
	}
	return v
}

// prefixes reads a comma-separated CIDR list. One bad entry fails the whole list:
// silently dropping it would trust fewer peers than configured, or none.
func (r *envReader) prefixes(key string) []netip.Prefix {
	var prefixes []netip.Prefix
	for field := range strings.SplitSeq(os.Getenv(key), ",") {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		prefix, err := netip.ParsePrefix(field)
		if err != nil {
			r.fail(key, err)
			return nil
		}
		prefixes = append(prefixes, prefix.Masked())
	}
	return prefixes
}

// level takes the names slog understands (DEBUG, INFO, WARN, ERROR, +N/-N).
func (r *envReader) level(key string) slog.Level {
	raw := os.Getenv(key)
	if raw == "" {
		return slog.LevelInfo
	}
	var level slog.Level
	if err := level.UnmarshalText([]byte(raw)); err != nil {
		r.fail(key, err)
	}
	return level
}

// resolveEnv reads ENV and loads .env outside production. An unrecognised value is
// an error: falling back to development turned a typo like ENV=prod into a production
// server with every production check off.
func resolveEnv() (string, error) {
	env := getEnv("ENV", envDevelopment)
	switch env {
	case envProduction:
	case envDevelopment, "staging":
		_ = godotenv.Load()
	default:
		return "", fmt.Errorf("invalid ENV %q: want production, staging or development", env)
	}
	return env, nil
}

// Load reads the environment (and .env outside production) into a validated Config.
func Load() (*Config, error) {
	env, err := resolveEnv()
	if err != nil {
		return nil, err
	}
	production := env == envProduction
	defaultSSLMode := "disable"
	if production {
		defaultSSLMode = "require"
	}

	r := &envReader{}
	cfg := &Config{
		Env:            env,
		ServerHost:     getEnv("SERVER_HOST", "0.0.0.0"),
		ServerPort:     r.int("SERVER_PORT", 6969),
		APIPort:        r.int("API_PORT", DefaultAPIPort),
		APIAllowOrigin: getEnv("API_ALLOW_ORIGIN", "*"),
		// Per network, not per visitor, and tunable without a rebuild.
		APIRequestsPerMinute: r.int("API_REQUESTS_PER_MINUTE", 120),
		// Off by default: trusting X-Forwarded-For on a directly reachable listener lets
		// any caller forge an address and walk past the rate limit. compose never
		// publishes the API port, so nginx is the only source there, and it opts in.
		APITrustProxy:        r.bool("API_TRUST_PROXY", false),
		ProxyProtocol:        r.bool("PROXY_PROTOCOL", true),
		ProxyTrustedCIDRs:    r.prefixes("PROXY_TRUSTED_CIDRS"),
		RegistrationLimit:    r.int("REGISTRATION_LIMIT", 5),
		RegistrationWindow:   r.duration("REGISTRATION_WINDOW", time.Hour),
		MaxConnections:       r.int("MAX_CONNECTIONS", 1000),
		SSHKeyPath:           getEnv("SSH_KEY_PATH", ".wishlist/server"),
		DBHost:               getEnv("DB_HOST", "localhost"),
		DBPort:               r.int("DB_PORT", 5432),
		DBUser:               getEnv("DB_USER", "postgres"),
		DBName:               getEnv("DB_NAME", "terminal_card"),
		DBPassword:           getEnv("DB_PASSWORD", ""),
		DBSSLMode:            getEnv("DB_SSLMODE", defaultSSLMode),
		DBMaxOpenConnections: r.int("DB_MAX_OPEN_CONNS", 25),
		AllowInsecureDB:      r.bool("ALLOW_INSECURE_DB", false),
		OTelEndpoint:         getEnv("OTEL_EXPORTER_OTLP_ENDPOINT", "localhost:4317"),
		// Plaintext to the collector outside production, where it is on localhost.
		OTelInsecure:    r.bool("OTEL_EXPORTER_OTLP_INSECURE", !production),
		ServiceVersion:  getEnv("SERVICE_VERSION", detectVersion()),
		RateLimitCount:  r.int("RATE_LIMIT_CONNECTIONS", 5),
		RateLimitWindow: time.Duration(r.int("RATE_LIMIT_WINDOW_MS", 1000)) * time.Millisecond,
		LogLevel:        r.level("LOG_LEVEL"),
	}
	if r.err != nil {
		return nil, r.err
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}
	return cfg, nil
}

// Validate checks production-critical configuration.
func (c *Config) Validate() error {
	if c.IsProduction() {
		if err := c.validateProductionDB(); err != nil {
			return err
		}
	}
	for _, check := range []struct {
		bad bool
		msg string
	}{
		{c.RateLimitCount < 1, "RATE_LIMIT_CONNECTIONS must be at least 1"},
		{c.RateLimitWindow < time.Millisecond, "RATE_LIMIT_WINDOW_MS must be at least 1"},
		{c.APIRequestsPerMinute < 1, "API_REQUESTS_PER_MINUTE must be at least 1"},
		{c.DBMaxOpenConnections < 1, "DB_MAX_OPEN_CONNS must be at least 1"},
		// netutil.LimitListener treats a non-positive limit as "accept nothing", so the
		// server would bind the port and then refuse every player.
		{c.MaxConnections < 1, "MAX_CONNECTIONS must be at least 1"},
		{c.RegistrationLimit < 1, "REGISTRATION_LIMIT must be at least 1"},
		{c.RegistrationWindow < time.Second, "REGISTRATION_WINDOW must be at least 1s"},
	} {
		if check.bad {
			return errors.New(check.msg)
		}
	}
	return nil
}

// validateProductionDB insists on a TLS mode that refuses plaintext. prefer and allow
// quietly fall back to it whenever the server declines TLS, so they are disable with
// extra steps.
func (c *Config) validateProductionDB() error {
	if c.DBPassword == "" {
		return errors.New("DB_PASSWORD is required when ENV=production")
	}
	switch c.DBSSLMode {
	case "require", "verify-ca", "verify-full":
		return nil
	}
	internalHost := c.DBHost == "db" || c.DBHost == "localhost" || c.DBHost == "127.0.0.1"
	if c.AllowInsecureDB || internalHost {
		return nil
	}
	return fmt.Errorf("DB_SSLMODE=%s is not allowed in production for host %q; use require, verify-ca "+
		"or verify-full, or set ALLOW_INSECURE_DB=true only for trusted networks", c.DBSSLMode, c.DBHost)
}

// DSN carries DBPassword in clear text. Never log the result; log the Config
// itself, whose String method redacts it.
//
// It is a URL and not keyword/value pairs because those need quoting: an empty or
// spaced password there swallows the keywords that follow it, so the server
// silently connects to a different database than the one it was configured with.
func (c *Config) DSN() string {
	dsn := url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(c.DBUser, c.DBPassword),
		Host:   net.JoinHostPort(c.DBHost, strconv.Itoa(c.DBPort)),
		Path:   "/" + c.DBName,
		RawQuery: url.Values{
			"sslmode":  {c.DBSSLMode},
			"TimeZone": {"UTC"},
		}.Encode(),
	}
	return dsn.String()
}

// String keeps the database password out of logs. Without it, any %v or
// slog.Any on a Config prints DB_PASSWORD in clear text, which is one careless
// debug line away from leaking the credential into log storage.
func (c *Config) String() string {
	if c == nil {
		return "<nil>"
	}
	redacted := *c
	if redacted.DBPassword != "" {
		redacted.DBPassword = "[REDACTED]"
	}
	// plain drops the method set, so formatting it cannot re-enter String.
	type plain Config
	return fmt.Sprintf("%+v", plain(redacted))
}

const fallbackVersion = "0.1.0"

func detectVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return fallbackVersion
	}
	return normalizeVersion(info.Main.Version)
}

// normalizeVersion rejects the two build stamps that carry no information. Reporting
// "(devel)" as a service version tells an operator nothing about what is deployed.
func normalizeVersion(version string) string {
	if version == "" || version == "(devel)" {
		return fallbackVersion
	}
	return version
}

// getEnv returns the environment value for key, falling back when it is unset or
// empty. An empty value must not win: a blank SSH_KEY_PATH or DB_HOST in a .env or
// compose file would otherwise silently defeat the default. envReader treats "" the
// same way.
func getEnv(key string, fallback string) string {
	return cmp.Or(os.Getenv(key), fallback)
}
