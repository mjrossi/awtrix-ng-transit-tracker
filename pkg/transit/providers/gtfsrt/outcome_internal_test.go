package gtfsrt

import (
	"errors"
	"testing"

	"github.com/mjrossi/urbanist-timetable/pkg/transit"
)

func TestOutcomeFailure(t *testing.T) {
	errSchedule := errors.New("schedule down")
	errFeed := errors.New("feed down")
	errTT := errors.New("timetable unreadable")
	tests := []struct {
		name string
		o    outcome
		// want is an error failure must wrap, or nil for an answer.
		want error
	}{
		{"schedule only, never loaded", outcome{scheduleErr: errSchedule}, errSchedule},
		{"realtime only, no feed loaded", outcome{realtime: true, feedProblems: []error{errFeed}}, errFeed},
		{"the only feed serving the stop failed", outcome{
			realtime: true, schedule: true, stopKnown: true, feedsLoaded: 1,
			feedProblems: []error{errFeed}, servedByFailed: true,
		}, errFeed},
		{"another feed serves the stop", outcome{
			realtime: true, schedule: true, stopKnown: true, feedsLoaded: 1,
			feedProblems: []error{errFeed}, servedByFailed: true, servedByOK: true,
		}, nil},
		{"the timetable stands in for a failed feed", outcome{
			realtime: true, schedule: true, stopKnown: true, hasTimes: true,
			feedProblems: []error{errFeed}, servedByFailed: true,
		}, nil},
		{"complete answer without the stop", outcome{realtime: true, schedule: true, feedsLoaded: 1}, transit.ErrUnknownStop},
		{"a failed feed might have the stop", outcome{
			realtime: true, schedule: true, feedsLoaded: 1, feedProblems: []error{errFeed},
		}, nil},
		{"a stale schedule might lack a new stop", outcome{
			realtime: true, schedule: true, scheduleErr: errSchedule, feedsLoaded: 1,
		}, nil},
		{"schedule only, timetable unreadable", outcome{schedule: true, stopKnown: true, timetableErr: errTT}, errTT},
		{"schedule only, no timetable", outcome{schedule: true, stopKnown: true, noTimetable: true}, errNoTimetable},
		{"schedule only, stop has times", outcome{schedule: true, stopKnown: true, hasTimes: true}, nil},
		{"schedule only, ended, nothing planned", outcome{
			schedule: true, stopKnown: true, hasTimes: true, outOfRange: errScheduleEnded,
		}, errScheduleEnded},
		{"schedule only, not started, nothing planned", outcome{
			schedule: true, stopKnown: true, hasTimes: true, outOfRange: errScheduleNotStarted,
		}, errScheduleNotStarted},
		{"schedule only, ended, trips still planned", outcome{
			schedule: true, stopKnown: true, hasTimes: true, planned: 2, outOfRange: errScheduleEnded,
		}, nil},
		{"ended, with realtime", outcome{
			realtime: true, schedule: true, stopKnown: true, feedsLoaded: 1, servedByOK: true, outOfRange: errScheduleEnded,
		}, nil},
		{"realtime with a schedule lacking a timetable", outcome{
			realtime: true, schedule: true, stopKnown: true, feedsLoaded: 1, servedByOK: true, noTimetable: true,
		}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.o.failure("S1")
			switch {
			case tt.want == nil && err != nil:
				t.Errorf("failure = %v, want an answer", err)
			case tt.want != nil && !errors.Is(err, tt.want):
				t.Errorf("failure = %v, want it to wrap %v", err, tt.want)
			}
		})
	}
}

func TestOutcomeProblems(t *testing.T) {
	errSchedule, errFeed, errTT := errors.New("schedule"), errors.New("feed"), errors.New("timetable")
	o := outcome{scheduleErr: errSchedule, feedProblems: []error{errFeed}, timetableErr: errTT, outOfRange: errScheduleEnded}
	got := o.problems()
	if len(got) != 4 || !errors.Is(got[0], errFeed) || !errors.Is(got[1], errSchedule) || !errors.Is(got[2], errTT) || !errors.Is(got[3], errScheduleEnded) {
		t.Errorf("problems = %v, want feed, schedule, timetable, ended", got)
	}
	if got := (outcome{}).problems(); len(got) != 0 {
		t.Errorf("problems of a clean outcome = %v, want none", got)
	}
}
