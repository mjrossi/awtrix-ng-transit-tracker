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

// ActiveHours is a daily window such as "06:00-23:30". A window whose end
// is before its start runs past midnight, so "18:00-02:00" covers the
// evening and early morning. The zero value is always active.
type ActiveHours struct {
	start, end int // minutes after midnight
	set        bool
}

// UnmarshalText implements encoding.TextUnmarshaler.
func (a *ActiveHours) UnmarshalText(b []byte) error {
	s := strings.TrimSpace(string(b))
	if s == "" {
		*a = ActiveHours{}
		return nil
	}
	from, to, ok := strings.Cut(s, "-")
	if !ok {
		return fmt.Errorf("active_hours %q: want HH:MM-HH:MM", s)
	}
	start, err := parseClock(from)
	if err != nil {
		return fmt.Errorf("active_hours %q: %w", s, err)
	}
	end, err := parseClock(to)
	if err != nil {
		return fmt.Errorf("active_hours %q: %w", s, err)
	}
	if start == end {
		return fmt.Errorf("active_hours %q: start and end are equal; leave it empty to stay always active", s)
	}
	*a = ActiveHours{start: start, end: end, set: true}
	return nil
}

// MarshalText implements encoding.TextMarshaler.
func (a ActiveHours) MarshalText() ([]byte, error) {
	if !a.set {
		return []byte{}, nil
	}
	return fmt.Appendf(nil, "%02d:%02d-%02d:%02d", a.start/60, a.start%60, a.end/60, a.end%60), nil
}

// Contains reports whether t, in its own location, is inside the window.
func (a ActiveHours) Contains(t time.Time) bool {
	if !a.set {
		return true
	}
	m := t.Hour()*60 + t.Minute()
	if a.start < a.end {
		return m >= a.start && m < a.end
	}
	return m >= a.start || m < a.end
}

func parseClock(s string) (int, error) {
	t, err := time.Parse("15:04", strings.TrimSpace(s))
	if err != nil {
		return 0, fmt.Errorf("time %q: want HH:MM", s)
	}
	return t.Hour()*60 + t.Minute(), nil
}
