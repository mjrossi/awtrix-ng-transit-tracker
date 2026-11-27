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

// scheduleFreshUntil is when a schedule loaded or revalidated at since is
// next checked with the server: daily, and also when its coverage ends.
// While it is outside its coverage it is checked hourly once the server has
// answered that it has not changed; otherwise each check is a full download.
func scheduleFreshUntil(st *static, since time.Time) time.Time {
	until := since.Add(staticTTL)
	if st == nil {
		return until
	}
	if st.coverage.outOfRange(since, st.loc) != nil {
		if st.revalidatable {
			until = since.Add(outOfRangeRecheck)
		}
		return until
	}
	if end := st.coverage.endsAt(st.loc); !end.IsZero() && end.Before(until) {
		until = end
	}
	return until
}

// scheduleExpires is scheduleFreshUntil for upstream.Cache. It counts from
// when the schedule was last checked rather than when this process loaded
// it, so a process starting from a file 23 hours old checks it an hour later.
func scheduleExpires(st *static, loadedAt time.Time) time.Time {
	if st != nil && !st.checked.IsZero() {
		return scheduleFreshUntil(st, st.checked)
	}
	return scheduleFreshUntil(st, loadedAt)
}

// scheduleFresh reports whether st, loaded or revalidated at since, can be
// used at now without checking with the server. A clock stepped back more
// than staticTTL makes it stale, as upstream.Cache judges.
func scheduleFresh(st *static, since, now time.Time) bool {
	return within(now, since, staticTTL) && now.Before(scheduleFreshUntil(st, since))
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
// the schedule st replaces unless st shares it. With timetable = false a timetable is kept only
// for a cached schedule, to name the lines at each stop for StopLines: that
// file is on disk anyway, while an uncached one is a temporary copy, often
// 100 MB, that a long-running service would hold for nothing.
func (p *Provider) adopt(st *static) {
	switch {
	case st.tt != nil:
		// A copy of the current schedule, from readCached, sharing its timetable.
	case st.file != nil && st.hasTimetable() && (p.timetableOn() || st.cached):
		dir := ""
		if st.cached {
			dir = p.cacheDir
		}
		st.tt = newTimetable(st, st.file, dir, p.cfg.StaticURL, p.clock, p.log)
	default:
		st.release()
		st.zr = nil
	}
	st.file = nil
	p.mu.Lock()
	old := p.current
	p.current = st
	p.mu.Unlock()
	if old != nil && old.tt != nil && old.tt != st.tt {
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
		took := p.clock().Sub(start)
		st, err := parseStatic(bytes.NewReader(b), int64(len(b)))
		if err != nil {
			return nil, err
		}
		p.logDownloaded(ctx, st, int64(len(b)), took)
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
	took := p.clock().Sub(start)
	st, err := parseStatic(f, size)
	if err != nil {
		_ = tf.Close()
		return nil, err
	}
	p.logDownloaded(ctx, st, size, took)
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

// logDownloaded reports a download, which took took, with the period the
// schedule covers.
func (p *Provider) logDownloaded(ctx context.Context, st *static, size int64, took time.Duration) {
	attrs := []any{
		slog.String("url", redact(p.cfg.StaticURL)),
		slog.Int64("bytes", size),
		slog.Duration("took", took.Round(time.Millisecond)),
	}
	if st.coverage.start != 0 {
		attrs = append(attrs, slog.String("from", st.coverage.start.iso()))
	}
	if st.coverage.end != 0 {
		attrs = append(attrs, slog.String("until", st.coverage.end.iso()))
	}
	if st.coverage.version != "" {
		attrs = append(attrs, slog.String("version", st.coverage.version))
	}
	p.log.InfoContext(ctx, "downloaded GTFS schedule", attrs...)
}
