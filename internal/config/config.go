// Package config reads the server's environment into one validated Config. Every
// malformed value fails the boot.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"net/url"
	"runtime/debug"
	"strconv"
	"time"
)

// DefaultAPIPort is the stats API's port when API_PORT is unset.
const DefaultAPIPort = 6970
const fallbackVersion = "0.1.0"

const (
	envProduction  = "production"
	envDevelopment = "development"
	envStaging     = "staging"
)

// Config is the whole of the server's configuration, as Load reads it.
type Config struct {
	Env            string
	ServerHost     string
	ServerPort     int
	ServiceVersion string

	APIPort              int
	APIAllowOrigin       string
	APIRequestsPerMinute int
	APITrustProxy        bool

	ProxyProtocol     bool
	ProxyTrustedCIDRs []netip.Prefix

	MaxConnections int
	SSHKeyPath     string

	DBHost         string
	DBPort         int
	DBUser         string
	DBName         string
	DBPassword     Secret
	DBSSLMode      string
	DBMaxOpenConns int

	// OTelEndpoint is an http:// or https:// URL; the scheme decides TLS.
	OTelEndpoint string
	LogLevel     slog.Level

	RateLimitCount     int
	RateLimitWindow    time.Duration
	RegistrationLimit  int
	RegistrationWindow time.Duration
	// RegistrationLimitWide is the budget per IPv6 /48 (ratelimit.WideKey), beside
	// RegistrationLimit's per /64: a free tunnel is a /48, 65,536 /64 budgets.
	RegistrationLimitWide int
	// RegistrationGlobalLimit is the ceiling across every network, for an attacker
	// with more networks than any per-network budget can see.
	RegistrationGlobalLimit int
}

// IsProduction reports ENV=production, which turns on the checks a staging or
// development boot may skip, such as a required database password.
func (c *Config) IsProduction() bool { return c.Env == envProduction }

// IsDevelopment reports ENV=development, the only environment allowed a
// plaintext database and an empty PROXY_TRUSTED_CIDRS.
func (c *Config) IsDevelopment() bool { return c.Env == envDevelopment }

// Load reads the environment (and .env outside production) into a validated Config.
// ENV itself comes from the process environment only, since it decides whether
// .env is read at all.
func Load() (*Config, error) {
	environment, err := resolveEnvironment()
	if err != nil {
		return nil, err
	}
	defaultSSLMode := "require"
	if environment == envDevelopment {
		defaultSSLMode = "disable"
	}
	defaultOTelEndpoint := "http://localhost:4317"
	if environment == envProduction {
		defaultOTelEndpoint = "https://localhost:4317"
	}

	r := &envReader{}
	cfg := &Config{
		Env:            environment,
		ServerHost:     getEnv("SERVER_HOST", "0.0.0.0"),
		ServerPort:     r.int("SERVER_PORT", 6969),
		ServiceVersion: getEnv("SERVICE_VERSION", detectVersion()),

		APIPort:              r.int("API_PORT", DefaultAPIPort),
		APIAllowOrigin:       getEnv("API_ALLOW_ORIGIN", "*"),
		APIRequestsPerMinute: r.int("API_REQUESTS_PER_MINUTE", 120),
		APITrustProxy:        r.bool("API_TRUST_PROXY", false),

		ProxyProtocol:     r.bool("PROXY_PROTOCOL", true),
		ProxyTrustedCIDRs: r.prefixes("PROXY_TRUSTED_CIDRS"),

		MaxConnections: r.int("MAX_CONNECTIONS", 1000),
		SSHKeyPath:     getEnv("SSH_KEY_PATH", ".wishlist/server"),

		DBHost:         getEnv("DB_HOST", "localhost"),
		DBPort:         r.int("DB_PORT", 5432),
		DBUser:         getEnv("DB_USER", "postgres"),
		DBName:         getEnv("DB_NAME", "terminal_card"),
		DBPassword:     Secret(getEnv("DB_PASSWORD", "")),
		DBSSLMode:      getEnv("DB_SSLMODE", defaultSSLMode),
		DBMaxOpenConns: r.int("DB_MAX_OPEN_CONNS", 25),

		OTelEndpoint: r.endpointURL("OTEL_EXPORTER_OTLP_ENDPOINT", defaultOTelEndpoint),
		LogLevel:     r.logLevel("LOG_LEVEL"),

		RateLimitCount:     r.int("RATE_LIMIT_CONNECTIONS", 5),
		RateLimitWindow:    r.duration("RATE_LIMIT_WINDOW", time.Second),
		RegistrationLimit:  r.int("REGISTRATION_LIMIT", 5),
		RegistrationWindow: r.duration("REGISTRATION_WINDOW", time.Hour),
		// Four /64s' worth per /48: a household or office shares one. 600 an hour is a
		// launch spike for a 1000-player goal with room to spare, while a botnet that
		// spreads past every per-network budget still mints at most 14,400 rows a day.
		RegistrationLimitWide:   r.int("REGISTRATION_LIMIT_WIDE", 20),
		RegistrationGlobalLimit: r.int("REGISTRATION_GLOBAL_LIMIT", 600),
	}
	if r.err != nil {
		return nil, r.err
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}
	return cfg, nil
}

// Validate checks every limit and that only development talks to its database in
// plaintext.
func (c *Config) Validate() error {
	if err := c.validateDB(); err != nil {
		return err
	}
	if err := c.validateProxy(); err != nil {
		return err
	}
	for _, check := range []struct {
		bad bool
		msg string
	}{
		{c.RateLimitCount < 1, "RATE_LIMIT_CONNECTIONS must be at least 1"},
		{c.RateLimitWindow < time.Millisecond, "RATE_LIMIT_WINDOW must be at least 1ms"},
		{c.APIRequestsPerMinute < 1, "API_REQUESTS_PER_MINUTE must be at least 1"},
		{c.DBMaxOpenConns < 1, "DB_MAX_OPEN_CONNS must be at least 1"},
		{c.MaxConnections < 1, "MAX_CONNECTIONS must be at least 1"},
		{c.RegistrationLimit < 1, "REGISTRATION_LIMIT must be at least 1"},
		{c.RegistrationLimitWide < 1, "REGISTRATION_LIMIT_WIDE must be at least 1"},
		{c.RegistrationGlobalLimit < 1, "REGISTRATION_GLOBAL_LIMIT must be at least 1"},
		{c.RegistrationWindow < time.Second, "REGISTRATION_WINDOW must be at least 1s"},
	} {
		if check.bad {
			return errors.New(check.msg)
		}
	}
	return nil
}

// validateDB exempts no host: "db" is only as private as the network compose puts it
// on, and prefer or allow fall back to plaintext whenever the server declines TLS.
func (c *Config) validateDB() error {
	if c.IsProduction() && c.DBPassword == "" {
		return errors.New("DB_PASSWORD is required when ENV=production")
	}
	if c.IsDevelopment() {
		return nil
	}
	switch c.DBSSLMode {
	case "require", "verify-ca", "verify-full":
		return nil
	}
	return fmt.Errorf("DB_SSLMODE=%s is not allowed with ENV=%s; use require, verify-ca or verify-full",
		c.DBSSLMode, c.Env)
}

// validateProxy refuses a listener that trusts a PROXY header or X-Forwarded-For
// from the whole internet. Development may boot without a trusted list; everywhere
// else an enabled proxy with nobody trusted, or a list that is the whole net, is a
// boot failure.
func (c *Config) validateProxy() error {
	for _, p := range c.ProxyTrustedCIDRs {
		if p == netip.MustParsePrefix("0.0.0.0/0") || p == netip.MustParsePrefix("::/0") {
			return fmt.Errorf("PROXY_TRUSTED_CIDRS must not contain %s", p)
		}
	}
	if c.IsDevelopment() || (!c.ProxyProtocol && !c.APITrustProxy) {
		return nil
	}
	if len(c.ProxyTrustedCIDRs) == 0 {
		return errors.New("PROXY_TRUSTED_CIDRS is required when PROXY_PROTOCOL or API_TRUST_PROXY is enabled outside development")
	}
	return nil
}

// DSN is the one place the database password is revealed: it is for the driver
// only and must never be logged.
//
// It is a URL and not keyword/value pairs because those need quoting: an empty or
// spaced password there swallows the keywords that follow it, so the server
// silently connects to a different database than the one it was configured with.
func (c *Config) DSN() string {
	dsn := url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(c.DBUser, c.DBPassword.Reveal()),
		Host:   net.JoinHostPort(c.DBHost, strconv.Itoa(c.DBPort)),
		Path:   "/" + c.DBName,
		RawQuery: url.Values{
			"sslmode":  {c.DBSSLMode},
			"TimeZone": {"UTC"},
		}.Encode(),
	}
	return dsn.String()
}

func detectVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return fallbackVersion
	}
	return normalizeVersion(info.Main.Version)
}

func normalizeVersion(version string) string {
	if version == "" || version == "(devel)" {
		return fallbackVersion
	}
	return version
}
