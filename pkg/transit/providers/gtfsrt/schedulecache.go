package gtfsrt

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mjrossi/urbanist-timetable/pkg/transit/internal/upstream"
)

// Option configures a Provider.
type Option func(*Provider)

// WithCacheDir keeps the schedule zip in dir, so a new process reuses it for
// a day and then revalidates it instead of downloading it again. Empty turns
// the cache off.
func WithCacheDir(dir string) Option {
	return func(p *Provider) { p.cacheDir = dir }
}

// WithLogger reports schedule downloads to log, since a large one can take
// minutes. Without it nothing is logged.
func WithLogger(log *slog.Logger) Option {
	return func(p *Provider) { p.log = log }
}

// validators let a stale schedule be revalidated with a conditional request.
type validators struct {
	ETag         string `json:"etag,omitempty"`
	LastModified string `json:"last_modified,omitempty"`
}

// cacheFile is one cached schedule: a JSON line of validators, then the zip.
// It is a single file so that processes replacing it concurrently cannot
// pair one's validators with another's zip. Its modification time is when
// it was last downloaded or revalidated. The file stays open from header to
// parse, and then for as long as the schedule's timetable reads stop times
// from it, so a replacement renamed into place meanwhile does not affect it.
type cacheFile struct {
	f        *os.File
	valid    validators
	zipStart int64
	size     int64
	fetched  time.Time
	info     os.FileInfo
}

// cacheName names the file by a hash of the URL, which can hold a key.
func cacheName(url string) string {
	sum := sha256.Sum256([]byte(url))
	return hex.EncodeToString(sum[:]) + ".gtfs"
}

func openCacheFile(path string) (*cacheFile, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	cf, err := readCacheHeader(f)
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return cf, nil
}

func readCacheHeader(f *os.File) (*cacheFile, error) {
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	line, err := bufio.NewReader(io.LimitReader(f, 64<<10)).ReadBytes('\n')
	if err != nil {
		return nil, fmt.Errorf("cached schedule header: %w", err)
	}
	cf := &cacheFile{f: f, zipStart: int64(len(line)), size: fi.Size(), fetched: fi.ModTime(), info: fi}
	if err := json.Unmarshal(line, &cf.valid); err != nil {
		return nil, fmt.Errorf("cached schedule header: %w", err)
	}
	return cf, nil
}

// zip is the schedule zip after the header line.
func (cf *cacheFile) zip() *io.SectionReader {
	return io.NewSectionReader(cf.f, cf.zipStart, cf.size-cf.zipStart)
}

// parse reads the schedule. On success the schedule holds the file, which
// Close then leaves open.
func (cf *cacheFile) parse() (*static, error) {
	zr := cf.zip()
	st, err := parseStatic(zr, zr.Size())
	if err != nil {
		return nil, err
	}
	st.file, st.cached, st.src = cf.f, true, cf.info
	st.checked = cf.fetched
	cf.f = nil
	return st, nil
}

// readCached returns cf's schedule. When the current schedule was read from
// the same file, it returns a copy of it sharing its timetable rather than
// parsing the whole zip again, as an hourly check of a schedule out of its
// coverage would.
func (p *Provider) readCached(cf *cacheFile) (*static, error) {
	if cur := p.currentStatic(); cur != nil && cur.src != nil && os.SameFile(cur.src, cf.info) {
		c := *cur
		if cf.fetched.After(c.checked) {
			c.checked = cf.fetched
		}
		return &c, nil
	}
	return cf.parse()
}

// Close closes the file unless a parsed schedule holds it.
func (cf *cacheFile) Close() error {
	if cf.f == nil {
		return nil
	}
	return cf.f.Close()
}

// removeAbandonedDownloads deletes partial downloads that no process can
// still be writing, such as ones cut off when a container stopped, and
// returns how many bytes it freed.
func removeAbandonedDownloads(dir string, now time.Time) int64 {
	var freed int64
	paths, _ := filepath.Glob(filepath.Join(dir, ".download-*"))
	for _, p := range paths {
		if fi, err := os.Stat(p); err == nil && now.Sub(fi.ModTime()) > staticTimeout {
			if os.Remove(p) == nil {
				freed += fi.Size()
			}
		}
	}
	return freed
}

// pruneEndedSchedules deletes cached schedules other than keep whose
// coverage ended at least a day ago and that no process has used for
// unusedGrace, with their stop indexes. A process using a schedule renews
// its time when it checks it with the server, normally daily, so one that a
// configuration still names is kept unless the server has been unreachable,
// or the time could not be renewed, for that long. Such a process keeps its
// copy in memory and downloads the schedule again at its next check.
func pruneEndedSchedules(dir, keep string, now time.Time) {
	paths, _ := filepath.Glob(filepath.Join(dir, "*.gtfs"))
	for _, path := range paths {
		if path == keep {
			continue
		}
		fi, err := os.Lstat(path)
		if err != nil || !fi.Mode().IsRegular() || within(now, fi.ModTime(), unusedGrace) || !cachedScheduleEnded(path, now) {
			continue
		}
		// A download renamed into place meanwhile is a different file.
		if again, err := os.Lstat(path); err != nil || !os.SameFile(fi, again) || !again.ModTime().Equal(fi.ModTime()) {
			continue
		}
		if os.Remove(path) == nil {
			removeIndexes(dir, strings.TrimSuffix(filepath.Base(path), ".gtfs"))
		}
	}
}

// cachedScheduleEnded reports whether the schedule cached at path covers
// nothing after the day before now. The day of margin stands in for the
// agency's time zone, which is not read.
func cachedScheduleEnded(path string, now time.Time) bool {
	cf, err := openCacheFile(path)
	if err != nil {
		return false
	}
	defer cf.Close()
	zr := cf.zip()
	c, err := readCoverageOnly(zr, zr.Size())
	return err == nil && c.end != 0 && c.end.next() < dateOf(now.UTC())
}

// ClearCache deletes the schedules cached in dir by WithCacheDir, the stop
// indexes saved with them, and partial downloads no process is still
// writing. It returns how many schedules it deleted and how many bytes it
// freed. A process using a deleted schedule keeps its copy in memory and
// downloads a new one at its next refresh. Other files in dir are left
// alone.
func ClearCache(dir string, now time.Time) (schedules int, freed int64, err error) {
	freed = removeAbandonedDownloads(dir, now)
	paths, err := filepath.Glob(filepath.Join(dir, "*.gtfs"))
	if err != nil {
		return 0, freed, err
	}
	indexFreed, indexErr := clearIndexes(dir)
	freed += indexFreed
	errs := []error{indexErr}
	for _, p := range paths {
		fi, err := os.Lstat(p)
		if err != nil || !fi.Mode().IsRegular() {
			continue
		}
		if err := os.Remove(p); err != nil {
			errs = append(errs, err)
			continue
		}
		schedules++
		freed += fi.Size()
	}
	return schedules, freed, errors.Join(errs...)
}

// loadCachedStatic serves the schedule from the cache directory, falling
// back to downloadStatic when the directory cannot be used.
func (p *Provider) loadCachedStatic(ctx context.Context, now time.Time) (st *static, err error) {
	if err := os.MkdirAll(p.cacheDir, 0o700); err != nil {
		return p.downloadStatic(ctx)
	}
	removeAbandonedDownloads(p.cacheDir, now)
	path := filepath.Join(p.cacheDir, cacheName(p.cfg.StaticURL))
	old, err := openCacheFile(path)
	if err != nil {
		old = nil
	} else {
		defer old.Close()
	}
	// parsed is old's schedule, read to judge whether it is still fresh; a
	// refresh that finds it unchanged returns it rather than reading it again.
	// Unless returned, it is released.
	var parsed *static
	defer func() {
		if parsed != nil && parsed != st {
			parsed.release()
		}
	}()
	if old != nil && within(now, old.fetched, staticTTL) {
		cached, err := p.readCached(old)
		switch {
		case err != nil:
			_ = os.Remove(path)
			old = nil
		case scheduleFresh(cached, old.fetched, now):
			return cached, nil
		default:
			parsed = cached
		}
	}
	// Pruning reads the calendar of each other old schedule, so it runs with
	// the check with the server, about daily, not on every load.
	pruneEndedSchedules(p.cacheDir, path, now)
	st, err = p.refreshCache(ctx, now, path, old, parsed)
	switch {
	case err == nil:
		return st, nil
	case errors.Is(err, errCacheUnusable):
		return p.downloadStatic(ctx)
	case old != nil && ctx.Err() == nil:
		if parsed != nil {
			return parsed, &upstream.StaleError{Err: err}
		}
		if cached, perr := p.readCached(old); perr == nil {
			return cached, &upstream.StaleError{Err: err}
		}
	}
	return nil, err
}

var errCacheUnusable = errors.New("schedule cache unusable")

// chtimes is os.Chtimes, replaced by tests.
var chtimes = os.Chtimes

// refreshCache downloads the schedule into path, or renews old's time when
// the server answers that it has not changed. parsed, if not nil, is old's
// schedule already read, which an unchanged answer returns.
func (p *Provider) refreshCache(ctx context.Context, now time.Time, path string, old *cacheFile, parsed *static) (*static, error) {
	cond := http.Header{}
	if old != nil {
		if old.valid.ETag != "" {
			cond.Set("If-None-Match", old.valid.ETag)
		}
		if old.valid.LastModified != "" {
			cond.Set("If-Modified-Since", old.valid.LastModified)
		}
	}
	tmp, err := os.CreateTemp(p.cacheDir, ".download-*")
	if err != nil {
		return nil, errCacheUnusable
	}
	var fresh *cacheFile
	defer func() {
		if fresh != nil {
			_ = fresh.Close()
		} else {
			_ = tmp.Close()
		}
		_ = os.Remove(tmp.Name())
	}()

	if old == nil {
		p.logDownloadStart(ctx)
	} else {
		p.log.DebugContext(ctx, "revalidating GTFS schedule", slog.String("url", redact(p.cfg.StaticURL)))
	}
	start := p.clock()
	var valid validators
	notModified := false
	size, err := p.fetchTo(ctx, p.cfg.StaticURL, cond, maxStaticBody, func(status int, h http.Header) (io.Writer, error) {
		if status == http.StatusNotModified && old != nil {
			notModified = true
			return io.Discard, nil
		}
		valid = validators{ETag: h.Get("ETag"), LastModified: h.Get("Last-Modified")}
		line, err := json.Marshal(valid)
		if err != nil {
			return nil, err
		}
		if _, err := tmp.Write(append(line, '\n')); err != nil {
			return nil, err
		}
		return tmp, nil
	})
	if err != nil {
		return nil, err
	}
	if notModified {
		p.log.DebugContext(ctx, "GTFS schedule unchanged", slog.String("url", redact(p.cfg.StaticURL)))
		// The schedule is valid either way; a time left old only makes the
		// next process revalidate it sooner.
		if err := chtimes(path, now, now); err != nil {
			p.log.WarnContext(ctx, "could not renew the cached schedule's time", slog.Any("err", err))
		}
		st := parsed
		if st == nil {
			var err error
			if st, err = p.readCached(old); err != nil {
				return nil, err
			}
		}
		st.checked, st.revalidatable = now, true
		return st, nil
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	took := p.clock().Sub(start)
	fresh, err = readCacheHeader(tmp)
	if err != nil {
		return nil, err
	}
	st, err := fresh.parse()
	if err != nil {
		return nil, err
	}
	p.logDownloaded(ctx, st, size, took)
	if err := os.Chtimes(tmp.Name(), now, now); err != nil {
		st.release()
		return nil, err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		st.release()
		return nil, err
	}
	st.checked = now
	return st, nil
}

// within reports whether since is less than d away from now, either way, as
// upstream.Cache judges freshness: a clock stepped back cannot keep a file
// fresh indefinitely.
func within(now, since time.Time, d time.Duration) bool {
	age := now.Sub(since)
	return age > -d && age < d
}
