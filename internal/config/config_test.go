package config_test

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Pieczasz/tty-cards/internal/config"

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
	t.Setenv("RATE_LIMIT_WINDOW", "")
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
	assert.Equal(t, 25, cfg.DBMaxOpenConns)
	assert.Equal(t, "http://localhost:4317", cfg.OTelEndpoint)
	assert.NotEmpty(t, cfg.ServiceVersion)
}

func TestLoad_ProductionRequiresPassword(t *testing.T) {
	t.Setenv("ENV", "production")
	t.Setenv("DB_PASSWORD", "")
	t.Setenv("DB_SSLMODE", "require")

	_, err := config.Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "DB_PASSWORD")
}

// Only a local run may talk to its database in plaintext, so every other
// environment has to default to TLS or it would fail its own validation.
func TestLoad_SSLModeDefaultsToTLSOutsideDevelopment(t *testing.T) {
	for env, want := range map[string]string{"development": "disable", "staging": "require", "production": "require"} {
		t.Run(env, func(t *testing.T) {
			t.Setenv("ENV", env)
			t.Setenv("DB_PASSWORD", "secret")
			t.Setenv("DB_SSLMODE", "")

			cfg, err := config.Load()
			require.NoError(t, err)
			assert.Equal(t, want, cfg.DBSSLMode)
		})
	}
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
				DBPassword: config.Secret(pw.value),
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
	assert.Equal(t, "http://localhost:4317", cfg.OTelEndpoint)
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
	for _, key := range []string{"API_TRUST_PROXY", "PROXY_PROTOCOL"} {
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
	t.Setenv("PROXY_PROTOCOL", "0")

	cfg, err := config.Load()
	require.NoError(t, err)
	assert.False(t, cfg.ProxyProtocol)
}

// The OTel SDK reads OTEL_EXPORTER_OTLP_ENDPOINT itself and wants a URL, so a bare
// host:port logged a parse error on every boot. The scheme also says whether the
// exporters use TLS, which is why the separate insecure switch is gone.
func TestLoad_OTelEndpointIsAURL(t *testing.T) {
	for _, tc := range []struct {
		env, want string
	}{
		{env: "development", want: "http://localhost:4317"},
		{env: "staging", want: "http://localhost:4317"},
		{env: "production", want: "https://localhost:4317"},
	} {
		t.Run("default in "+tc.env, func(t *testing.T) {
			t.Setenv("ENV", tc.env)
			t.Setenv("DB_PASSWORD", "secret")
			t.Setenv("DB_SSLMODE", "require")
			t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")

			cfg, err := config.Load()
			require.NoError(t, err)
			assert.Equal(t, tc.want, cfg.OTelEndpoint)
		})
	}
	for _, endpoint := range []string{"http://alloy:4317", "https://otel.example.com:4317"} {
		t.Run("accepts "+endpoint, func(t *testing.T) {
			t.Setenv("ENV", "development")
			t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", endpoint)

			cfg, err := config.Load()
			require.NoError(t, err)
			assert.Equal(t, endpoint, cfg.OTelEndpoint)
		})
	}
	for _, endpoint := range []string{"alloy:4317", "localhost:4317", "%%%", "grpc://alloy:4317", "http://"} {
		t.Run("refuses "+endpoint, func(t *testing.T) {
			t.Setenv("ENV", "development")
			t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", endpoint)

			_, err := config.Load()
			require.ErrorContains(t, err, "invalid OTEL_EXPORTER_OTLP_ENDPOINT")
		})
	}
}

func TestLoad_OTelInsecureFailsTheBoot(t *testing.T) {
	t.Setenv("ENV", "development")
	t.Setenv("OTEL_EXPORTER_OTLP_INSECURE", "true")

	_, err := config.Load()
	require.ErrorContains(t, err, "invalid OTEL_EXPORTER_OTLP_INSECURE")
	require.ErrorContains(t, err, "http:// is plaintext")
}

// ALLOW_INSECURE_DB used to let a plaintext database through outside development.
// It is gone, and a deployment still setting it has to hear so rather than quietly
// switching to TLS, whatever the environment and whatever the value.
func TestLoad_AllowInsecureDBFailsTheBoot(t *testing.T) {
	for _, env := range []string{"development", "staging", "production"} {
		for _, value := range []string{"true", "false"} {
			t.Run(env+"="+value, func(t *testing.T) {
				t.Setenv("ENV", env)
				t.Setenv("DB_PASSWORD", "secret")
				t.Setenv("DB_SSLMODE", "require")
				t.Setenv("ALLOW_INSECURE_DB", value)

				_, err := config.Load()
				require.ErrorContains(t, err, "invalid ALLOW_INSECURE_DB")
				require.ErrorContains(t, err, "only accepted with ENV=development")
			})
		}
	}
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

// A .env that fails to parse used to be ignored, so the server booted without the
// settings it was written to provide.
func TestResolveEnv_MalformedDotEnvFailsTheBoot(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".env"), []byte("DB_NAME=\"unterminated\nnot a pair\n"), 0o600))
	t.Chdir(dir)
	t.Setenv("ENV", "development")

	_, err := config.Load()
	require.ErrorContains(t, err, ".env")
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

// prefer and allow fall back to plaintext whenever the server declines TLS, so
// outside development they are disable with extra steps. No host is exempt: "db" is
// only as private as the network compose happens to put it on.
func TestValidate_OnlyDevelopmentMayUsePlaintext(t *testing.T) {
	t.Parallel()
	base := func(env, mode, host string) *config.Config {
		return &config.Config{
			Env: env, DBPassword: "secret", DBHost: host, DBSSLMode: mode,
			RateLimitCount: 5, RateLimitWindow: time.Second, DBMaxOpenConns: 25,
			MaxConnections: 1, APIRequestsPerMinute: 1,
			RegistrationLimit: 1, RegistrationWindow: time.Hour,
			RegistrationLimitWide: 1, RegistrationGlobalLimit: 1,
		}
	}

	tests := []struct {
		env, mode, host string
		wantErr         bool
	}{
		{env: "production", mode: "require", host: "db.example.com"},
		{env: "production", mode: "verify-ca", host: "db.example.com"},
		{env: "production", mode: "verify-full", host: "db.example.com"},
		{env: "production", mode: "prefer", host: "db.example.com", wantErr: true},
		{env: "production", mode: "allow", host: "db.example.com", wantErr: true},
		{env: "production", mode: "disable", host: "db.example.com", wantErr: true},
		{env: "production", mode: "disable", host: "db", wantErr: true},
		{env: "production", mode: "disable", host: "localhost", wantErr: true},
		{env: "production", mode: "prefer", host: "127.0.0.1", wantErr: true},
		{env: "staging", mode: "require", host: "db"},
		{env: "staging", mode: "disable", host: "db", wantErr: true},
		{env: "development", mode: "disable", host: "db"},
		{env: "development", mode: "prefer", host: "db.example.com"},
	}
	for _, tt := range tests {
		t.Run(tt.env+"/"+tt.mode+"@"+tt.host, func(t *testing.T) {
			t.Parallel()
			err := base(tt.env, tt.mode, tt.host).Validate()
			if !tt.wantErr {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, "DB_SSLMODE="+tt.mode)
			require.ErrorContains(t, err, "ENV="+tt.env)
			require.ErrorContains(t, err, "require, verify-ca or verify-full")
			assert.NotContains(t, err.Error(), "ALLOW_INSECURE_DB", "the escape hatch is gone")
		})
	}
}

// make loadtest registers one account per session, far past the production budget
// of five an hour, so the budget has to be settable without a rebuild.
func TestLoad_RegistrationBudget(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		t.Setenv("ENV", "development")
		t.Setenv("REGISTRATION_LIMIT", "")
		t.Setenv("REGISTRATION_LIMIT_WIDE", "")
		t.Setenv("REGISTRATION_GLOBAL_LIMIT", "")
		t.Setenv("REGISTRATION_WINDOW", "")

		cfg, err := config.Load()
		require.NoError(t, err)
		assert.Equal(t, 5, cfg.RegistrationLimit)
		assert.Equal(t, 20, cfg.RegistrationLimitWide)
		assert.Equal(t, 600, cfg.RegistrationGlobalLimit)
		assert.Equal(t, time.Hour, cfg.RegistrationWindow)
	})
	t.Run("override", func(t *testing.T) {
		t.Setenv("ENV", "development")
		t.Setenv("REGISTRATION_LIMIT", "10000")
		t.Setenv("REGISTRATION_LIMIT_WIDE", "20000")
		t.Setenv("REGISTRATION_GLOBAL_LIMIT", "30000")
		t.Setenv("REGISTRATION_WINDOW", "1m")

		cfg, err := config.Load()
		require.NoError(t, err)
		assert.Equal(t, 10000, cfg.RegistrationLimit)
		assert.Equal(t, 20000, cfg.RegistrationLimitWide)
		assert.Equal(t, 30000, cfg.RegistrationGlobalLimit)
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

// RATE_LIMIT_WINDOW is a Go duration, like REGISTRATION_WINDOW, and replaced the
// millisecond RATE_LIMIT_WINDOW_MS. The old name has to fail the boot: silently
// ignored, an operator's tuned window would fall back to the default.
func TestLoad_RateLimitWindow(t *testing.T) {
	t.Run("a duration", func(t *testing.T) {
		t.Setenv("ENV", "development")
		t.Setenv("RATE_LIMIT_WINDOW", "250ms")

		cfg, err := config.Load()
		require.NoError(t, err)
		assert.Equal(t, 250*time.Millisecond, cfg.RateLimitWindow)
	})
	t.Run("a bare number is not a duration", func(t *testing.T) {
		t.Setenv("ENV", "development")
		t.Setenv("RATE_LIMIT_WINDOW", "1000")

		_, err := config.Load()
		require.ErrorContains(t, err, "RATE_LIMIT_WINDOW")
	})
	t.Run("the old millisecond name fails the boot", func(t *testing.T) {
		t.Setenv("ENV", "development")
		t.Setenv("RATE_LIMIT_WINDOW_MS", "1000")

		_, err := config.Load()
		require.ErrorContains(t, err, "renamed RATE_LIMIT_WINDOW")
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
			Env:                     "development",
			RateLimitCount:          1,
			RateLimitWindow:         time.Millisecond,
			DBMaxOpenConns:          1,
			MaxConnections:          1,
			APIRequestsPerMinute:    1,
			RegistrationLimit:       1,
			RegistrationWindow:      time.Second,
			RegistrationLimitWide:   1,
			RegistrationGlobalLimit: 1,
		}
	}

	require.NoError(t, base().Validate(), "the documented minimum of each limit is valid")

	tests := []struct {
		name    string
		breakIt func(*config.Config)
		want    string
	}{
		{name: "no connections allowed", breakIt: func(c *config.Config) { c.RateLimitCount = 0 }, want: "RATE_LIMIT_CONNECTIONS"},
		{name: "sub-millisecond window", breakIt: func(c *config.Config) { c.RateLimitWindow = time.Microsecond }, want: "RATE_LIMIT_WINDOW"},
		{name: "no db connections", breakIt: func(c *config.Config) { c.DBMaxOpenConns = 0 }, want: "DB_MAX_OPEN_CONNS"},
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
			name:    "no registrations per /48",
			breakIt: func(c *config.Config) { c.RegistrationLimitWide = 0 },
			want:    "REGISTRATION_LIMIT_WIDE",
		},
		{
			name:    "no registrations at all",
			breakIt: func(c *config.Config) { c.RegistrationGlobalLimit = 0 },
			want:    "REGISTRATION_GLOBAL_LIMIT",
		},
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

// A Config is logged, printed and marshalled whole, so the password has to be
// unprintable by construction: every formatter a careless debug line might use is
// checked here, and only DSN, which the driver needs, may carry it.
func TestConfig_PasswordNeverRenders(t *testing.T) {
	t.Parallel()
	const password = "super-secret-value"
	cfg := &config.Config{DBHost: "db", DBUser: "postgres", DBName: "n", DBPort: 5432, DBPassword: password}

	logWith := func(newHandler func(io.Writer) slog.Handler) string {
		var buf bytes.Buffer
		slog.New(newHandler(&buf)).Info("config", "config", cfg, "password", cfg.DBPassword)
		return buf.String()
	}
	marshalled, err := json.Marshal(cfg)
	require.NoError(t, err)

	rendered := map[string]string{
		"%v":           fmt.Sprintf("%v", cfg),
		"%+v":          fmt.Sprintf("%+v", cfg),
		"%#v":          fmt.Sprintf("%#v", cfg),
		"Sprint":       fmt.Sprint(cfg.DBPassword),
		"%q":           fmt.Sprintf("%q", cfg.DBPassword),
		"%x":           fmt.Sprintf("%x", cfg.DBPassword),
		"%d":           fmt.Sprintf("%d", cfg.DBPassword),
		"%t":           fmt.Sprintf("%t", cfg.DBPassword),
		"%s":           fmt.Sprintf("%s", cfg.DBPassword),
		"%10s":         fmt.Sprintf("%10s", cfg.DBPassword),
		"slog text":    logWith(func(w io.Writer) slog.Handler { return slog.NewTextHandler(w, nil) }),
		"slog json":    logWith(func(w io.Writer) slog.Handler { return slog.NewJSONHandler(w, nil) }),
		"json.Marshal": string(marshalled),
	}
	for format, out := range rendered {
		assert.NotContainsf(t, out, password, "%s printed the password", format)
		assert.NotContainsf(t, out, hex.EncodeToString([]byte(password)), "%s printed the password in hex", format)
	}
	assert.Contains(t, rendered["%+v"], "[REDACTED]", "a set password still shows it is set")
	assert.Contains(t, rendered["%+v"], "postgres", "everything else still needs to be readable")
	assert.Contains(t, rendered["slog json"], `"DBPassword":"[REDACTED]"`)
	assert.Contains(t, cfg.DSN(), ":"+password+"@", "the driver still gets the password")
}

// An unset password is worth seeing in a dump; it is not a secret.
func TestSecret_EmptyRendersEmpty(t *testing.T) {
	t.Parallel()
	assert.Empty(t, fmt.Sprint(config.Secret("")))
	assert.Equal(t, "hunter2", config.Secret("hunter2").Reveal())
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
