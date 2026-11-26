package transit_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/mjrossi/urbanist-timetable/pkg/transit"
)

var now = time.Date(2026, 11, 22, 8, 0, 0, 0, time.UTC)

func dep(line, headsign string, inMin int, predicted bool) transit.Departure {
	t := now.Add(time.Duration(inMin) * time.Minute)
	d := transit.Departure{
		Line:      transit.Line{ID: line, ShortName: line, Mode: transit.ModeMetro},
		Headsign:  headsign,
		StopID:    "s1",
		Scheduled: t,
		Status:    transit.StatusScheduled,
	}
	if predicted {
		p := t.Add(30 * time.Second)
		d.Predicted = &p
	}
	return d
}

func TestMinutesUntil(t *testing.T) {
	tests := []struct {
		in   time.Duration
		want int
	}{
		{-2 * time.Minute, 0},
		{0, 0},
		{59 * time.Second, 0},
		{61 * time.Second, 1},
		{10*time.Minute + 59*time.Second, 10},
	}
	for _, tt := range tests {
		d := transit.Departure{Scheduled: now.Add(tt.in)}
		if got := d.MinutesUntil(now); got != tt.want {
			t.Errorf("MinutesUntil(%s) = %d, want %d", tt.in, got, tt.want)
		}
	}
}

func TestDepartureTimePrefersPrediction(t *testing.T) {
	d := dep("azul", "x", 5, true)
	if !d.Realtime() || !d.Time().Equal(*d.Predicted) {
		t.Fatalf("Time() = %s, want predicted %s", d.Time(), *d.Predicted)
	}
	d.Predicted = nil
	if d.Realtime() || !d.Time().Equal(d.Scheduled) {
		t.Fatalf("Time() = %s, want scheduled %s", d.Time(), d.Scheduled)
	}
}

func TestFilter(t *testing.T) {
	canceled := dep("azul", "Santa Apolónia", 6, false)
	canceled.Status = transit.StatusCanceled
	deps := []transit.Departure{
		dep("azul", "Santa Apolónia", -1, false),
		dep("azul", "Santa Apolónia", 2, false),
		dep("azul", "Reboleira", 7, false),
		dep("verde", "Cais do Sodré", 8, false),
		canceled,
	}
	names := func(ds []transit.Departure) []string {
		var out []string
		for _, d := range ds {
			out = append(out, d.Line.ID+"/"+d.Headsign+"/"+d.Scheduled.Format("15:04"))
		}
		return out
	}

	tests := []struct {
		name string
		f    transit.Filter
		want []string
	}{
		{"zero drops past and canceled", transit.Filter{}, []string{
			"azul/Santa Apolónia/08:02", "azul/Reboleira/08:07", "verde/Cais do Sodré/08:08",
		}},
		{"lines case-insensitive", transit.Filter{Lines: []string{"VERDE"}}, []string{"verde/Cais do Sodré/08:08"}},
		{"headsign substring", transit.Filter{Headsigns: []string{"apol"}}, []string{"azul/Santa Apolónia/08:02"}},
		{"min lead", transit.Filter{MinLead: 5 * time.Minute}, []string{"azul/Reboleira/08:07", "verde/Cais do Sodré/08:08"}},
		{"keep canceled", transit.Filter{KeepCanceled: true, Lines: []string{"azul"}, Headsigns: []string{"santa"}}, []string{
			"azul/Santa Apolónia/08:02", "azul/Santa Apolónia/08:06",
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if diff := cmp.Diff(tt.want, names(tt.f.Apply(now, deps))); diff != "" {
				t.Errorf("Apply mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestFilterNarrows sets each field in turn, so a new field must be
// classified here before the test passes.
func TestFilterNarrows(t *testing.T) {
	widens := map[string]bool{"KeepCanceled": true}
	if (transit.Filter{}).Narrows() {
		t.Error("zero Filter narrows")
	}
	typ := reflect.TypeFor[transit.Filter]()
	for i := range typ.NumField() {
		field := typ.Field(i)
		var f transit.Filter
		v := reflect.ValueOf(&f).Elem().Field(i)
		switch v.Kind() {
		case reflect.Slice:
			v.Set(reflect.MakeSlice(v.Type(), 1, 1))
		case reflect.Bool:
			v.SetBool(true)
		case reflect.Int64:
			v.SetInt(1)
		default:
			t.Fatalf("field %s: kind %s not covered", field.Name, v.Kind())
		}
		if got, want := f.Narrows(), !widens[field.Name]; got != want {
			t.Errorf("only %s set: Narrows() = %v, want %v", field.Name, got, want)
		}
	}
}

func TestGroup(t *testing.T) {
	deps := []transit.Departure{
		dep("verde", "Telheiras", 9, false),
		dep("azul", "Reboleira", 4, true),
		dep("azul", "Reboleira", 1, true),
		dep("verde", "Telheiras", 3, false),
		dep("azul", "Reboleira", 12, false),
		dep("azul", "Santa Apolónia", 6, false),
	}
	type row struct {
		Line, Headsign string
		Mins           []int
	}
	summarize := func(rows []transit.Row) []row {
		var out []row
		for _, r := range rows {
			x := row{Line: r.Line.ID, Headsign: r.Headsign}
			for _, d := range r.Departures {
				x.Mins = append(x.Mins, int(d.Scheduled.Sub(now)/time.Minute))
			}
			out = append(out, x)
		}
		return out
	}

	got := summarize(transit.Group(deps, 2, 2))
	want := []row{
		{"azul", "Reboleira", []int{1, 4}},
		{"verde", "Telheiras", []int{3, 9}},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Group(2, 2) mismatch (-want +got):\n%s", diff)
	}

	if n := len(transit.Group(deps, 0, 0)); n != 3 {
		t.Errorf("Group(0, 0) returned %d rows, want 3", n)
	}
	if transit.Group(nil, 2, 2) != nil {
		t.Error("Group(nil) should be nil")
	}
}

func TestNormalizeColor(t *testing.T) {
	tests := []struct{ in, want string }{
		{"ee352e", "#EE352E"},
		{"#0072ce", "#0072CE"},
		{" #FFC20E ", "#FFC20E"},
		{"", ""},
		{"FFF", ""},
		{"GREEN", ""},
		{"#12345G", ""},
		{"#1234567", ""},
	}
	for _, tt := range tests {
		if got := transit.NormalizeColor(tt.in); got != tt.want {
			t.Errorf("NormalizeColor(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
