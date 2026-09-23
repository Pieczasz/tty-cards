package config_test

import (
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Pieczasz/terminal-card/internal/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoad_Defaults(t *testing.T) {
	t.Setenv("ENV", "development")
	t.Setenv("DB_PASSWORD", "")
	t.Setenv("SERVER_PORT", "")
	t.Setenv("MAX_CONNECTIONS", "")
	t.Setenv("DB_PORT", "")
	t.Setenv("DB_MAX_OPEN_CONNS", "")
	t.Setenv("RATE_LIMIT_CONNECTIONS", "")
	t.Setenv("RATE_LIMIT_WINDOW_MS", "")
	t.Setenv("DB_SSLMODE", "")
	t.Setenv("SERVER_HOST", "")
	t.Setenv("DB_HOST", "")
	t.Setenv("DB_USER", "")
	t.Setenv("DB_NAME", "")
	t.Setenv("SSH_KEY_PATH", "")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_INSECURE", "")
	t.Setenv("SERVICE_VERSION", "")

	cfg, err := config.Load()
	require.NoError(t, err)
	assert.Equal(t, "development", cfg.Env)
	assert.Equal(t, 6969, cfg.ServerPort)
	assert.Equal(t, "disable", cfg.DBSSLMode)
	assert.Equal(t, 5, cfg.RateLimitCount)
	assert.Equal(t, time.Second, cfg.RateLimitWindow)
	assert.Equal(t, 25, cfg.DBMaxOpenConnections)
	assert.True(t, cfg.OTelInsecure)
	assert.NotEmpty(t, cfg.ServiceVersion)
}

func TestLoad_ProductionRequiresPassword(t *testing.T) {
	t.Setenv("ENV", "production")
	t.Setenv("DB_PASSWORD", "")
	t.Setenv("DB_SSLMODE", "disable")

	_, err := config.Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "DB_PASSWORD")
}

func TestLoad_ProductionSSLDefault(t *testing.T) {
	t.Setenv("ENV", "production")
	t.Setenv("DB_PASSWORD", "secret")
	t.Setenv("DB_SSLMODE", "")

	cfg, err := config.Load()
	require.NoError(t, err)
	assert.Equal(t, "require", cfg.DBSSLMode)
}

func TestLoad_InvalidPort(t *testing.T) {
	t.Setenv("ENV", "development")
	t.Setenv("SERVER_PORT", "not-a-number")

	_, err := config.Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "SERVER_PORT")
}

func TestValidate_RateLimit(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Env:             "development",
		RateLimitCount:  0,
		RateLimitWindow: time.Second,
	}
	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "RATE_LIMIT_CONNECTIONS")
}

// Every field has to survive the trip, whatever the password looks like: the old
// keyword/value DSN let a blank or spaced password absorb the keywords after it, and
// the server connected to a different database than the one it was configured with.
func TestConfig_DSN(t *testing.T) {
	t.Parallel()

	passwords := []struct {
		name  string
		value string
	}{
		{name: "ordinary", value: "secret"},
		{name: "empty", value: ""},
		{name: "spaced", value: "two words"},
		{name: "looks like more keywords", value: " dbname=other sslmode=disable"},
	}

	for _, pw := range passwords {
		t.Run(pw.name, func(t *testing.T) {
			t.Parallel()
			cfg := &config.Config{
				DBHost:     "localhost",
				DBUser:     "postgres",
				DBPassword: pw.value,
				DBName:     "terminal_card",
				DBPort:     5432,
				DBSSLMode:  "disable",
			}

			parsed, err := url.Parse(cfg.DSN())
			require.NoError(t, err)

			assert.Equal(t, "localhost:5432", parsed.Host)
			assert.Equal(t, "/terminal_card", parsed.Path)
			assert.Equal(t, "postgres", parsed.User.Username())
			password, _ := parsed.User.Password()
			assert.Equal(t, pw.value, password, "the driver must read back the configured password")
			assert.Equal(t, "disable", parsed.Query().Get("sslmode"))
			assert.Equal(t, "UTC", parsed.Query().Get("TimeZone"))
		})
	}
}

func TestValidate_InsecureDBRemoteHost(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Env:                  "production",
		DBPassword:           "secret",
		DBHost:               "db.example.com",
		DBSSLMode:            "disable",
		RateLimitCount:       5,
		RateLimitWindow:      time.Second,
		DBMaxOpenConnections: 25,
	}
	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "DB_SSLMODE=disable")
}

// An env var that is present but blank must not defeat the default.
func TestLoad_BlankEnvFallsBackToDefault(t *testing.T) {
	blanked := []string{
		"SERVER_HOST", "SSH_KEY_PATH", "DB_HOST", "DB_USER", "DB_NAME",
		"DB_SSLMODE", "OTEL_EXPORTER_OTLP_ENDPOINT", "SERVICE_VERSION",
	}
	t.Setenv("ENV", "development")
	for _, key := range blanked {
		t.Setenv(key, "")
	}

	cfg, err := config.Load()
	require.NoError(t, err)

	assert.Equal(t, "0.0.0.0", cfg.ServerHost)
	assert.Equal(t, ".wishlist/server", cfg.SSHKeyPath)
	assert.Equal(t, "localhost", cfg.DBHost)
	assert.Equal(t, "postgres", cfg.DBUser)
	assert.Equal(t, "terminal_card", cfg.DBName)
	assert.Equal(t, "disable", cfg.DBSSLMode)
	assert.Equal(t, "localhost:4317", cfg.OTelEndpoint)
	assert.NotEmpty(t, cfg.ServiceVersion, "version always resolves to something")
}

// httpapi.NewServer applies no defaults of its own, so Load is the only place the
// stats API gets a rate and an origin: a zero rate refuses every visitor and an empty
// origin breaks the website's fetch.
func TestLoad_StatsAPIDefaults(t *testing.T) {
	t.Setenv("ENV", "development")
	t.Setenv("API_REQUESTS_PER_MINUTE", "")
	t.Setenv("API_ALLOW_ORIGIN", "")

	cfg, err := config.Load()
	require.NoError(t, err)
	assert.Equal(t, 120, cfg.APIRequestsPerMinute)
	assert.Equal(t, "*", cfg.APIAllowOrigin)

	t.Setenv("API_REQUESTS_PER_MINUTE", "0")
	_, err = config.Load()
	require.ErrorContains(t, err, "API_REQUESTS_PER_MINUTE")
}

// X-Forwarded-For on a directly reachable listener can be forged, so off is the only
// safe default and turning it on takes an explicit opt-in.
func TestLoad_TrustProxyDefaultsToOff(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  bool
	}{
		{name: "unset", value: "", want: false},
		{name: "explicitly false", value: "false", want: false},
		{name: "zero", value: "0", want: false},
		{name: "opted in", value: "true", want: true},
		{name: "opted in with a one", value: "1", want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("ENV", "development")
			t.Setenv("API_TRUST_PROXY", tt.value)

			cfg, err := config.Load()
			require.NoError(t, err)
			assert.Equal(t, tt.want, cfg.APITrustProxy)
		})
	}
}

// The four switches used to spell "true" four ways: == "true", != "false", a three-word
// allow-list and a plain string compare. So PROXY_PROTOCOL=off kept the PROXY header
// on and API_TRUST_PROXY=yes left it off. A value ParseBool refuses now fails the boot.
func TestLoad_BoolEnvTypoFailsTheBoot(t *testing.T) {
	for _, key := range []string{"API_TRUST_PROXY", "PROXY_PROTOCOL", "OTEL_EXPORTER_OTLP_INSECURE", "ALLOW_INSECURE_DB"} {
		for _, value := range []string{"yes", "off", "ture"} {
			t.Run(key+"="+value, func(t *testing.T) {
				t.Setenv("ENV", "development")
				t.Setenv(key, value)

				_, err := config.Load()
				require.ErrorContains(t, err, "invalid "+key)
			})
		}
	}
}

func TestLoad_BoolEnvs(t *testing.T) {
	t.Setenv("ENV", "production")
	t.Setenv("DB_PASSWORD", "secret")
	t.Setenv("DB_HOST", "db.example.com")
	t.Setenv("DB_SSLMODE", "disable")
	t.Setenv("PROXY_PROTOCOL", "0")
	t.Setenv("OTEL_EXPORTER_OTLP_INSECURE", "TRUE")
	t.Setenv("ALLOW_INSECURE_DB", "true")

	cfg, err := config.Load()
	require.NoError(t, err, "ALLOW_INSECURE_DB lets a remote plaintext database through")
	assert.False(t, cfg.ProxyProtocol)
	assert.True(t, cfg.OTelInsecure)
	assert.True(t, cfg.AllowInsecureDB)
}

// Every bad variable is reported at once, so a broken .env is fixed in one pass.
func TestLoad_ReportsEveryInvalidVariable(t *testing.T) {
	t.Setenv("ENV", "development")
	t.Setenv("SERVER_PORT", "x")
	t.Setenv("REGISTRATION_WINDOW", "an hour")
	t.Setenv("LOG_LEVEL", "loud")

	_, err := config.Load()
	require.Error(t, err)
	for _, key := range []string{"SERVER_PORT", "REGISTRATION_WINDOW", "LOG_LEVEL"} {
		assert.ErrorContains(t, err, "invalid "+key)
	}
}

// An explicitly configured SSL mode has to survive Load: silently replacing it with the
// environment default would either weaken or break a deployment.
func TestLoad_ExplicitSSLModeIsHonored(t *testing.T) {
	t.Setenv("ENV", "production")
	t.Setenv("DB_PASSWORD", "secret")
	t.Setenv("DB_SSLMODE", "verify-full")

	cfg, err := config.Load()
	require.NoError(t, err)
	assert.Equal(t, "verify-full", cfg.DBSSLMode)
}

// .env is a development convenience.
func TestResolveEnv_DotEnvLoadedOutsideProductionOnly(t *testing.T) {
	tests := []struct {
		name       string
		env        string
		wantDBName string
	}{
		{name: "development reads .env", env: "development", wantDBName: "from_dotenv"},
		{name: "staging reads .env", env: "staging", wantDBName: "from_dotenv"},
		{name: "production ignores .env", env: "production", wantDBName: "terminal_card"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(dir, ".env"), []byte("DB_NAME=from_dotenv\n"), 0o600))
			t.Chdir(dir)

			t.Setenv("ENV", tt.env)
			t.Setenv("DB_PASSWORD", "secret")
			// godotenv never overrides a variable that is already present, so this one
			// has to be genuinely absent for the file to be observable at all.
			t.Setenv("DB_NAME", "")
			require.NoError(t, os.Unsetenv("DB_NAME"))

			cfg, err := config.Load()
			require.NoError(t, err)
			assert.Equal(t, tt.wantDBName, cfg.DBName)
			assert.Equal(t, tt.env, cfg.Env)
		})
	}
}

// A mistyped ENV used to mean development, which silently switched off every
// production check - the password requirement, the TLS default - on the one
// deployment that needed them.
func TestResolveEnv_UnknownIsAnError(t *testing.T) {
	for _, env := range []string{"wat", "prod", "Production"} {
		t.Run(env, func(t *testing.T) {
			t.Setenv("ENV", env)

			_, err := config.Load()
			require.Error(t, err)
			assert.Contains(t, err.Error(), "ENV")
		})
	}
}

// prefer and allow fall back to plaintext whenever the server declines TLS, so in
// production they are disable with extra steps.
func TestValidate_ProductionNeedsAnSSLModeThatRequiresTLS(t *testing.T) {
	t.Parallel()
	base := func(mode, host string) *config.Config {
		return &config.Config{
			Env: "production", DBPassword: "secret", DBHost: host, DBSSLMode: mode,
			RateLimitCount: 5, RateLimitWindow: time.Second, DBMaxOpenConnections: 25,
			MaxConnections: 1, APIRequestsPerMinute: 1,
			RegistrationLimit: 1, RegistrationWindow: time.Hour,
		}
	}

	tests := []struct {
		mode, host string
		wantErr    bool
	}{
		{mode: "require", host: "db.example.com"},
		{mode: "verify-ca", host: "db.example.com"},
		{mode: "verify-full", host: "db.example.com"},
		{mode: "prefer", host: "db.example.com", wantErr: true},
		{mode: "allow", host: "db.example.com", wantErr: true},
		{mode: "disable", host: "db.example.com", wantErr: true},
		{mode: "prefer", host: "db"},
	}
	for _, tt := range tests {
		t.Run(tt.mode+"@"+tt.host, func(t *testing.T) {
			t.Parallel()
			err := base(tt.mode, tt.host).Validate()
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "DB_SSLMODE="+tt.mode)
				return
			}
			require.NoError(t, err)
		})
	}
}

// make loadtest registers one account per session, far past the production budget
// of five an hour, so the budget has to be settable without a rebuild.
func TestLoad_RegistrationBudget(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		t.Setenv("ENV", "development")
		t.Setenv("REGISTRATION_LIMIT", "")
		t.Setenv("REGISTRATION_WINDOW", "")

		cfg, err := config.Load()
		require.NoError(t, err)
		assert.Equal(t, 5, cfg.RegistrationLimit)
		assert.Equal(t, time.Hour, cfg.RegistrationWindow)
	})
	t.Run("override", func(t *testing.T) {
		t.Setenv("ENV", "development")
		t.Setenv("REGISTRATION_LIMIT", "10000")
		t.Setenv("REGISTRATION_WINDOW", "1m")

		cfg, err := config.Load()
		require.NoError(t, err)
		assert.Equal(t, 10000, cfg.RegistrationLimit)
		assert.Equal(t, time.Minute, cfg.RegistrationWindow)
	})
	t.Run("an unparsable window fails the boot", func(t *testing.T) {
		t.Setenv("ENV", "development")
		t.Setenv("REGISTRATION_WINDOW", "an hour")

		_, err := config.Load()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "REGISTRATION_WINDOW")
	})
}

// The PROXY header names the client address every limiter keys on, so accepting it
// from anyone lets anyone choose their own address.
func TestLoad_ProxyTrustedCIDRs(t *testing.T) {
	t.Run("unset trusts every peer, as before", func(t *testing.T) {
		t.Setenv("ENV", "development")
		t.Setenv("PROXY_TRUSTED_CIDRS", "")

		cfg, err := config.Load()
		require.NoError(t, err)
		assert.Empty(t, cfg.ProxyTrustedCIDRs)
	})
	t.Run("a list is parsed", func(t *testing.T) {
		t.Setenv("ENV", "development")
		t.Setenv("PROXY_TRUSTED_CIDRS", "172.30.0.0/24, fd00:30::/64")

		cfg, err := config.Load()
		require.NoError(t, err)
		require.Len(t, cfg.ProxyTrustedCIDRs, 2)
		assert.Equal(t, "172.30.0.0/24", cfg.ProxyTrustedCIDRs[0].String())
		assert.Equal(t, "fd00:30::/64", cfg.ProxyTrustedCIDRs[1].String())
	})
	t.Run("the list compose sets", func(t *testing.T) {
		t.Setenv("ENV", "development")
		t.Setenv("PROXY_TRUSTED_CIDRS", "172.29.69.0/24,fd6b:1e37:9a52:6969::/64")

		cfg, err := config.Load()
		require.NoError(t, err)
		require.Len(t, cfg.ProxyTrustedCIDRs, 2)
		assert.Equal(t, "172.29.69.0/24", cfg.ProxyTrustedCIDRs[0].String())
		assert.Equal(t, "fd6b:1e37:9a52:6969::/64", cfg.ProxyTrustedCIDRs[1].String())
	})
	t.Run("a typo fails the boot rather than trusting nobody or everybody", func(t *testing.T) {
		t.Setenv("ENV", "development")
		t.Setenv("PROXY_TRUSTED_CIDRS", "172.30.0.0/33")

		_, err := config.Load()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "PROXY_TRUSTED_CIDRS")
	})
}

// Each of these limits is "at least one", so one itself has to pass.
func TestValidate_LowestAllowedValuesAreValid(t *testing.T) {
	t.Parallel()

	base := func() *config.Config {
		return &config.Config{
			Env:                  "development",
			RateLimitCount:       1,
			RateLimitWindow:      time.Millisecond,
			DBMaxOpenConnections: 1,
			MaxConnections:       1,
			APIRequestsPerMinute: 1,
			RegistrationLimit:    1,
			RegistrationWindow:   time.Second,
		}
	}

	require.NoError(t, base().Validate(), "the documented minimum of each limit is valid")

	tests := []struct {
		name    string
		breakIt func(*config.Config)
		want    string
	}{
		{name: "no connections allowed", breakIt: func(c *config.Config) { c.RateLimitCount = 0 }, want: "RATE_LIMIT_CONNECTIONS"},
		{name: "sub-millisecond window", breakIt: func(c *config.Config) { c.RateLimitWindow = time.Microsecond }, want: "RATE_LIMIT_WINDOW_MS"},
		{name: "no db connections", breakIt: func(c *config.Config) { c.DBMaxOpenConnections = 0 }, want: "DB_MAX_OPEN_CONNS"},
		{
			name:    "no api requests allowed",
			breakIt: func(c *config.Config) { c.APIRequestsPerMinute = 0 },
			want:    "API_REQUESTS_PER_MINUTE",
		},
		// LimitListener accepts nothing at all with a non-positive limit, so the
		// server would come up healthy and refuse every player.
		{name: "no connections at all", breakIt: func(c *config.Config) { c.MaxConnections = 0 }, want: "MAX_CONNECTIONS"},
		{name: "negative connections", breakIt: func(c *config.Config) { c.MaxConnections = -1 }, want: "MAX_CONNECTIONS"},
		{name: "no registrations", breakIt: func(c *config.Config) { c.RegistrationLimit = 0 }, want: "REGISTRATION_LIMIT"},
		{
			name:    "sub-second registration window",
			breakIt: func(c *config.Config) { c.RegistrationWindow = time.Millisecond },
			want:    "REGISTRATION_WINDOW",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := base()
			tt.breakIt(cfg)

			err := cfg.Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
		})
	}
}

// String is the only thing standing between a careless log line and the database password
// ending up in log storage, so it is checked directly.
func TestConfig_String_RedactsThePassword(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{DBUser: "postgres", DBHost: "db", DBPassword: "super-secret-value"}

	rendered := cfg.String()

	assert.NotContains(t, rendered, "super-secret-value", "the password must never be formatted")
	assert.Contains(t, rendered, "[REDACTED]")
	assert.Contains(t, rendered, "postgres", "everything else still needs to be readable")
	assert.Equal(t, "super-secret-value", cfg.DBPassword, "the config itself is not mutated")
}

func TestConfig_String_HandlesTheEmptyCases(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "<nil>", (*config.Config)(nil).String())
	assert.NotContains(t, (&config.Config{}).String(), "[REDACTED]", "no password, nothing to redact")
}

// DSN still carries the password; that is what the driver needs.
func TestConfig_DSN_CarriesThePasswordAndStringDoesNot(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{DBHost: "db", DBUser: "u", DBPassword: "pw", DBName: "n", DBPort: 5432, DBSSLMode: "require"}

	assert.Contains(t, cfg.DSN(), ":pw@")
	assert.NotContains(t, cfg.String(), "pw")
}

func TestLoad_LogLevel(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		want    slog.Level
		wantErr bool
	}{
		{name: "unset defaults to info", want: slog.LevelInfo},
		{name: "lowercase name", value: "debug", want: slog.LevelDebug},
		{name: "uppercase name", value: "ERROR", want: slog.LevelError},
		{name: "offset form", value: "WARN+1", want: slog.LevelWarn + 1},
		{name: "a typo fails the boot", value: "verbose", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.value != "" {
				t.Setenv("LOG_LEVEL", tt.value)
			}
			cfg, err := config.Load()
			if tt.wantErr {
				require.ErrorContains(t, err, "invalid LOG_LEVEL")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, cfg.LogLevel)
		})
	}
}

// Only a local run logs every SQL statement; staging is meant to look like production.
func TestConfig_EnvPredicates(t *testing.T) {
	t.Parallel()
	for env, want := range map[string][2]bool{
		"development": {true, false},
		"staging":     {false, false},
		"production":  {false, true},
	} {
		c := &config.Config{Env: env}
		assert.Equal(t, want[0], c.IsDevelopment(), env)
		assert.Equal(t, want[1], c.IsProduction(), env)
	}
}
