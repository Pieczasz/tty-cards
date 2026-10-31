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
		// The PROXY listener matches with net.ParseCIDR, which reads ::ffff:0.0.0.0/96
		// as 0.0.0.0/0; the stats API matches with netip, which never puts an IPv4
		// peer in it. Refusing the form keeps both consumers trusting the same peers.
		if err == nil && prefix.Addr().Is4In6() {
			err = fmt.Errorf("%s is IPv4-mapped; write the IPv4 network instead", field)
		}
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
		if err := loadDotEnv(); err != nil {
			return "", err
		}
	default:
		return "", fmt.Errorf("invalid ENV %q: want production, staging or development", env)
	}
	return env, nil
}

// loadDotEnv refuses an ENV line because ENV is settled before .env is read
// (production never reads it), so the line could only mislead: ENV=staging there
// would boot as development with every fail-closed check off. A missing .env is
// normal; one that fails to parse would otherwise boot without what it set.
func loadDotEnv() error {
	vars, err := godotenv.Read()
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load .env: %w", err)
	}
	if _, ok := vars["ENV"]; ok {
		return errors.New("ENV in .env is never read: remove it and set ENV in the process environment")
	}
	if err := godotenv.Load(); err != nil {
		return fmt.Errorf("load .env: %w", err)
	}
	return nil
}

func getEnv(key string, fallback string) string {
	return cmp.Or(os.Getenv(key), fallback)
}
