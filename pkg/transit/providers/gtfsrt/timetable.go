package gtfsrt

import (
	"archive/zip"
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// scanGather lets the first queries for several stops, such as every board
// starting at once, share one pass over stop_times.txt.
const scanGather = 100 * time.Millisecond

// indexVersion names the format of persisted stop indexes.
const indexVersion = "v1"

// stopTime is one boardable timetabled departure from a stop: not the trip's
// last stop, and not one with pickup_type 1.
type stopTime struct {
	trip string
	// dep is seconds from the start of the service day, or for a frequency
	// trip from its first departure.
	dep int32
	seq uint32
	// start is the trip's first departure, which with its route, direction
	// and day identifies it in feeds without trip IDs. Zero for frequency
	// trips, whose start is each headway's.
	start int32
	// last is the stop_id of the trip's last stop, for trips without a
	// headsign, or empty when unknown.
	last string
}

// timetable reads stop times from the schedule zip for the stops that are
// asked for, and keeps them. With a directory, each stop's times are also
// saved there, since a new process would otherwise read the whole file
// again.
type timetable struct {
	st    *static
	clock func() time.Time
	log   *slog.Logger
	// dir and prefix name saved indexes; dir is empty when none are saved.
	dir, prefix string

	// fileMu is held for reading during a scan, and for writing by close, so
	// the zip is not closed under a scan.
	fileMu sync.RWMutex
	closer io.Closer

	mu       sync.Mutex
	closed   bool
	stops    map[string][]stopTime
	next     *scan
	running  bool
	failed   error
	failedAt time.Time
}

type scan struct {
	stops map[string]bool
	done  chan struct{}
	err   error
}

var errTimetableClosed = errors.New("timetable replaced by a newer schedule")

// newTimetable reads stop times from st.zr, closing closer when the
// timetable is closed. dir, if not empty, is where stop indexes are saved;
// url names the schedule there.
func newTimetable(st *static, closer io.Closer, dir, url string, clock func() time.Time, log *slog.Logger) *timetable {
	tt := &timetable{st: st, clock: clock, log: log, closer: closer, stops: map[string][]stopTime{}}
	if dir != "" {
		if v, ok := st.version(); ok {
			base := strings.TrimSuffix(cacheName(url), ".gtfs")
			tt.dir, tt.prefix = dir, base+"."+v+"."
			removeOtherIndexes(dir, base, tt.prefix)
		}
	}
	return tt
}

// version identifies the schedule's stop times and trips, so a saved index
// is used only with the schedule it was made from.
func (s *static) version() (string, bool) {
	var parts []string
	for _, name := range []string{"stop_times.txt", "trips.txt"} {
		i := slices.IndexFunc(s.zr.File, func(f *zip.File) bool { return f.Name == name })
		if i < 0 {
			return "", false
		}
		f := s.zr.File[i]
		parts = append(parts, fmt.Sprintf("%08x%x", f.CRC32, f.UncompressedSize64))
	}
	return strings.Join(parts, "-"), true
}

// removeOtherIndexes deletes the indexes saved for earlier versions of the
// schedule base names.
func removeOtherIndexes(dir, base, keep string) {
	paths, _ := filepath.Glob(filepath.Join(dir, base+".*.stops"))
	for _, p := range paths {
		if !strings.HasPrefix(filepath.Base(p), keep) {
			_ = os.Remove(p)
		}
	}
}

// close releases the schedule zip once no scan is using it. Stops already
// read stay available.
func (tt *timetable) close() {
	tt.mu.Lock()
	tt.closed = true
	tt.mu.Unlock()
	tt.fileMu.Lock()
	defer tt.fileMu.Unlock()
	if tt.closer != nil {
		_ = tt.closer.Close()
		tt.closer = nil
	}
}

// stopTimes returns the timetabled departures from stop and its platforms.
func (tt *timetable) stopTimes(ctx context.Context, stop string) ([]stopTime, error) {
	tt.mu.Lock()
	if sts, ok := tt.stops[stop]; ok {
		tt.mu.Unlock()
		return sts, nil
	}
	if sts, ok := tt.loadIndex(stop); ok {
		tt.stops[stop] = sts
		tt.mu.Unlock()
		return sts, nil
	}
	switch {
	case tt.closed:
		tt.mu.Unlock()
		return nil, errTimetableClosed
	case tt.failed != nil && within(tt.clock(), tt.failedAt, staticRetry):
		err := tt.failed
		tt.mu.Unlock()
		return nil, err
	}
	if tt.next == nil {
		tt.next = &scan{stops: map[string]bool{}, done: make(chan struct{})}
		if !tt.running {
			tt.running = true
			go tt.run()
		}
	}
	s := tt.next
	s.stops[stop] = true
	tt.mu.Unlock()

	select {
	case <-s.done:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if s.err != nil {
		return nil, s.err
	}
	tt.mu.Lock()
	defer tt.mu.Unlock()
	return tt.stops[stop], nil
}

// run performs the scans queued by stopTimes until none is waiting. A scan
// does not stop when the query that started it is canceled, since others
// may be waiting for it.
func (tt *timetable) run() {
	for {
		time.Sleep(scanGather)
		tt.mu.Lock()
		s := tt.next
		tt.next = nil
		if s == nil {
			tt.running = false
			tt.mu.Unlock()
			return
		}
		tt.mu.Unlock()

		found, err := tt.scan(s.stops)

		tt.mu.Lock()
		if err != nil {
			tt.failed, tt.failedAt = err, tt.clock()
		} else {
			tt.failed = nil
			for stop := range s.stops {
				sts := found[stop]
				if sts == nil {
					sts = []stopTime{}
				}
				tt.stops[stop] = sts
				tt.saveIndex(stop, sts)
			}
		}
		s.err = err
		close(s.done)
		tt.mu.Unlock()
	}
}

// scan reads stop_times.txt once for every stop in want.
func (tt *timetable) scan(want map[string]bool) (map[string][]stopTime, error) {
	tt.fileMu.RLock()
	defer tt.fileMu.RUnlock()
	if tt.closer == nil && tt.closed {
		return nil, errTimetableClosed
	}
	start := tt.clock()
	st := tt.st

	type candidate struct {
		stop string
		stopTime
		pickup string
	}
	var (
		found []candidate
		// Per trip, by tripInfo.idx: its highest and lowest stop_sequence,
		// and the departure at the lowest.
		maxSeq   = make([]int64, len(st.trips))
		minSeq   = make([]int64, len(st.trips))
		firstDep = make([]int32, len(st.trips))
		// lastStop is the stop at maxSeq, kept only for trips that stop at
		// a wanted stop.
		lastStop = map[int32]string{}
		// Stop times are usually grouped by trip, so the trip lookup is
		// skipped while the trip ID repeats.
		prevTrip string
		prevInfo tripInfo
		prevOK   bool
	)
	for i := range maxSeq {
		maxSeq[i], minSeq[i] = -1, -1
	}
	err := readTable(st.zr, "stop_times.txt", func(r row) {
		tripID := r.get("trip_id")
		if tripID != prevTrip || prevTrip == "" {
			prevTrip = strings.Clone(tripID)
			prevInfo, prevOK = st.trips[tripID]
		}
		if !prevOK {
			return
		}
		info := prevInfo
		seq, err := strconv.ParseUint(r.get("stop_sequence"), 10, 32)
		if err != nil {
			return
		}
		stopID := r.get("stop_id")
		if int64(seq) > maxSeq[info.idx] {
			maxSeq[info.idx] = int64(seq)
			if _, ok := lastStop[info.idx]; ok {
				lastStop[info.idx] = strings.Clone(stopID)
			}
		}
		dep, ok := parseClock(r.get("departure_time"))
		if !ok {
			dep, ok = parseClock(r.get("arrival_time"))
		}
		if ok && (minSeq[info.idx] < 0 || int64(seq) < minSeq[info.idx]) {
			minSeq[info.idx], firstDep[info.idx] = int64(seq), dep
		}
		parent := st.stops[stopID].parent
		for _, target := range [2]string{stopID, parent} {
			if target == "" || !want[target] {
				continue
			}
			if !ok {
				// A stop without times (not a timepoint) is left out
				// rather than guessed between its neighbors.
				continue
			}
			if _, ok := lastStop[info.idx]; !ok {
				lastStop[info.idx] = ""
				if int64(seq) == maxSeq[info.idx] {
					lastStop[info.idx] = strings.Clone(stopID)
				}
			}
			found = append(found, candidate{
				stop:     target,
				stopTime: stopTime{trip: prevTrip, dep: dep, seq: uint32(seq)},
				pickup:   strings.Clone(r.get("pickup_type")),
			})
		}
	})
	if err != nil {
		return nil, err
	}

	out := map[string][]stopTime{}
	for _, c := range found {
		info := st.trips[c.trip]
		if c.pickup == "1" || int64(c.seq) == maxSeq[info.idx] {
			continue
		}
		if _, freq := st.freqs[c.trip]; freq {
			c.dep -= firstDep[info.idx]
		} else {
			c.start = firstDep[info.idx]
		}
		c.last = lastStop[info.idx]
		out[c.stop] = append(out[c.stop], c.stopTime)
	}
	tt.log.Info("indexed GTFS stop times",
		slog.Int("stops", len(want)),
		slog.Duration("took", tt.clock().Sub(start).Round(time.Millisecond)))
	return out, nil
}

func (tt *timetable) indexPath(stop string) string {
	sum := sha256.Sum256([]byte(stop))
	return filepath.Join(tt.dir, tt.prefix+hex.EncodeToString(sum[:8])+".stops")
}

// loadIndex reads a stop's saved times. Any problem reads as not saved, so
// the stop is scanned again.
func (tt *timetable) loadIndex(stop string) ([]stopTime, bool) {
	if tt.dir == "" {
		return nil, false
	}
	f, err := os.Open(tt.indexPath(stop))
	if err != nil {
		return nil, false
	}
	defer f.Close()
	cr := csv.NewReader(bufio.NewReader(f))
	cr.FieldsPerRecord = -1
	head, err := cr.Read()
	if err != nil || len(head) != 2 || head[0] != indexVersion || head[1] != stop {
		return nil, false
	}
	sts := []stopTime{}
	for {
		rec, err := cr.Read()
		if errors.Is(err, io.EOF) {
			return sts, true
		}
		if err != nil || len(rec) != 5 {
			return nil, false
		}
		dep, err1 := strconv.ParseInt(rec[1], 10, 32)
		seq, err2 := strconv.ParseUint(rec[2], 10, 32)
		start, err3 := strconv.ParseInt(rec[3], 10, 32)
		if err1 != nil || err2 != nil || err3 != nil {
			return nil, false
		}
		if _, ok := tt.st.trips[rec[0]]; !ok {
			return nil, false
		}
		sts = append(sts, stopTime{trip: rec[0], dep: int32(dep), seq: uint32(seq), start: int32(start), last: rec[4]})
	}
}

// saveIndex writes a stop's times next to the cached schedule. Failing to
// save only costs a scan in the next process.
func (tt *timetable) saveIndex(stop string, sts []stopTime) {
	if tt.dir == "" {
		return
	}
	tmp, err := os.CreateTemp(tt.dir, ".index-*")
	if err != nil {
		return
	}
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
	}()
	w := bufio.NewWriter(tmp)
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{indexVersion, stop})
	for _, s := range sts {
		_ = cw.Write([]string{s.trip, strconv.Itoa(int(s.dep)), strconv.FormatUint(uint64(s.seq), 10), strconv.Itoa(int(s.start)), s.last})
	}
	cw.Flush()
	if cw.Error() != nil || w.Flush() != nil || tmp.Close() != nil {
		return
	}
	_ = os.Rename(tmp.Name(), tt.indexPath(stop))
}

// planned is one timetabled departure on one service day.
type planned struct {
	trip  string
	day   date
	at    time.Time
	seq   uint32
	route string
	dir   int8
	// start is the trip's first departure, as seconds of the service day.
	start int32
	last  string
}

// planned expands stop times into departures between from and to on the
// service days that run then.
func (s *static) planned(sts []stopTime, from, to time.Time) []planned {
	// A service day's times can pass 24:00, so the day before from counts
	// too.
	var days []date
	for d := from.In(s.loc).AddDate(0, 0, -1); dateOf(d) <= dateOf(to.In(s.loc)); d = d.AddDate(0, 0, 1) {
		days = append(days, dateOf(d))
	}
	var out []planned
	for _, st := range sts {
		info, ok := s.trips[st.trip]
		if !ok {
			continue
		}
		svc := s.services[info.serviceID]
		if svc == nil {
			continue
		}
		freqs := s.freqs[st.trip]
		for _, d := range days {
			if !svc.runsOn(d) {
				continue
			}
			mid := d.midnight(s.loc)
			add := func(secs, start int32) {
				at := mid.Add(time.Duration(secs) * time.Second)
				if !at.Before(from) && !at.After(to) {
					out = append(out, planned{
						trip: st.trip, day: d, at: at, seq: st.seq, route: info.routeID,
						dir: info.direction, start: start, last: st.last,
					})
				}
			}
			if len(freqs) == 0 {
				add(st.dep, st.start)
				continue
			}
			for _, f := range freqs {
				for t := f.start; t < f.end; t += f.headway {
					add(t+st.dep, t)
				}
			}
		}
	}
	return out
}

// clearIndexes deletes the stop indexes saved in dir and returns the bytes
// freed.
func clearIndexes(dir string) (int64, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.stops"))
	if err != nil {
		return 0, err
	}
	var freed int64
	var errs []error
	for _, p := range paths {
		fi, err := os.Lstat(p)
		if err != nil || !fi.Mode().IsRegular() {
			continue
		}
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
			continue
		}
		freed += fi.Size()
	}
	return freed, errors.Join(errs...)
}
