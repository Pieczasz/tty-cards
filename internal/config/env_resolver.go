package config

import (
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

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

// endpointURL insists on a scheme because the OTel SDK reads the same variable as a
// URL, and a bare host:port parses there as scheme "alloy" with no host.
func (r *envReader) endpointURL(key, fallback string) string {
	raw := getEnv(key, fallback)
	u, err := url.Parse(raw)
	if err == nil && ((u.Scheme != "http" && u.Scheme != "https") || u.Host == "") {
		err = errors.New("want an http:// or https:// URL such as http://alloy:4317")
	}
	if err != nil {
		r.fail(key, err)
	}
	return raw
}

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

func (r *envReader) logLevel(key string) slog.Level {
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

func resolveEnvironment() (string, error) {
	env := getEnv("ENV", envDevelopment)
	switch env {
	case envProduction:
	case envDevelopment, envStaging:
		// A missing .env is normal; one that fails to parse would otherwise boot
		// without whatever it was meant to set.
		if err := godotenv.Load(); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("load .env: %w", err)
		}
	default:
		return "", fmt.Errorf("invalid ENV %q: want production, staging or development", env)
	}
	return env, nil
}

func getEnv(key string, fallback string) string {
	return cmp.Or(os.Getenv(key), fallback)
}
