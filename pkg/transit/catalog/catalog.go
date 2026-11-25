// Package catalog holds ready-made provider settings for transit systems
// known to work, so a configuration can name a system instead of spelling
// out feed URLs:
//
//	[providers.nyc]
//	system = "nyc-subway"
//
// Each system is one TOML file in systems/, named by its ID. Resolve turns
// a provider block, with or without a system, into a provider type and its
// options; providers.Open does that and builds the provider.
package catalog

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"maps"
	"path"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/pelletier/go-toml/v2"
)

//go:embed systems/*.toml
var files embed.FS

// Statuses.
const (
	// StatusMaintained systems are checked by the maintainers.
	StatusMaintained = "maintained"
	// StatusCommunity systems were contributed and verified once.
	StatusCommunity = "community"
)

// System is one catalog entry.
type System struct {
	ID      string `toml:"-"`
	Name    string `toml:"name"`
	City    string `toml:"city"`
	Country string `toml:"country"`
	// Modes lists the modes the system serves, for display.
	Modes    []string `toml:"modes"`
	Provider string   `toml:"provider"`
	Status   string   `toml:"status"`
	// Verified is when the entry last passed a live check (YYYY-MM-DD).
	Verified string `toml:"verified"`
	// SampleStop is a busy stop used by live checks and examples.
	SampleStop  string `toml:"sample_stop"`
	InfoURL     string `toml:"info_url"`
	DataLicense string `toml:"data_license"`
	Notes       string `toml:"notes"`
	// RequiredOptions are settings users must add, such as API keys.
	RequiredOptions []string       `toml:"required_options"`
	Options         map[string]any `toml:"options"`
}

var idPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

var (
	loadOnce sync.Once
	systems  []System
	errLoad  error
)

func load() ([]System, error) {
	loadOnce.Do(func() {
		names, err := files.ReadDir("systems")
		if err != nil {
			errLoad = err
			return
		}
		var errs []error
		for _, e := range names {
			s, err := parse(e.Name())
			if err != nil {
				errs = append(errs, err)
				continue
			}
			systems = append(systems, s)
		}
		slices.SortFunc(systems, func(a, b System) int { return strings.Compare(a.ID, b.ID) })
		errLoad = errors.Join(errs...)
	})
	return systems, errLoad
}

func parse(file string) (System, error) {
	b, err := files.ReadFile(path.Join("systems", file))
	if err != nil {
		return System{}, err
	}
	var s System
	dec := toml.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		return System{}, fmt.Errorf("catalog %s: %w", file, err)
	}
	s.ID = strings.TrimSuffix(file, ".toml")
	switch {
	case !idPattern.MatchString(s.ID):
		return System{}, fmt.Errorf("catalog %s: ID must be lowercase words joined by -", file)
	case s.Name == "" || s.Provider == "" || s.SampleStop == "":
		return System{}, fmt.Errorf("catalog %s: name, provider and sample_stop are required", file)
	case s.Status != StatusMaintained && s.Status != StatusCommunity:
		return System{}, fmt.Errorf("catalog %s: status %q must be %q or %q", file, s.Status, StatusMaintained, StatusCommunity)
	}
	if s.Options == nil {
		s.Options = map[string]any{}
	}
	return s, nil
}

// All returns every system, sorted by ID.
func All() ([]System, error) {
	s, err := load()
	return slices.Clone(s), err
}

// Get returns the system with the given ID.
func Get(id string) (System, bool) {
	all, _ := load()
	i := slices.IndexFunc(all, func(s System) bool { return s.ID == id })
	if i < 0 {
		return System{}, false
	}
	return all[i], true
}

// Merge returns the system's options overlaid with the user's settings.
// User settings win, which is how API keys and overrides are added.
func (s System) Merge(user map[string]any) map[string]any {
	out := make(map[string]any, len(s.Options)+len(user))
	maps.Copy(out, s.Options)
	maps.Copy(out, user)
	return out
}

// Missing lists required options absent from opts.
func (s System) Missing(opts map[string]any) []string {
	var out []string
	for _, k := range s.RequiredOptions {
		if v, ok := opts[k]; !ok || v == "" {
			out = append(out, k)
		}
	}
	return out
}
