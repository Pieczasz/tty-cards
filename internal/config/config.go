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
	OTelEndpoint         string
	OTelInsecure         bool
	ServiceVersion       string
	RateLimitCount       int
	RateLimitWindow      time.Duration
	LogLevel             slog.Level
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

// intEnvs accumulates integer env lookups so one error check covers all of them
// instead of one per variable. The first failure is the one reported.
type intEnvs struct {
	err error
}

func (e *intEnvs) get(key string, fallback int) int {
	value, err := getEnvAsInt(key, fallback)
	if err != nil && e.err == nil {
		e.err = fmt.Errorf("invalid %s: %w", key, err)
	}
	return value
}

// resolveEnv reads ENV and loads .env outside production. An unrecognised value is
// an error: falling back to development turned a typo like ENV=prod into a production
// server with every production check off.
func resolveEnv() (string, error) {
	env := getEnv("ENV", "development")
	switch env {
	case "production":
	case "development", "staging":
		_ = godotenv.Load()
	default:
		return "", fmt.Errorf("invalid ENV %q: want production, staging or development", env)
	}
	return env, nil
}

// otelInsecure defaults to plaintext outside production, overridable by env.
func otelInsecure(env string) bool {
	if v, ok := os.LookupEnv("OTEL_EXPORTER_OTLP_INSECURE"); ok && v != "" {
		return v == "1" || v == "true" || v == "TRUE"
	}
	return env != "production"
}

func Load() (*Config, error) {
	env, err := resolveEnv()
	if err != nil {
		return nil, err
	}

	ints := &intEnvs{}
	serverPort := ints.get("SERVER_PORT", 6969)
	apiPort := ints.get("API_PORT", 6970)
	maxConnections := ints.get("MAX_CONNECTIONS", 1000)
	dbPort := ints.get("DB_PORT", 5432)
	dbMaxOpenConnections := ints.get("DB_MAX_OPEN_CONNS", 25)
	rateLimitCount := ints.get("RATE_LIMIT_CONNECTIONS", 5)
	rateLimitWindowMS := ints.get("RATE_LIMIT_WINDOW_MS", 1000)
	apiRequestsPerMinute := ints.get("API_REQUESTS_PER_MINUTE", 120)
	registrationLimit := ints.get("REGISTRATION_LIMIT", 5)
	if ints.err != nil {
		return nil, ints.err
	}
	registrationWindow, err := time.ParseDuration(getEnv("REGISTRATION_WINDOW", "1h"))
	if err != nil {
		return nil, fmt.Errorf("invalid REGISTRATION_WINDOW: %w", err)
	}
	trustedCIDRs, err := parsePrefixes(getEnv("PROXY_TRUSTED_CIDRS", ""))
	if err != nil {
		return nil, fmt.Errorf("invalid PROXY_TRUSTED_CIDRS: %w", err)
	}

	logLevel, err := parseLogLevel(getEnv("LOG_LEVEL", ""))
	if err != nil {
		return nil, err
	}

	defaultSSLMode := "disable"
	if env == "production" {
		defaultSSLMode = "require"
	}

	cfg := &Config{
		Env:            env,
		ServerHost:     getEnv("SERVER_HOST", "0.0.0.0"),
		ServerPort:     serverPort,
		APIPort:        apiPort,
		APIAllowOrigin: getEnv("API_ALLOW_ORIGIN", "*"),
		// Per network, not per visitor, and tunable without a rebuild.
		APIRequestsPerMinute: apiRequestsPerMinute,
		// Off by default: trusting X-Forwarded-For on a directly reachable listener lets
		// any caller forge an address and walk past the rate limit. compose never
		// publishes the API port, so nginx is the only source there, and it opts in.
		APITrustProxy:        getEnv("API_TRUST_PROXY", "false") == "true",
		ProxyProtocol:        getEnv("PROXY_PROTOCOL", "true") != "false",
		ProxyTrustedCIDRs:    trustedCIDRs,
		RegistrationLimit:    registrationLimit,
		RegistrationWindow:   registrationWindow,
		MaxConnections:       maxConnections,
		SSHKeyPath:           getEnv("SSH_KEY_PATH", ".wishlist/server"),
		DBHost:               getEnv("DB_HOST", "localhost"),
		DBPort:               dbPort,
		DBUser:               getEnv("DB_USER", "postgres"),
		DBName:               getEnv("DB_NAME", "terminal_card"),
		DBPassword:           getEnv("DB_PASSWORD", ""),
		DBSSLMode:            getEnv("DB_SSLMODE", defaultSSLMode),
		DBMaxOpenConnections: dbMaxOpenConnections,
		OTelEndpoint:         getEnv("OTEL_EXPORTER_OTLP_ENDPOINT", "localhost:4317"),
		OTelInsecure:         otelInsecure(env),
		ServiceVersion:       getEnv("SERVICE_VERSION", detectVersion()),
		RateLimitCount:       rateLimitCount,
		RateLimitWindow:      time.Duration(rateLimitWindowMS) * time.Millisecond,
		LogLevel:             logLevel,
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	return cfg, nil
}

// Validate checks production-critical configuration.
func (c *Config) Validate() error {
	if c.Env == "production" {
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
	allowInsecure := os.Getenv("ALLOW_INSECURE_DB") == "true"
	internalHost := c.DBHost == "db" || c.DBHost == "localhost" || c.DBHost == "127.0.0.1"
	if allowInsecure || internalHost {
		return nil
	}
	return fmt.Errorf("DB_SSLMODE=%s is not allowed in production for host %q; use require, verify-ca "+
		"or verify-full, or set ALLOW_INSECURE_DB=true only for trusted networks", c.DBSSLMode, c.DBHost)
}

// parsePrefixes reads a comma-separated CIDR list. One bad entry fails the whole list:
// silently dropping it would trust fewer peers than configured, or none.
func parsePrefixes(raw string) ([]netip.Prefix, error) {
	var prefixes []netip.Prefix
	for field := range strings.SplitSeq(raw, ",") {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		prefix, err := netip.ParsePrefix(field)
		if err != nil {
			return nil, fmt.Errorf("parse %q: %w", field, err)
		}
		prefixes = append(prefixes, prefix.Masked())
	}
	return prefixes, nil
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
// compose file would otherwise silently defeat the default. This matches
// getEnvAsInt, which already treats "" as absent.
func getEnv(key string, fallback string) string {
	return cmp.Or(os.Getenv(key), fallback)
}

// parseLogLevel takes the names slog understands (DEBUG, INFO, WARN, ERROR, +N/-N). A
// typo fails the boot rather than silently meaning info, which is how an operator ends
// up believing debug logging is on.
func parseLogLevel(raw string) (slog.Level, error) {
	if raw == "" {
		return slog.LevelInfo, nil
	}
	var level slog.Level
	if err := level.UnmarshalText([]byte(raw)); err != nil {
		return slog.LevelInfo, fmt.Errorf("invalid LOG_LEVEL %q: %w", raw, err)
	}
	return level, nil
}

func getEnvAsInt(name string, fallback int) (int, error) {
	valStr := getEnv(name, "")
	if valStr == "" {
		return fallback, nil
	}
	val, err := strconv.Atoi(valStr)
	if err != nil {
		return 0, fmt.Errorf("trying to parse env value failed: %w", err)
	}
	return val, nil
}
