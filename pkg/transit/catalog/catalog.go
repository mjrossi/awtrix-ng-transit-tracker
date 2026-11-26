// Package catalog holds ready-made provider settings for transit systems
// known to work, so a configuration can name a system instead of spelling
// out feed URLs:
//
//	[providers.nyc]
//	system = "mta-subway"
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

// System is one catalog entry: one network an agency runs, served by one
// provider block. An agency such as MTA runs several systems (the subway,
// LIRR); many agencies run one.
type System struct {
	ID string `toml:"-"`
	// Agency is who runs the system, as riders know it: "MTA".
	Agency string `toml:"agency"`
	// Name is the system's name within the agency, such as "Subway", or
	// empty when the entry covers all of the agency.
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
	RequiredOptions []string `toml:"required_options"`
	// MobilityDB and TransitlandFeeds name the same system in those
	// directories, so a search can show this entry instead.
	MobilityDB       []string       `toml:"mobility_db"`
	TransitlandFeeds []string       `toml:"transitland_feeds"`
	Options          map[string]any `toml:"options"`
}

// Title is the agency and the system's name: "MTA Subway", or "HSL" for an
// entry covering the whole agency.
func (s System) Title() string {
	if s.Name == "" {
		return s.Agency
	}
	return s.Agency + " " + s.Name
}

var (
	idPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	// directoryID is the shape of directory IDs: the Mobility Database's
	// (mdb-53, tld-716) and Transitland's onestop IDs, which start with a
	// one-letter kind (f-9q9-bart). Catalog IDs are kept clear of them so
	// both can name a system.
	directoryID = regexp.MustCompile(`^([a-z]+-[0-9]+|[a-z]-.*)$`)
)

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
	return parseSystem(file, b)
}

func parseSystem(file string, b []byte) (System, error) {
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
	case directoryID.MatchString(s.ID):
		return System{}, fmt.Errorf("catalog %s: ID must not look like a Mobility Database or Transitland ID", file)
	case s.Agency == "" || s.Provider == "" || s.SampleStop == "":
		return System{}, fmt.Errorf("catalog %s: agency, provider and sample_stop are required", file)
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
