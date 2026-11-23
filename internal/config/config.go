// Package config loads and validates the TOML configuration file.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"

	"github.com/mjrossi/awtrix-ng-transit-tracker/internal/render"
	"github.com/mjrossi/awtrix-ng-transit-tracker/pkg/awtrix"
	"github.com/mjrossi/awtrix-ng-transit-tracker/pkg/transit"
	"github.com/mjrossi/awtrix-ng-transit-tracker/pkg/transit/catalog"
)

// Defaults applied to boards that leave a field out.
const (
	DefaultPollInterval = 60 * time.Second
	DefaultWindow       = 90 * time.Minute
	DefaultMaxRows      = 2
	DefaultPerRow       = 3
	DefaultMaxStale     = 10 * time.Minute
	// MinPollInterval protects upstream APIs from a typo like "5s".
	MinPollInterval = 15 * time.Second
)

// Config is the whole file.
type Config struct {
	// Timezone is an IANA name used for active hours. Empty means the
	// process's local zone (the TZ environment variable in Docker).
	Timezone  string                    `toml:"timezone"`
	Clock     Clock                     `toml:"clock"`
	Providers map[string]map[string]any `toml:"providers"`
	Boards    []Board                   `toml:"boards"`

	location *time.Location
}

// Clock says how to reach the AWTRIX NG clock.
type Clock struct {
	URL      string `toml:"url"`
	Username string `toml:"username"`
	Password string `toml:"password"`
}

// Board is one pushed app on the clock, fed by one or more stops.
type Board struct {
	// Name is the app name on the clock.
	Name string `toml:"name"`
	// Layout is "departure" (one line and direction, like a platform sign)
	// or "rows" (one line and direction per row).
	Layout       string      `toml:"layout"`
	PollInterval Duration    `toml:"poll_interval"`
	Window       Duration    `toml:"window"`
	ActiveHours  ActiveHours `toml:"active_hours"`
	MaxRows      int         `toml:"max_rows"`
	PerRow       int         `toml:"per_row"`
	// Lifetime is how long the clock keeps the app without a fresh push.
	// Defaults to three poll intervals.
	Lifetime Duration `toml:"lifetime"`
	// MaxStale is how long the last good data is reused while providers
	// fail.
	MaxStale Duration `toml:"max_stale"`
	// Duration is how long the app is shown per rotation; 0 uses the
	// clock's setting.
	Duration  Duration `toml:"duration"`
	Font      string   `toml:"font"`
	EmptyText string   `toml:"empty_text"`
	// Overflow is "scroll" or "clip" for names wider than their box.
	Overflow string `toml:"overflow"`
	// Labels maps headsigns to shorter display names.
	Labels map[string]string `toml:"labels"`
	Stops  []Stop            `toml:"stops"`
}

// Stop is one stop on a board and the filters applied to it.
type Stop struct {
	Provider  string   `toml:"provider"`
	Stop      string   `toml:"stop"`
	Lines     []string `toml:"lines"`
	Headsigns []string `toml:"headsigns"`
	// MinLead hides departures sooner than this, such as the walk to the
	// stop.
	MinLead Duration `toml:"min_lead"`
}

// Filter returns the transit filter for the stop.
func (s Stop) Filter() transit.Filter {
	return transit.Filter{Lines: s.Lines, Headsigns: s.Headsigns, MinLead: s.MinLead.D()}
}

// Location is the resolved time zone.
func (c *Config) Location() *time.Location {
	if c.location == nil {
		return time.Local
	}
	return c.location
}

// Load reads, expands and validates the file at path.
func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	return Parse(b, os.LookupEnv)
}

// Parse decodes and validates a configuration. ${NAME} references in the
// clock and provider settings are replaced with environment variables
// through lookup, so secrets can stay out of the file.
func Parse(b []byte, lookup func(string) (string, bool)) (*Config, error) {
	var c Config
	dec := toml.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		var strict *toml.StrictMissingError
		if errors.As(err, &strict) {
			return nil, fmt.Errorf("config: %s", strict.String())
		}
		return nil, fmt.Errorf("config: %w", err)
	}
	if err := c.expandEnv(lookup); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	if err := c.validate(); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	return &c, nil
}

var envRef = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// expandEnv works on decoded values rather than the raw file so that
// references in comments are left alone and values need no TOML escaping.
func (c *Config) expandEnv(lookup func(string) (string, bool)) error {
	missing := map[string]bool{}
	expand := func(s string) string {
		return envRef.ReplaceAllStringFunc(s, func(m string) string {
			name := envRef.FindStringSubmatch(m)[1]
			v, ok := lookup(name)
			if !ok {
				missing[name] = true
				return m
			}
			return v
		})
	}
	c.Clock.URL = expand(c.Clock.URL)
	c.Clock.Username = expand(c.Clock.Username)
	c.Clock.Password = expand(c.Clock.Password)
	for _, opts := range c.Providers {
		expandAny(opts, expand)
	}
	if len(missing) > 0 {
		names := make([]string, 0, len(missing))
		for n := range missing {
			names = append(names, n)
		}
		slices.Sort(names)
		return fmt.Errorf("environment variables not set: %s", strings.Join(names, ", "))
	}
	return nil
}

// expandAny expands strings in v, updating maps and slices in place.
func expandAny(v any, expand func(string) string) any {
	switch x := v.(type) {
	case string:
		return expand(x)
	case map[string]any:
		for k, e := range x {
			x[k] = expandAny(e, expand)
		}
		return x
	case []any:
		for i, e := range x {
			x[i] = expandAny(e, expand)
		}
		return x
	default:
		return v
	}
}

func (c *Config) validate() error {
	var errs []error
	if c.Timezone != "" {
		loc, err := time.LoadLocation(c.Timezone)
		if err != nil {
			errs = append(errs, fmt.Errorf("timezone: %w", err))
		}
		c.location = loc
	}
	if c.Clock.URL == "" {
		errs = append(errs, errors.New("clock.url is required"))
	}
	if len(c.Boards) == 0 {
		errs = append(errs, errors.New("at least one [[boards]] entry is required"))
	}
	for name, raw := range c.Providers {
		if _, _, err := ResolveProvider(name, raw); err != nil {
			errs = append(errs, fmt.Errorf("providers.%s: %w", name, err))
		}
	}
	seen := map[string]bool{}
	for i := range c.Boards {
		b := &c.Boards[i]
		b.applyDefaults()
		if err := b.validate(c.Providers); err != nil {
			errs = append(errs, fmt.Errorf("boards[%d] (%s): %w", i, b.Name, err))
		}
		if seen[b.Name] {
			errs = append(errs, fmt.Errorf("boards[%d]: duplicate name %q", i, b.Name))
		}
		seen[b.Name] = true
	}
	return errors.Join(errs...)
}

func (b *Board) applyDefaults() {
	if b.PollInterval == 0 {
		b.PollInterval = Duration(DefaultPollInterval)
	}
	if b.Window == 0 {
		b.Window = Duration(DefaultWindow)
	}
	if b.Layout == "" {
		b.Layout = render.LayoutDeparture
	}
	if b.Overflow == "" {
		b.Overflow = render.OverflowScroll
	}
	if b.MaxRows == 0 {
		b.MaxRows = DefaultMaxRows
	}
	if b.PerRow == 0 {
		b.PerRow = DefaultPerRow
	}
	if b.Lifetime == 0 {
		b.Lifetime = 3 * b.PollInterval
	}
	if b.MaxStale == 0 {
		b.MaxStale = Duration(DefaultMaxStale)
	}
}

func (b *Board) validate(providers map[string]map[string]any) error {
	var errs []error
	if !awtrix.ValidAppName(b.Name) {
		errs = append(errs, fmt.Errorf("name %q must be 1-32 letters, digits, - or _", b.Name))
	}
	if b.PollInterval.D() < MinPollInterval {
		errs = append(errs, fmt.Errorf("poll_interval must be at least %s", MinPollInterval))
	}
	if b.Lifetime < b.PollInterval {
		errs = append(errs, errors.New("lifetime must be at least poll_interval, or the app expires between pushes"))
	}
	if b.Layout != render.LayoutDeparture && b.Layout != render.LayoutRows {
		errs = append(errs, fmt.Errorf("layout %q must be %q or %q", b.Layout, render.LayoutDeparture, render.LayoutRows))
	}
	if b.Overflow != render.OverflowScroll && b.Overflow != render.OverflowClip {
		errs = append(errs, fmt.Errorf("overflow %q must be %q or %q", b.Overflow, render.OverflowScroll, render.OverflowClip))
	}
	if b.MaxRows < 0 || b.PerRow < 0 {
		errs = append(errs, errors.New("max_rows and per_row must not be negative"))
	}
	if b.Window < 0 || b.MaxStale < 0 || b.Duration < 0 {
		errs = append(errs, errors.New("durations must not be negative"))
	}
	if len(b.Stops) == 0 {
		errs = append(errs, errors.New("at least one [[boards.stops]] entry is required"))
	}
	for j, s := range b.Stops {
		if s.Provider == "" || s.Stop == "" {
			errs = append(errs, fmt.Errorf("stops[%d]: provider and stop are required", j))
			continue
		}
		if _, ok := providers[s.Provider]; !ok {
			errs = append(errs, fmt.Errorf("stops[%d]: no [providers.%s] section", j, s.Provider))
		}
		if s.MinLead < 0 {
			errs = append(errs, fmt.Errorf("stops[%d]: min_lead must not be negative", j))
		}
	}
	return errors.Join(errs...)
}

// Provider returns the type and options of the [providers.NAME] block.
// See ResolveProvider.
func (c *Config) Provider(name string) (typ string, opts ProviderOptions, err error) {
	raw, ok := c.Providers[name]
	if !ok {
		return "", nil, fmt.Errorf("no [providers.%s] section", name)
	}
	return ResolveProvider(name, raw)
}

// ResolveProvider works out a provider block's type and options:
//   - `system = "ID"` takes the type and settings of a catalog system,
//     overlaid with the block's other keys;
//   - otherwise `type = "..."` names the type;
//   - otherwise the block's NAME is the type.
func ResolveProvider(name string, raw map[string]any) (typ string, opts ProviderOptions, err error) {
	user := maps.Clone(raw)
	sysID, hasSystem := user["system"]
	typeName, hasType := user["type"]
	delete(user, "system")
	delete(user, "type")

	switch {
	case hasSystem && hasType:
		return "", nil, errors.New("set either system or type, not both")
	case hasSystem:
		id, _ := sysID.(string)
		sys, ok := catalog.Get(id)
		if !ok {
			return "", nil, fmt.Errorf("unknown system %q; run `transit-tracker systems` to list them", id)
		}
		merged := sys.Merge(user)
		if missing := sys.Missing(merged); len(missing) > 0 {
			return "", nil, fmt.Errorf("system %q needs %s; run `transit-tracker systems show %s`", id, strings.Join(missing, " and "), id)
		}
		return sys.Provider, merged, nil
	case hasType:
		t, _ := typeName.(string)
		if t == "" {
			return "", nil, errors.New("type must be a non-empty string")
		}
		return t, user, nil
	default:
		return name, user, nil
	}
}

// ProviderOptions implements transit.Options for one [providers.NAME]
// block, decoding it strictly so typos in provider settings are reported.
type ProviderOptions map[string]any

// Decode implements transit.Options.
func (o ProviderOptions) Decode(v any) error {
	b, err := toml.Marshal(map[string]any(o))
	if err != nil {
		return fmt.Errorf("re-encode options: %w", err)
	}
	dec := toml.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		var strict *toml.StrictMissingError
		if errors.As(err, &strict) {
			// Only key names: strict.String() quotes the offending lines,
			// whose values have had ${NAME} secrets filled in by now.
			keys := make([]string, len(strict.Errors))
			for i, e := range strict.Errors {
				keys[i] = strings.Join(e.Key(), ".")
			}
			return fmt.Errorf("unknown setting %s", strings.Join(keys, ", "))
		}
		return err
	}
	return nil
}
