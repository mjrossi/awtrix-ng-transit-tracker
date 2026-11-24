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

	"github.com/mjrossi/urbanist-timetable/internal/render"
	"github.com/mjrossi/urbanist-timetable/pkg/awtrix"
	"github.com/mjrossi/urbanist-timetable/pkg/transit"
	"github.com/mjrossi/urbanist-timetable/pkg/transit/catalog"
)

// Defaults applied to boards that leave a field out.
const (
	DefaultPollInterval = 60 * time.Second
	DefaultWindow       = 90 * time.Minute
	DefaultPerRow       = 3
	DefaultMaxStale     = 10 * time.Minute
	// MinPollInterval protects upstream APIs from a typo like "5s".
	MinPollInterval = 15 * time.Second
)

// Config is the whole file.
type Config struct {
	// Timezone is the IANA name for active hours on boards that set none.
	// Empty means the process's local zone (the TZ environment variable in
	// Docker).
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

// Board is one set of departures, fed by one or more stops. On the clock it
// is one pushed app, named after the board.
type Board struct {
	// Name identifies the board, and is its app name on the clock.
	Name         string      `toml:"name"`
	PollInterval Duration    `toml:"poll_interval"`
	Window       Duration    `toml:"window"`
	ActiveHours  ActiveHours `toml:"active_hours"`
	// Timezone is the IANA name for active hours; empty uses the top-level
	// timezone.
	Timezone string `toml:"timezone"`
	PerRow   int    `toml:"per_row"`
	// MaxStale is how long the last good data is reused while providers
	// fail.
	MaxStale Duration   `toml:"max_stale"`
	Stops    []Stop     `toml:"stops"`
	Clock    BoardClock `toml:"clock"`

	location *time.Location
}

// BoardClock is how a board appears on the clock: the [boards.clock] table.
type BoardClock struct {
	// Layout is "departure" (one line and direction, like a platform sign)
	// or "rows" (one line and direction per row).
	Layout  render.Layout `toml:"layout"`
	MaxRows int           `toml:"max_rows"`
	// Lifetime is how long the clock keeps the app without a fresh push.
	// Defaults to three poll intervals.
	Lifetime Duration `toml:"lifetime"`
	// Duration is how long the app is shown per rotation; 0 uses the
	// clock's setting.
	Duration  Duration `toml:"duration"`
	Font      string   `toml:"font"`
	EmptyText string   `toml:"empty_text"`
	// Overflow is "scroll" or "clip" for names wider than their box.
	Overflow render.Overflow `toml:"overflow"`
	// Labels maps headsigns to shorter display names.
	Labels map[string]string `toml:"labels"`
}

// Render returns the renderer options for the board, with the renderer's
// defaults for anything left unset.
func (c BoardClock) Render() render.Options { return c.options().WithDefaults() }

// options is the board's renderer options as written, which validation
// needs: defaults would replace an invalid max_rows before it is checked.
func (c BoardClock) options() render.Options {
	return render.Options{
		Layout:    c.Layout,
		MaxRows:   c.MaxRows,
		Labels:    c.Labels,
		Overflow:  c.Overflow,
		Font:      c.Font,
		EmptyText: c.EmptyText,
	}
}

// Location is the board's resolved time zone.
func (b Board) Location() *time.Location { return orLocal(b.location) }

func orLocal(loc *time.Location) *time.Location {
	if loc == nil {
		return time.Local
	}
	return loc
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
	// KeepCanceled shows canceled trips, struck through, instead of
	// dropping them.
	KeepCanceled bool `toml:"keep_canceled"`
}

// Filter returns the transit filter for the stop.
func (s Stop) Filter() transit.Filter {
	return transit.Filter{
		Lines:        s.Lines,
		Headsigns:    s.Headsigns,
		MinLead:      s.MinLead.D(),
		KeepCanceled: s.KeepCanceled,
	}
}

// Location is the resolved time zone.
func (c *Config) Location() *time.Location { return orLocal(c.location) }

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
		} else {
			c.location = loc
		}
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
		b.location = c.Location()
		if b.Timezone != "" {
			loc, err := time.LoadLocation(b.Timezone)
			if err != nil {
				errs = append(errs, fmt.Errorf("boards[%d] (%s): timezone: %w", i, b.Name, err))
			} else {
				b.location = loc
			}
		}
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
	if b.PerRow == 0 {
		b.PerRow = DefaultPerRow
	}
	if b.Clock.Lifetime == 0 {
		b.Clock.Lifetime = 3 * b.PollInterval
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
	if b.Clock.Lifetime < b.PollInterval {
		errs = append(errs, errors.New("clock.lifetime must be at least poll_interval, or the app expires between pushes"))
	}
	if err := b.Clock.options().Validate(); err != nil {
		errs = append(errs, fmt.Errorf("clock: %w", err))
	}
	if b.PerRow < 0 {
		errs = append(errs, errors.New("per_row must not be negative"))
	}
	if b.Window < 0 || b.MaxStale < 0 || b.Clock.Duration < 0 {
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
			return "", nil, fmt.Errorf("unknown system %q; run `urbanist-timetable systems` to list them", id)
		}
		merged := sys.Merge(user)
		if missing := sys.Missing(merged); len(missing) > 0 {
			return "", nil, fmt.Errorf("system %q needs %s; run `urbanist-timetable systems show %s`", id, strings.Join(missing, " and "), id)
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
