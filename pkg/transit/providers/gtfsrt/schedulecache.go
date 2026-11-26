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
// parse, so a replacement renamed into place meanwhile does not affect it.
type cacheFile struct {
	f        *os.File
	valid    validators
	zipStart int64
	size     int64
	fetched  time.Time
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
	cf := &cacheFile{f: f, zipStart: int64(len(line)), size: fi.Size(), fetched: fi.ModTime()}
	if err := json.Unmarshal(line, &cf.valid); err != nil {
		return nil, fmt.Errorf("cached schedule header: %w", err)
	}
	return cf, nil
}

func (cf *cacheFile) parse() (*static, error) {
	return parseStatic(io.NewSectionReader(cf.f, cf.zipStart, cf.size-cf.zipStart), cf.size-cf.zipStart)
}

// Close closes the file.
func (cf *cacheFile) Close() error { return cf.f.Close() }

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

// ClearCache deletes the schedules cached in dir by WithCacheDir, and
// partial downloads no process is still writing. It returns how many
// schedules it deleted and how many bytes it freed. A process using a
// deleted schedule keeps its copy in memory and downloads a new one at its
// next refresh. Other files in dir are left alone.
func ClearCache(dir string, now time.Time) (schedules int, freed int64, err error) {
	freed = removeAbandonedDownloads(dir, now)
	paths, err := filepath.Glob(filepath.Join(dir, "*.gtfs"))
	if err != nil {
		return 0, freed, err
	}
	var errs []error
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
func (p *Provider) loadCachedStatic(ctx context.Context, now time.Time) (*static, error) {
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
	if old != nil && within(now, old.fetched, staticTTL) {
		if st, err := old.parse(); err == nil {
			return st, nil
		}
		_ = os.Remove(path)
		old = nil
	}
	st, err := p.refreshCache(ctx, now, path, old)
	switch {
	case err == nil:
		return st, nil
	case errors.Is(err, errCacheUnusable):
		return p.downloadStatic(ctx)
	case old != nil && ctx.Err() == nil:
		if st, perr := old.parse(); perr == nil {
			return st, &upstream.StaleError{Err: err}
		}
	}
	return nil, err
}

var errCacheUnusable = errors.New("schedule cache unusable")

// refreshCache downloads the schedule into path, or renews old's time when
// the server answers that it has not changed.
func (p *Provider) refreshCache(ctx context.Context, now time.Time, path string, old *cacheFile) (*static, error) {
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
	defer func() {
		_ = tmp.Close()
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
		if err := os.Chtimes(path, now, now); err != nil {
			return nil, err
		}
		return old.parse()
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	fresh, err := readCacheHeader(tmp)
	if err != nil {
		return nil, err
	}
	st, err := fresh.parse()
	if err != nil {
		return nil, err
	}
	p.logDownloaded(ctx, size, start)
	if err := os.Chtimes(tmp.Name(), now, now); err != nil {
		return nil, err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return nil, err
	}
	return st, nil
}

// within reports whether since is less than d away from now, either way, as
// upstream.Cache judges freshness: a clock stepped back cannot keep a file
// fresh indefinitely.
func within(now, since time.Time, d time.Duration) bool {
	age := now.Sub(since)
	return age > -d && age < d
}
