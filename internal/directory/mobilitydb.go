package directory

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/mjrossi/urbanist-timetable/internal/fold"
	"github.com/mjrossi/urbanist-timetable/pkg/transit/catalog"
)

// DefaultMobilityDBURL is the Mobility Database's list of every feed,
// public and CC0. It changes a few times a month.
const DefaultMobilityDBURL = "https://files.mobilitydatabase.org/feeds_v2.csv"

const (
	mdbTTL   = 7 * 24 * time.Hour
	mdbFile  = "mobilitydb-feeds.csv"
	mdbLimit = 64 << 20
)

// Authentication types in the list's urls.authentication_type.
const (
	authNone   = 0
	authParam  = 1
	authHeader = 2
)

type mdbFeed struct {
	url   string
	auth  int
	info  string
	param string
}

type mdbSystem struct {
	sys      System
	static   mdbFeed
	realtime []mdbFeed
	// keys maps each feed's key information to the variable for its key.
	keys map[string]string
}

type mdbFeeds struct {
	systems   map[string]*mdbSystem
	order     []string
	redirects map[string]string
	// haystacks holds each system's searchable text.
	haystacks map[string]string
}

// mobilityDB returns the parsed list, from the cache directory while it is
// less than a week old, and the old copy when a refresh fails.
func (d *Directory) mobilityDB(ctx context.Context) (*mdbFeeds, error) {
	u := d.MobilityDBURL
	if u == "" {
		u = DefaultMobilityDBURL
	}
	if d.CacheDir == "" {
		b, _, err := d.fetchMDB(ctx, u, time.Time{})
		if err != nil {
			return &mdbFeeds{}, err
		}
		return parseMDB(b)
	}
	// The file's time is when the server last confirmed this copy, so it is
	// also the right If-Modified-Since. It changes only on a 200 or a 304:
	// touching it after a failed refresh would claim a check that never
	// happened, and later updates would be answered 304.
	path := filepath.Join(d.CacheDir, mdbFile)
	fi, statErr := os.Stat(path)
	var saved *mdbFeeds
	if statErr == nil {
		if b, err := os.ReadFile(path); err == nil {
			if f, err := parseMDB(b); err == nil {
				saved = f
			}
		}
	}
	if saved != nil && within(d.now(), fi.ModTime(), mdbTTL) {
		return saved, nil
	}
	var since time.Time
	if saved != nil {
		since = fi.ModTime()
	}
	b, notModified, err := d.fetchMDB(ctx, u, since)
	switch {
	case notModified && saved != nil:
		now := d.now()
		_ = os.Chtimes(path, now, now)
		return saved, nil
	case err != nil && saved != nil:
		d.log().WarnContext(ctx, "using the saved Mobility Database list", "err", err)
		return saved, nil
	case err != nil:
		return &mdbFeeds{}, err
	case notModified:
		return &mdbFeeds{}, errors.New("feed list: 304 without a saved copy")
	}
	f, err := parseMDB(b)
	if err != nil {
		return f, err
	}
	saveFile(d.CacheDir, path, b, d.now())
	return f, nil
}

// fetchMDB downloads the list, conditionally when since is set.
func (d *Directory) fetchMDB(ctx context.Context, u string, since time.Time) (b []byte, notModified bool, err error) {
	h := http.Header{}
	if !since.IsZero() {
		h.Set("If-Modified-Since", since.UTC().Format(http.TimeFormat))
	}
	d.log().InfoContext(ctx, "downloading the Mobility Database feed list")
	status, body, err := d.get(ctx, u, h, mdbLimit)
	if err != nil {
		return nil, false, err
	}
	switch status {
	case http.StatusOK:
		return body, false, nil
	case http.StatusNotModified:
		return nil, true, nil
	default:
		return nil, false, fmt.Errorf("GET %s: %d %s", u, status, http.StatusText(status))
	}
}

// saveFile writes b to path through a temporary file, setting its time to
// now. Failing to save only costs a download next time.
func saveFile(dir, path string, b []byte, now time.Time) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	tmp, err := os.CreateTemp(dir, ".download-*")
	if err != nil {
		return
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return
	}
	if tmp.Close() != nil || os.Chtimes(tmp.Name(), now, now) != nil {
		return
	}
	_ = os.Rename(tmp.Name(), path)
}

// ClearCache deletes the saved Mobility Database list and Transitland
// lookups from dir, returning the bytes freed.
func ClearCache(dir string) (int64, error) {
	paths, _ := filepath.Glob(filepath.Join(dir, resolvedPrefix+"*.json"))
	var (
		freed int64
		errs  []error
	)
	for _, path := range append([]string{filepath.Join(dir, mdbFile)}, paths...) {
		fi, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) || err == nil && !fi.Mode().IsRegular() {
			continue
		}
		if err == nil {
			err = os.Remove(path)
		}
		if err != nil {
			errs = append(errs, err)
			continue
		}
		freed += fi.Size()
	}
	return freed, errors.Join(errs...)
}

func within(now, since time.Time, d time.Duration) bool {
	age := now.Sub(since)
	return age > -d && age < d
}

func parseMDB(b []byte) (*mdbFeeds, error) {
	cr := csv.NewReader(bytes.NewReader(b))
	cr.FieldsPerRecord = -1
	head, err := cr.Read()
	if err != nil {
		return &mdbFeeds{}, fmt.Errorf("feed list header: %w", err)
	}
	col := map[string]int{}
	for i, h := range head {
		col[strings.TrimPrefix(h, "\uFEFF")] = i
	}
	for _, c := range []string{"id", "data_type", "provider", "urls.direct_download", "status"} {
		if _, ok := col[c]; !ok {
			return &mdbFeeds{}, fmt.Errorf("feed list has no %s column", c)
		}
	}
	f := &mdbFeeds{systems: map[string]*mdbSystem{}, redirects: map[string]string{}, haystacks: map[string]string{}}
	type rt struct {
		refs []string
		feed mdbFeed
	}
	var realtime []rt
	for {
		rec, err := cr.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return &mdbFeeds{}, fmt.Errorf("feed list: %w", err)
		}
		get := func(c string) string {
			i, ok := col[c]
			if !ok || i >= len(rec) {
				return ""
			}
			return strings.TrimSpace(rec[i])
		}
		id, status := get("id"), get("status")
		if to := get("redirect.id"); to != "" {
			f.redirects[id] = strings.Split(to, "|")[0]
		}
		if status != "active" && status != "" {
			continue
		}
		feed := mdbFeed{
			url:   firstNonEmpty(get("urls.direct_download"), get("urls.latest")),
			auth:  atoi(get("urls.authentication_type")),
			info:  get("urls.authentication_info"),
			param: get("urls.api_key_parameter_name"),
		}
		if feed.url == "" || hasEnvRef(feed.url, feed.param) {
			continue
		}
		switch get("data_type") {
		case "gtfs":
			place := joinNonEmpty(", ", get("location.municipality"), get("location.subdivision_name"), get("location.country_code"))
			s := &mdbSystem{
				sys: System{
					ID: id, Agency: get("provider"), Name: get("name"), Place: place, Data: DataSchedule,
					Source: SourceMobilityDB, License: get("urls.license"), StaticURL: feed.url,
				},
				static: feed,
			}
			f.systems[id] = s
			f.order = append(f.order, id)
			f.haystacks[id] = strings.Join([]string{id, s.sys.Agency, s.sys.Name, place}, " ")
		case "gtfs_rt":
			if slices.Contains(strings.Split(get("entity_type"), "|"), "tu") {
				realtime = append(realtime, rt{refs: strings.Split(get("static_reference"), "|"), feed: feed})
			}
		}
	}
	for _, r := range realtime {
		for _, ref := range r.refs {
			if s, ok := f.systems[ref]; ok {
				s.realtime = append(s.realtime, r.feed)
				s.sys.Data = DataRealtime
			}
		}
	}
	for _, s := range f.systems {
		s.finish()
	}
	return f, nil
}

// finish keeps only the feeds a block can use safely and works out the
// environment variables for their keys.
func (s *mdbSystem) finish() {
	s.keys = map[string]string{}
	var infos []string
	for _, feed := range append([]mdbFeed{s.static}, s.realtime...) {
		if feed.auth == authNone {
			continue
		}
		// Feeds with different sign-up pages take keys from different
		// issuers, so each gets its own variable.
		if _, ok := s.keys[feed.info]; ok {
			continue
		}
		v := keyVar(s.sys.Agency, s.sys.ID)
		if n := len(s.keys); n > 0 {
			v = strings.TrimSuffix(v, "_API_KEY") + fmt.Sprintf("_%d_API_KEY", n+1)
		}
		s.keys[feed.info] = v
		s.sys.Needs = append(s.sys.Needs, v)
		if feed.info != "" {
			infos = append(infos, feed.info)
		}
	}
	s.sys.KeyInfo = strings.Join(infos, " and ")
}

// headerHosts lists the hosts of a system's feeds when a key goes in a
// header and they are not all one host: gtfsrt sends its headers to every
// feed, so the key reaches each of them.
func (s *mdbSystem) headerHosts() []string {
	feeds := append([]mdbFeed{s.static}, s.realtime...)
	if !slices.ContainsFunc(feeds, func(f mdbFeed) bool { return f.auth == authHeader }) {
		return nil
	}
	var hosts []string
	for _, f := range feeds {
		if h := hostOf(f.url); !slices.Contains(hosts, h) {
			hosts = append(hosts, h)
		}
	}
	if len(hosts) < 2 {
		return nil
	}
	return hosts
}

func hostOf(u string) string {
	p, err := url.Parse(u)
	if err != nil {
		return ""
	}
	return strings.ToLower(p.Host)
}

// hasEnvRef reports whether directory data holds a ${NAME} reference,
// which would read the environment once the block is expanded.
func hasEnvRef(ss ...string) bool {
	return slices.ContainsFunc(ss, func(s string) bool { return strings.Contains(s, "${") })
}

func (f *mdbFeeds) search(words string) []System {
	if f == nil {
		return nil
	}
	var out []System
	for _, id := range f.order {
		if matches(f.haystacks[id], words) {
			out = append(out, f.systems[id].sys)
		}
	}
	return out
}

// resolve finds a system by ID, following the list's redirects from
// replaced feeds.
func (f *mdbFeeds) resolve(id string) (System, Block, bool) {
	if f == nil {
		return System{}, Block{}, false
	}
	for range 5 {
		if s, ok := f.systems[id]; ok {
			return s.sys, s.block(), true
		}
		to, ok := f.redirects[id]
		if !ok {
			break
		}
		id = to
	}
	return System{}, Block{}, false
}

func (s *mdbSystem) block() Block {
	opts := map[string]any{}
	headers := map[string]any{}
	withKey := func(feed mdbFeed) string {
		key := "${" + s.keys[feed.info] + "}"
		switch {
		case feed.auth == authParam && feed.param != "":
			sep := "?"
			if strings.Contains(feed.url, "?") {
				sep = "&"
			}
			return feed.url + sep + url.QueryEscape(feed.param) + "=" + key
		case feed.auth == authHeader && feed.param != "":
			headers[feed.param] = key
		}
		return feed.url
	}
	opts["static_url"] = withKey(s.static)
	if len(s.realtime) > 0 {
		urls := make([]any, len(s.realtime))
		for i, feed := range s.realtime {
			urls[i] = withKey(feed)
		}
		opts["realtime_urls"] = urls
	}
	if len(headers) > 0 {
		opts["headers"] = headers
	}
	comment := fmt.Sprintf("From the Mobility Database (%s).", s.sys.ID)
	if hosts := s.headerHosts(); hosts != nil {
		comment += " The key header is sent to every feed: " + strings.Join(hosts, ", ") + "."
	}
	if s.sys.License != "" {
		comment += " Data license: " + s.sys.License
	}
	return Block{Type: "gtfsrt", Options: opts, Comment: comment}
}

var acronym = regexp.MustCompile(`\(([A-Za-z][A-Za-z0-9 .\-]{1,11})\)`)

// agencyShort is an agency's acronym in parentheses, as the list often
// gives one, or its first two words. Key variables and block names are
// made from it, so a block and its key read alike.
func agencyShort(agency string) string {
	if m := acronym.FindStringSubmatch(agency); m != nil {
		return m[1]
	}
	words := strings.Fields(agency)
	return strings.Join(words[:min(len(words), 2)], " ")
}

// BlockName names the system's [providers] block after its agency, as its
// key variable is, or after its ID when the agency's name has no Latin
// letters or digits.
func (s System) BlockName() string {
	var b strings.Builder
	for _, r := range fold.String(agencyShort(s.Agency)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case b.Len() > 0 && !strings.HasSuffix(b.String(), "-"):
			b.WriteByte('-')
		}
	}
	name := strings.Trim(b.String(), "-")
	if len(name) > 32 {
		name = strings.TrimRight(name[:32], "-")
	}
	if name == "" {
		return s.ID
	}
	return name
}

// keyVar names the environment variable for an agency's key, from
// agencyShort. Names this tool or its catalog already use get the system's
// ID instead, so a directory entry cannot be handed another service's key.
func keyVar(agency, id string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(fold.String(agencyShort(agency))) {
		switch {
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case b.Len() > 0 && !strings.HasSuffix(b.String(), "_"):
			b.WriteByte('_')
		}
	}
	v := strings.Trim(b.String(), "_") + "_API_KEY"
	if v == "_API_KEY" || reservedVar(v) {
		v = strings.ToUpper(strings.ReplaceAll(id, "-", "_")) + "_API_KEY"
	}
	return v
}

// reservedVar reports whether v is a variable this tool reads for itself
// or for a catalog system.
func reservedVar(v string) bool {
	if v == TransitlandKeyVar || strings.HasPrefix(v, "TIMETABLE_") || strings.HasPrefix(v, "AWTRIX_") {
		return true
	}
	all, _ := catalog.All()
	for _, s := range all {
		for _, o := range s.RequiredOptions {
			if s.EnvName(o) == v {
				return true
			}
		}
	}
	return false
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

func joinNonEmpty(sep string, ss ...string) string {
	return strings.Join(slices.DeleteFunc(ss, func(s string) bool { return s == "" }), sep)
}

func atoi(s string) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
	}
	return n
}
