// Package directory finds transit systems beyond the catalog: in the
// Mobility Database's public feed list and, with an API key, Transitland's
// operators. Each result is a system, one schedule with its realtime feeds,
// whose ID works where a catalog system ID does, and which turns into a
// provider block.
//
// An agency, in these results, is whoever published the data: an
// authority, an operator, or for national feeds an aggregator.
package directory

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/mjrossi/urbanist-timetable/internal/fold"
	"github.com/mjrossi/urbanist-timetable/internal/tomlout"
	"github.com/mjrossi/urbanist-timetable/pkg/transit/catalog"
)

// Source says where a system was found.
type Source string

// Sources, in the order results list them.
const (
	SourceCatalog     Source = "catalog"
	SourceMobilityDB  Source = "mobilitydb"
	SourceTransitland Source = "transitland"
)

// Data says what departures a system gives.
type Data string

// Kinds of data.
const (
	DataRealtime Data = "realtime"
	DataSchedule Data = "schedule"
)

// System is one search result.
type System struct {
	ID     string `json:"id"`
	Agency string `json:"agency"`
	// Name is the system's name within the agency, or empty.
	Name   string `json:"name,omitempty"`
	Place  string `json:"place,omitempty"`
	Data   Data   `json:"data"`
	Source Source `json:"source"`
	// Needs lists the environment variables the system's block reads,
	// such as API keys.
	Needs []string `json:"needs,omitempty"`
	// KeyInfo is where to get a key, when one is needed and known.
	KeyInfo string `json:"key_info,omitempty"`
	License string `json:"license,omitempty"`
	// StaticURL is the schedule, when known without further requests.
	StaticURL string `json:"-"`
}

// Title is the agency and the system's name.
func (s System) Title() string {
	if s.Name == "" || strings.EqualFold(s.Name, s.Agency) {
		return s.Agency
	}
	return s.Agency + " " + s.Name
}

// Block is a provider block for a system.
type Block struct {
	// System is set for a catalog system, whose block is `system = "ID"`.
	System string
	// Type and Options are the block otherwise. Strings may hold ${NAME}
	// references to environment variables.
	Type    string
	Options map[string]any
	// Comment explains where the block came from.
	Comment string
}

// TOML renders the block as a [providers] table named name, with the usual
// keys in reading order and any others after them.
func (b Block) TOML(name string) (string, error) {
	var sb strings.Builder
	fmt.Fprintf(&sb, "[providers.%s]\n", tomlout.Key(name))
	settings := b.Settings()
	keys := slices.SortedFunc(maps.Keys(settings), func(x, y string) int {
		return cmp.Or(cmp.Compare(keyOrder(x), keyOrder(y)), strings.Compare(x, y))
	})
	for _, k := range keys {
		v := settings[k]
		s, err := tomlout.Value(v)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&sb, "%s = %s\n", k, s)
	}
	return sb.String(), nil
}

// keyOrder ranks the keys blocks usually have, in the order they read
// best; others rank after them.
func keyOrder(k string) int {
	order := []string{"system", "type", "static_url", "realtime_urls", "feed", "api_key", "headers"}
	if i := slices.Index(order, k); i >= 0 {
		return i
	}
	return len(order)
}

// Settings is the block as configuration settings.
func (b Block) Settings() map[string]any {
	if b.System != "" {
		return map[string]any{"system": b.System}
	}
	out := map[string]any{"type": b.Type}
	for k, v := range b.Options {
		out[k] = v
	}
	return out
}

// Directory searches and resolves systems. The zero value searches the
// catalog and the Mobility Database without a cache.
type Directory struct {
	// CacheDir keeps the Mobility Database's list between runs; empty
	// downloads it each time.
	CacheDir string
	// HTTP is the client for every request; nil uses a default.
	HTTP *http.Client
	// TransitlandKey enables Transitland results.
	TransitlandKey string
	// TransitlandURL and MobilityDBURL override the services' addresses,
	// for tests.
	TransitlandURL string
	MobilityDBURL  string
	Log            *slog.Logger
	// Now is the time, for the cache's age; nil uses the wall clock.
	Now func() time.Time
}

// TransitlandKeyVar is the environment variable for Transitland's API key,
// as the example configuration names it.
const TransitlandKeyVar = "TRANSITLAND_API_KEY"

// IsID reports whether id has the shape of a directory ID: a Mobility
// Database ID such as "mdb-53", "tld-716" or "ntd-23", or a Transitland
// feed Onestop ID such as "f-9q9-bart".
func IsID(id string) bool {
	return mdbID.MatchString(id) && !catalogPrefix(id) || strings.HasPrefix(id, "f-") && len(id) > 2
}

// mdbID takes any prefix: the Mobility Database names feeds after where it
// found them (mdb, tld, ntd, tdg, jbda, odpt), and adds sources over time.
// The catalog keeps its IDs out of this shape.
var mdbID = regexp.MustCompile(`^[a-z]+-[0-9]+$`)

// catalogPrefix reports whether id starts like a catalog ID, as "bart-2"
// does: a mistyped catalog ID, better answered with a suggestion at once
// than by loading the Mobility Database's list to find it missing.
func catalogPrefix(id string) bool {
	prefix, _, _ := strings.Cut(id, "-")
	all, _ := catalog.All()
	return slices.ContainsFunc(all, func(s catalog.System) bool {
		first, _, _ := strings.Cut(s.ID, "-")
		return first == prefix
	})
}

// Results is what Search found.
type Results struct {
	Systems []System
	// More is how many matches were left out beyond the limit.
	More int
	// Problems are sources that failed; the rest still answered.
	Problems []error
	// NoTransitland is true when Transitland was not searched for want of
	// a key.
	NoTransitland bool
}

// SearchLimit caps the systems Search returns, so a short search does not
// fill the screen.
const SearchLimit = 20

// Search finds systems whose agency, name or place contain every word of
// words. Catalog systems come first, then those whose agency or name is
// words exactly, then systems with realtime data, then schedule-only ones;
// directory entries for a catalog system are left out.
func (d *Directory) Search(ctx context.Context, words string) (Results, error) {
	if strings.TrimSpace(words) == "" {
		return Results{}, errors.New("give words to search for, such as an agency or a city")
	}
	var res Results
	cat, err := catalog.All()
	if err != nil {
		return res, err
	}
	known := map[string]bool{}
	var found []System
	for _, s := range cat {
		for _, id := range slices.Concat(s.MobilityDB, s.TransitlandFeeds) {
			known[id] = true
		}
		if u, ok := s.Options["static_url"].(string); ok {
			known[u] = true
		}
		if matches(strings.Join([]string{s.ID, s.Agency, s.Name, s.City, s.Country}, " "), words) {
			found = append(found, catalogSystem(s))
		}
	}

	feeds, err := d.mobilityDB(ctx)
	if err != nil {
		res.Problems = append(res.Problems, fmt.Errorf("mobility database: %w", err))
	}
	for _, s := range feeds.search(words) {
		if !known[s.ID] && !known[s.StaticURL] {
			found = append(found, s)
		}
	}

	if d.TransitlandKey == "" {
		res.NoTransitland = true
	} else {
		tl, err := d.searchTransitland(ctx, words)
		if err != nil {
			if ctx.Err() != nil {
				return res, ctx.Err()
			}
			res.Problems = append(res.Problems, fmt.Errorf("transitland: %w", err))
		}
		for _, s := range tl {
			if !known[s.ID] {
				found = append(found, s)
			}
		}
	}

	slices.SortStableFunc(found, func(a, b System) int {
		return cmp.Or(cmp.Compare(rank(a, words), rank(b, words)), cmp.Compare(sourceOrder(a.Source), sourceOrder(b.Source)))
	})
	if len(found) > SearchLimit {
		res.More = len(found) - SearchLimit
		found = found[:SearchLimit]
	}
	res.Systems = found
	if len(found) == 0 && len(res.Problems) > 0 {
		return res, errors.Join(res.Problems...)
	}
	return res, nil
}

// matches reports whether every word of search starts a word of text,
// ignoring case and accents, so "mta" finds "MTA" but not "SFMTA". Words
// split at anything but letters and digits; a search word with such a
// character, as "metro-north", matches across it. Unlike stop search
// (transit.MatchesSearch), a word must start a word: agency names are full
// of acronyms that contain one another.
func matches(text, search string) bool {
	words := strings.Fields(fold.String(search))
	if len(words) == 0 {
		return false
	}
	t := fold.String(text)
	for _, w := range words {
		if !startsWord(t, w) {
			return false
		}
	}
	return true
}

func startsWord(text, w string) bool {
	for i := 0; ; {
		j := strings.Index(text[i:], w)
		if j < 0 {
			return false
		}
		at := i + j
		if prev, _ := utf8.DecodeLastRuneInString(text[:at]); at == 0 || !isWordChar(prev) {
			return true
		}
		i = at + 1
	}
}

func isWordChar(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

func rank(s System, words string) int {
	if s.Source == SourceCatalog {
		return 0
	}
	r := 1
	if !named(s, words) {
		r += 2
	}
	if s.Data != DataRealtime {
		r++
	}
	return r
}

// named reports whether words is the system's agency or name exactly, with
// or without a part in parentheses, or that part or one of its words: an
// acronym such as "MTA" in "Metropolitan Transit Authority (MTA)", or
// "Muni" in "(SFMTA - Muni)". "mta" then ranks New York's MTA above
// Nashville MTA WeGo, which only contains it.
func named(s System, words string) bool {
	want := fold.Slug(words, ' ', 0)
	if want == "" {
		return false
	}
	for _, text := range []string{s.Agency, s.Name} {
		names := []string{text, parens.ReplaceAllString(text, "")}
		for _, m := range parens.FindAllStringSubmatch(text, -1) {
			names = append(names, m[1])
			names = append(names, strings.FieldsFunc(m[1], func(r rune) bool { return !isWordChar(r) })...)
		}
		for _, n := range names {
			if fold.Slug(n, ' ', 0) == want {
				return true
			}
		}
	}
	return false
}

var parens = regexp.MustCompile(`\(([^()]*)\)`)

func sourceOrder(s Source) int {
	return slices.Index([]Source{SourceCatalog, SourceMobilityDB, SourceTransitland}, s)
}

func catalogSystem(s catalog.System) System {
	sys := System{ID: s.ID, Agency: s.Agency, Name: s.Name, Place: s.City, Source: SourceCatalog, License: s.DataLicense, Data: DataSchedule}
	if s.Provider != "gtfsrt" || len(asSlice(s.Options["realtime_urls"])) > 0 {
		sys.Data = DataRealtime
	}
	for _, k := range s.RequiredOptions {
		sys.Needs = append(sys.Needs, s.EnvName(k))
	}
	return sys
}

func asSlice(v any) []any {
	s, _ := v.([]any)
	return s
}

// ErrUnknownID is returned, wrapped, for an ID no directory knows.
var ErrUnknownID = errors.New("unknown system")

// Resolve returns a directory system and its provider block. Transitland
// IDs need TransitlandKey.
func (d *Directory) Resolve(ctx context.Context, id string) (System, Block, error) {
	if strings.HasPrefix(id, "f-") {
		if d.TransitlandKey == "" {
			return System{}, Block{}, fmt.Errorf("%s is a Transitland feed; set %s to use it", id, TransitlandKeyVar)
		}
		return d.resolveTransitland(ctx, id)
	}
	feeds, err := d.mobilityDB(ctx)
	if err != nil {
		return System{}, Block{}, fmt.Errorf("mobility database: %w", err)
	}
	s, b, ok := feeds.resolve(id)
	if !ok {
		return System{}, Block{}, fmt.Errorf("%w %q in the Mobility Database", ErrUnknownID, id)
	}
	return s, b, nil
}

// ScheduleSize returns the size a HEAD request reports for a schedule URL,
// or false when the server does not say.
func (d *Directory) ScheduleSize(ctx context.Context, url string) (int64, bool) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return 0, false
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := d.client().Do(req)
	if err != nil {
		return 0, false
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.ContentLength <= 0 {
		return 0, false
	}
	return resp.ContentLength, true
}

// LargeSchedule is a schedule size above which a small machine, such as a
// NAS, may struggle to load it.
const LargeSchedule = 150 << 20

const userAgent = "urbanist-timetable"

// requestTimeout bounds each request; the Mobility Database's list is
// about 3 MB, which a slow link needs some time for.
const requestTimeout = 2 * time.Minute

// client is d.HTTP, or a default, made to drop the Transitland key when a
// redirect leaves the host first asked or downgrades to plain HTTP.
func (d *Directory) client() *http.Client {
	c := &http.Client{}
	if d.HTTP != nil {
		*c = *d.HTTP
	}
	next := c.CheckRedirect
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		first := via[0].URL
		if req.URL.Host != first.Host || first.Scheme == "https" && req.URL.Scheme != "https" {
			req.Header.Del("Apikey")
		}
		if next != nil {
			return next(req, via)
		}
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		return nil
	}
	return c
}

func (d *Directory) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

func (d *Directory) log() *slog.Logger {
	if d.Log != nil {
		return d.Log
	}
	return slog.New(slog.DiscardHandler)
}

// get fetches u with the extra headers, reading at most limit bytes, and
// returns the status and body.
func (d *Directory) get(ctx context.Context, u string, header http.Header, limit int64) (int, []byte, error) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	for k, v := range header {
		req.Header[k] = v
	}
	resp, err := d.client().Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return 0, nil, err
	}
	if int64(len(b)) > limit {
		return 0, nil, fmt.Errorf("response over %d MB", limit>>20)
	}
	return resp.StatusCode, b, nil
}
