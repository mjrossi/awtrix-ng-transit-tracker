package config

import (
	"fmt"
	"strings"
	"time"
)

// Duration is a time.Duration written as a string such as "45s" or "2m".
type Duration time.Duration

// D returns the value as a time.Duration.
func (d Duration) D() time.Duration { return time.Duration(d) }

// UnmarshalText implements encoding.TextUnmarshaler.
func (d *Duration) UnmarshalText(b []byte) error {
	v, err := time.ParseDuration(string(b))
	if err != nil {
		return fmt.Errorf("duration %q: %w", b, err)
	}
	*d = Duration(v)
	return nil
}

// MarshalText implements encoding.TextMarshaler.
func (d Duration) MarshalText() ([]byte, error) { return []byte(d.D().String()), nil }

// DailyWindow is a daily window such as "06:00-23:30". A window whose end
// is before its start runs past midnight, so "18:00-02:00" covers the
// evening and early morning. An empty one is unset.
//
// It is a plain string, checked by Validate, because go-toml names the key
// of an unquoted value only for types without UnmarshalText.
type DailyWindow string

// IsSet reports whether there is a window.
func (w DailyWindow) IsSet() bool { return strings.TrimSpace(string(w)) != "" }

// Validate reports whether the window is unset or well formed.
func (w DailyWindow) Validate() error {
	if !w.IsSet() {
		return nil
	}
	_, _, err := w.parse()
	return err
}

// Contains reports whether t, in its own location, is inside a valid window.
func (w DailyWindow) Contains(t time.Time) bool {
	start, end, err := w.parse()
	if err != nil {
		return false
	}
	m := t.Hour()*60 + t.Minute()
	if start < end {
		return m >= start && m < end
	}
	return m >= start || m < end
}

// parse returns the bounds in minutes after midnight.
func (w DailyWindow) parse() (start, end int, err error) {
	s := strings.TrimSpace(string(w))
	from, to, ok := strings.Cut(s, "-")
	if !ok {
		return 0, 0, fmt.Errorf("%q: want HH:MM-HH:MM", s)
	}
	if start, err = parseClock(from); err != nil {
		return 0, 0, fmt.Errorf("%q: %w", s, err)
	}
	if end, err = parseClock(to); err != nil {
		return 0, 0, fmt.Errorf("%q: %w", s, err)
	}
	if start == end {
		return 0, 0, fmt.Errorf("%q: start and end are equal; leave it empty for no window", s)
	}
	return start, end, nil
}

func parseClock(s string) (int, error) {
	t, err := time.Parse("15:04", strings.TrimSpace(s))
	if err != nil {
		return 0, fmt.Errorf("time %q: want HH:MM", s)
	}
	return t.Hour()*60 + t.Minute(), nil
}
