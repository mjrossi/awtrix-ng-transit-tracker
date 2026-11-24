package awtrix_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/mjrossi/urbanist-timetable/pkg/awtrix"
)

// fakeClock answers the calls ForgetApps makes and records the writes.
// scripts are the names its script API knows; the rest answer 404.
type fakeClock struct {
	apps         string
	device       string
	scripting    bool
	scripts      map[string]string
	writes       []string
	scriptChecks []string
	order        map[string][]string
}

func (c *fakeClock) client(t *testing.T) *awtrix.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/device":
			if c.device != "" {
				_, _ = w.Write([]byte(c.device))
			} else {
				_ = json.NewEncoder(w).Encode(map[string]any{"version": "1.2.3", "scriptingRunning": c.scripting})
			}
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/apps":
			_, _ = w.Write([]byte(c.apps))
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/v1/apps/script/"):
			name := strings.TrimPrefix(r.URL.Path, "/api/v1/apps/script/")
			c.scriptChecks = append(c.scriptChecks, name)
			if src, ok := c.scripts[name]; ok {
				_, _ = w.Write([]byte(src))
			} else {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"error":{"code":"notFound","message":"no such script"}}`))
			}
		default:
			c.writes = append(c.writes, r.Method+" "+r.URL.Path)
			if r.URL.Path == "/api/v1/apps/order" {
				if err := json.NewDecoder(r.Body).Decode(&c.order); err != nil {
					t.Errorf("order body: %v", err)
				}
			}
			_, _ = w.Write([]byte(`{"ok":true}`))
		}
	}))
	t.Cleanup(srv.Close)
	client, err := awtrix.NewClient(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

// The firmware's GET /api/v1/apps: pushed apps carry origin "pushed", and
// when a pushed app and a script share a name only the pushed one is
// listed. A name that only keeps a place, such as one left by an app that
// expired or by a script refused at boot, has no origin and present false.
const apps = `[
 {"name":"Nimbus","enabled":true,"slot":0,"present":true,"origin":"script"},
 {"name":"saldanha","enabled":true,"slot":1,"present":true,"origin":"pushed"},
 {"name":"Nimbus","enabled":true,"slot":2,"present":true,"origin":"script"},
 {"name":"b65","enabled":true,"slot":3,"present":false,"origin":null},
 {"name":"paused","enabled":false,"slot":4,"present":true,"origin":"pushed"},
 {"name":"metro","enabled":true,"slot":5,"present":true,"origin":"pushed"},
 {"name":"refused","enabled":false,"slot":6,"present":false,"origin":null},
 {"name":"gamma","enabled":true,"slot":7,"present":true,"origin":"pushed"},
 {"name":"Time","enabled":false,"slot":null,"present":true,"origin":"builtin"},
 {"name":"Weather","enabled":false,"slot":null,"present":true,"origin":"script"},
 {"name":"off","enabled":false,"slot":null,"present":false,"origin":null},
 {"name":"loose","enabled":true,"slot":null,"present":true,"origin":"pushed"},
 {"name":"location","import":"location","origin":"module","error":null}
]`

// scripts are the stored scripts the clock's script API knows: "metro" is
// also a pushed app, and "refused" was refused at boot but is still listed
// as an empty placeholder.
var scripts = map[string]string{
	"Nimbus":  "print('n')",
	"Weather": "print('w')",
	"metro":   "print('m')",
	"refused": "syntax error",
}

func TestForgetApps(t *testing.T) {
	tests := []struct {
		name         string
		forget       []string
		scripting    bool
		writes       []string
		scriptChecks []string
		order        map[string][]string
		removed      []string
	}{
		{
			"pushed app is deleted and its slot rewritten",
			[]string{"saldanha"},
			true,
			[]string{"DELETE /api/v1/apps/saldanha", "PUT /api/v1/apps/order"},
			[]string{"saldanha"},
			map[string][]string{
				"order":    {"Nimbus", "Nimbus", "b65", "paused", "metro", "refused", "gamma"},
				"disabled": {"paused", "refused", "Time", "Weather", "off"},
			},
			[]string{"saldanha"},
		},
		{
			"placeholder only needs the order rewrite",
			[]string{"b65"},
			true,
			[]string{"PUT /api/v1/apps/order"},
			nil,
			map[string][]string{
				"order":    {"Nimbus", "saldanha", "Nimbus", "paused", "metro", "refused", "gamma"},
				"disabled": {"paused", "refused", "Time", "Weather", "off"},
			},
			[]string{"b65"},
		},
		{
			// A script refused at boot is listed as an empty placeholder;
			// DELETE on its name would remove its source.
			"placeholder that is a refused script is not deleted",
			[]string{"refused"},
			true,
			[]string{"PUT /api/v1/apps/order"},
			nil,
			map[string][]string{
				"order":    {"Nimbus", "saldanha", "Nimbus", "b65", "paused", "metro", "gamma"},
				"disabled": {"paused", "Time", "Weather", "off"},
			},
			[]string{"refused"},
		},
		{
			// The clock lists a pushed app and a script sharing a name only
			// as the pushed one; DELETE would remove the script's source.
			"pushed app sharing a script's name is left alone",
			[]string{"metro"},
			true,
			nil,
			[]string{"metro"},
			nil,
			nil,
		},
		{
			"pushed app without a script is deleted",
			[]string{"gamma"},
			true,
			[]string{"DELETE /api/v1/apps/gamma", "PUT /api/v1/apps/order"},
			[]string{"gamma"},
			map[string][]string{
				"order":    {"Nimbus", "saldanha", "Nimbus", "b65", "paused", "metro", "refused"},
				"disabled": {"paused", "refused", "Time", "Weather", "off"},
			},
			[]string{"gamma"},
		},
		{
			"not arranged",
			[]string{"loose"},
			true,
			[]string{"DELETE /api/v1/apps/loose"},
			[]string{"loose"},
			nil,
			[]string{"loose"},
		},
		// DELETE on a script's, built-in's or module's name would delete
		// its source or settings.
		{"names the clock uses otherwise", []string{"Nimbus", "Weather", "Time", "location"}, true, nil, nil, nil, nil},
		{"unknown to the clock", []string{"ghost"}, true, nil, nil, nil, nil},
		{
			"scripting off keeps a slotted script in the rotation",
			[]string{"saldanha"},
			false,
			[]string{"DELETE /api/v1/apps/saldanha", "PUT /api/v1/apps/order"},
			[]string{"saldanha"},
			map[string][]string{
				"order":    {"Nimbus", "Nimbus", "b65", "paused", "metro", "refused", "gamma"},
				"disabled": {"paused", "refused", "Time", "off"},
			},
			[]string{"saldanha"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &fakeClock{apps: apps, scripting: tt.scripting, scripts: scripts}
			removed, err := c.client(t).ForgetApps(t.Context(), tt.forget)
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(tt.writes, c.writes); diff != "" {
				t.Errorf("writes (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tt.scriptChecks, c.scriptChecks); diff != "" {
				t.Errorf("script checks (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tt.order, c.order); diff != "" {
				t.Errorf("order (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tt.removed, removed); diff != "" {
				t.Errorf("removed (-want +got):\n%s", diff)
			}
		})
	}
}

// A null or empty app list is not a success: the clock always lists its
// built-in apps, so acting on an empty answer could wipe the rotation.
func TestForgetAppsRejectsEmptyList(t *testing.T) {
	for _, list := range []string{"null", "[]"} {
		c := &fakeClock{apps: list, scripting: true, scripts: scripts}
		if _, err := c.client(t).ForgetApps(t.Context(), []string{"b65"}); err == nil {
			t.Errorf("apps = %s: ForgetApps succeeded on an empty list", list)
		}
		if len(c.writes) != 0 {
			t.Errorf("apps = %s: writes = %v, want none", list, c.writes)
		}
	}
}

// A device answer without scriptingRunning must not be read as false, or
// slotted scripts would be dropped from the rotation.
func TestForgetAppsRejectsMissingFlag(t *testing.T) {
	c := &fakeClock{apps: apps, device: `{"version":"1.2.3"}`, scripts: scripts}
	if _, err := c.client(t).ForgetApps(t.Context(), []string{"b65"}); err == nil {
		t.Error("ForgetApps succeeded without scriptingRunning")
	}
	if len(c.writes) != 0 {
		t.Errorf("writes = %v, want none", c.writes)
	}
}

// The firmware requires both lists, so empty ones are sent as [].
func TestForgetAppsSendsEmptyLists(t *testing.T) {
	c := &fakeClock{apps: `[{"name":"b65","enabled":true,"slot":0,"present":false,"origin":null}]`, scripting: true}
	if _, err := c.client(t).ForgetApps(t.Context(), []string{"b65"}); err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(map[string][]string{"order": {}, "disabled": {}}, c.order); diff != "" {
		t.Errorf("order (-want +got):\n%s", diff)
	}
}
