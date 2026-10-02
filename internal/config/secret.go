package config

import "log/slog"

// Secret is a credential that renders as [REDACTED] through every fmt verb, slog
// and encoding/json, so a Config can be logged whole. Reveal is the only way back to
// the value.
type Secret string

func (s Secret) Reveal() string { return string(s) }

// String keeps an unset secret visibly empty; knowing it is missing is not a leak.
func (s Secret) String() string {
	if s == "" {
		return ""
	}
	return "[REDACTED]"
}

func (s Secret) GoString() string             { return s.String() }
func (s Secret) LogValue() slog.Value         { return slog.StringValue(s.String()) }
func (s Secret) MarshalText() ([]byte, error) { return []byte(s.String()), nil }
