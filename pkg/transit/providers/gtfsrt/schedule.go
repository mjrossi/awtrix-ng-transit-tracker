package gtfsrt

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/mjrossi/urbanist-timetable/pkg/transit/internal/upstream"
)

// staticFor returns the schedule data and the error of the latest
// download if it failed, in which case the previous data is kept. While
// earlier data exists every caller gets it at once; with none yet, callers
// wait for the download.
func (p *Provider) staticFor(ctx context.Context, now time.Time) (*static, error) {
	if p.schedule == nil {
		return nil, nil
	}
	return p.schedule.Get(ctx, now)
}

func (p *Provider) loadStatic(ctx context.Context, now time.Time) (*static, error) {
	var (
		st  *static
		err error
	)
	if p.cacheDir != "" {
		st, err = p.loadCachedStatic(ctx, now)
	} else {
		st, err = p.downloadStatic(ctx)
	}
	if st != nil {
		p.adopt(st)
	}
	if err != nil {
		var stale *upstream.StaleError
		if errors.As(err, &stale) {
			return st, &upstream.StaleError{Err: fmt.Errorf("schedule: %w", stale.Err)}
		}
		return nil, fmt.Errorf("schedule: %w", err)
	}
	return st, nil
}

func (p *Provider) currentStatic() *static {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.current
}

// timetableOn reports whether the configuration wants timetabled trips.
func (p *Provider) timetableOn() bool { return p.cfg.Timetable == nil || *p.cfg.Timetable }

// adopt makes st the current schedule: it gives st a timetable that keeps
// the schedule's file open, or closes the file, and closes the timetable of
// the schedule st replaces. With timetable = false a timetable is kept only
// for a cached schedule, to name the lines at each stop for StopLines: that
// file is on disk anyway, while an uncached one is a temporary copy, often
// 100 MB, that a long-running service would hold for nothing.
func (p *Provider) adopt(st *static) {
	if st.file != nil && st.hasTimetable() && (p.timetableOn() || st.cached) {
		dir := ""
		if st.cached {
			dir = p.cacheDir
		}
		st.tt = newTimetable(st, st.file, dir, p.cfg.StaticURL, p.clock, p.log)
	} else {
		st.release()
		st.zr = nil
	}
	st.file = nil
	p.mu.Lock()
	old := p.current
	p.current = st
	p.mu.Unlock()
	if old != nil && old != st && old.tt != nil {
		old.tt.close()
	}
}

// downloadStatic fetches and parses the schedule zip. The zip goes to a
// temporary file rather than memory: large feeds are around 100 MB, and
// only a small part of them is kept. The file stays open for the timetable
// to read stop times from. Where no temporary file can be created, such as
// a read-only filesystem, it falls back to memory, without a timetable.
func (p *Provider) downloadStatic(ctx context.Context) (*static, error) {
	p.logDownloadStart(ctx)
	start := p.clock()
	f, err := os.CreateTemp("", "gtfsrt-static-*.zip")
	if err != nil {
		b, err := p.fetch(ctx, p.cfg.StaticURL, maxStaticBody)
		if err != nil {
			return nil, err
		}
		p.logDownloaded(ctx, int64(len(b)), start)
		st, err := parseStatic(bytes.NewReader(b), int64(len(b)))
		if err != nil {
			return nil, err
		}
		if p.timetableOn() && st.hasTimetable() {
			p.log.WarnContext(ctx, "no temporary file for the GTFS schedule, so only realtime departures are shown", slog.String("url", redact(p.cfg.StaticURL)))
		}
		// Keeping the zip to read stop times would keep all of it in memory.
		st.zr = nil
		return st, nil
	}
	tf := newTempFile(f)
	size, err := p.fetchTo(ctx, p.cfg.StaticURL, nil, maxStaticBody, to(f))
	if err != nil {
		_ = tf.Close()
		return nil, err
	}
	p.logDownloaded(ctx, size, start)
	st, err := parseStatic(f, size)
	if err != nil {
		_ = tf.Close()
		return nil, err
	}
	st.file = tf
	return st, nil
}

// tempFile is an open temporary file, deleted at once where the system
// allows deleting an open file, so it cannot outlive the process, and
// otherwise when closed.
type tempFile struct {
	*os.File
	removed bool
}

func newTempFile(f *os.File) *tempFile {
	return &tempFile{File: f, removed: os.Remove(f.Name()) == nil}
}

func (t *tempFile) Close() error {
	err := t.File.Close()
	if !t.removed {
		_ = os.Remove(t.Name())
	}
	return err
}

func (p *Provider) logDownloadStart(ctx context.Context) {
	p.log.InfoContext(ctx, "downloading GTFS schedule", slog.String("url", redact(p.cfg.StaticURL)))
}

func (p *Provider) logDownloaded(ctx context.Context, size int64, start time.Time) {
	p.log.InfoContext(ctx, "downloaded GTFS schedule",
		slog.String("url", redact(p.cfg.StaticURL)),
		slog.Int64("bytes", size),
		slog.Duration("took", p.clock().Sub(start).Round(time.Millisecond)))
}
