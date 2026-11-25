package config_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/mjrossi/urbanist-timetable/internal/config"
)

func TestEstimateRequests(t *testing.T) {
	const head = `
timezone = "UTC"
%s
[clock]
url = "http://clock.local"
[providers.tl]
type = "demo"
[[providers.tl.lines]]
id = "a"
headway = "5m"
[providers.other]
type = "demo"
[[providers.other.lines]]
id = "a"
headway = "5m"
`
	board := func(name, extra string, stops ...string) string {
		var b strings.Builder
		fmt.Fprintf(&b, "[[boards]]\nname = %q\n%s\n", name, extra)
		for _, s := range stops {
			p, stop, _ := strings.Cut(s, ":")
			fmt.Fprintf(&b, "[[boards.stops]]\nprovider = %q\nstop = %q\n", p, stop)
		}
		return b.String()
	}
	tests := map[string]struct {
		top     string
		boards  []string
		refresh time.Duration
		want    int
	}{
		"one stop all day": {
			boards: []string{board("a", "", "tl:s1")}, refresh: 5 * time.Minute,
			want: 288 * 31,
		},
		"two stops, 2m": {
			boards: []string{board("a", "", "tl:s1", "tl:s2")}, refresh: 2 * time.Minute,
			want: 2 * 720 * 31,
		},
		"active hours": {
			boards: []string{board("a", `active_hours = "07:00-09:00"`, "tl:s1")}, refresh: 5 * time.Minute,
			want: 24 * 31,
		},
		"overnight window": {
			boards: []string{board("a", `active_hours = "23:00-01:00"`, "tl:s1")}, refresh: 5 * time.Minute,
			want: 24 * 31,
		},
		"partial interval rounds up": {
			boards: []string{board("a", `active_hours = "07:00-07:07"`, "tl:s1")}, refresh: 5 * time.Minute,
			want: 2 * 31,
		},
		"quiet hours": {
			top:    `quiet_hours = "00:00-12:00"`,
			boards: []string{board("a", "", "tl:s1")}, refresh: 5 * time.Minute,
			want: 144 * 31,
		},
		"board time zone": {
			top:    `quiet_hours = "00:00-12:00"`,
			boards: []string{board("a", "active_hours = \"07:00-09:00\"\ntimezone = \"Asia/Tokyo\"", "tl:s1")}, refresh: 5 * time.Minute,
			// 07:00-09:00 in Tokyo is 22:00-24:00 UTC, outside quiet hours.
			want: 24 * 31,
		},
		"boards share a stop": {
			boards: []string{
				board("a", `active_hours = "07:00-09:00"`, "tl:s1"),
				board("b", `active_hours = "08:00-10:00"`, "tl:s1"),
			},
			refresh: 5 * time.Minute,
			want:    36 * 31,
		},
		"different windows share a stop": {
			boards: []string{
				board("a", `active_hours = "07:00-09:00"`, "tl:s1"),
				board("b", "active_hours = \"07:00-09:00\"\nwindow = \"30m\"", "tl:s1"),
			},
			refresh: 5 * time.Minute,
			want:    24 * 31,
		},
		"each active span rounds up": {
			boards: []string{
				board("a", `active_hours = "07:00-07:06"`, "tl:s1"),
				board("b", `active_hours = "12:00-12:06"`, "tl:s1"),
			},
			refresh: 5 * time.Minute,
			want:    4 * 31,
		},
		"slow polling": {
			boards: []string{board("a", `poll_interval = "10m"`, "tl:s1")}, refresh: 2 * time.Minute,
			want: 144 * 31,
		},
		"slow boards polling at different times": {
			boards: []string{
				board("a", `poll_interval = "2m"`, "tl:s1"),
				board("b", `poll_interval = "3m"`, "tl:s1"),
			},
			refresh: time.Minute,
			// Minutes 0, 2, 3 and 4 of every six.
			want: 960 * 31,
		},
		"slow boards polling together": {
			boards: []string{
				board("a", `poll_interval = "4m"`, "tl:s1"),
				board("b", `poll_interval = "4m"`, "tl:s1"),
			},
			refresh: time.Minute,
			want:    360 * 31,
		},
		"active for less than one poll": {
			boards: []string{board("a", "active_hours = \"07:01-07:07\"\npoll_interval = \"10m\"", "tl:s1")}, refresh: time.Minute,
			want: 31,
		},
		"other providers ignored": {
			boards: []string{board("a", "", "other:s1")}, refresh: 5 * time.Minute,
			want: 0,
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			src := fmt.Sprintf(head, tt.top) + strings.Join(tt.boards, "\n")
			cfg, err := config.Parse([]byte(src), noEnv)
			if err != nil {
				t.Fatal(err)
			}
			got := cfg.EstimateRequests(map[string]time.Duration{"tl": tt.refresh})
			if got["tl"] != tt.want || got["other"] != 0 {
				t.Errorf("estimate = %v, want tl = %d alone", got, tt.want)
			}
		})
	}
}
