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

const (
	noScript    = `{"error":{"code":"notFound","message":"no such script"}}`
	noScripting = `{"error":{"code":"unavailable","message":"no scripting"}}`
	notSaved    = `{"error":{"code":"insufficientStorage","message":"applied, not saved yet"}}`
)

type reply struct {
	status int
	body   string
}

// fakeClock answers the calls ForgetApps makes and records the writes.
type fakeClock struct {
	apps string
	// device overrides the device answer.
	device    string
	scripting bool
	// scripts answers script lookups by name; others get noScript.
	scripts map[string]reply
	// orderReplies are returned by successive order writes, then 200.
	orderReplies []reply
	writes       []string
	order        map[string][]string
	pushed       map[string]any
}

func (c *fakeClock) client(t *testing.T) *awtrix.Client {
	t.Helper()
	const scriptPath = "/api/v1/apps/script/"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			switch {
			case r.URL.Path == "/api/v1/device" && c.device != "":
				_, _ = w.Write([]byte(c.device))
			case r.URL.Path == "/api/v1/device":
				_ = json.NewEncoder(w).Encode(map[string]any{"version": "1.2.3", "scriptingRunning": c.scripting})
			case r.URL.Path == "/api/v1/apps":
				_, _ = w.Write([]byte(c.apps))
			case strings.HasPrefix(r.URL.Path, scriptPath):
				rep, ok := c.scripts[strings.TrimPrefix(r.URL.Path, scriptPath)]
				if !ok {
					rep = reply{http.StatusNotFound, noScript}
				}
				w.WriteHeader(rep.status)
				_, _ = w.Write([]byte(rep.body))
			}
			return
		}
		c.writes = append(c.writes, r.Method+" "+r.URL.Path)
		var err error
		switch {
		case r.URL.Path == "/api/v1/apps/order":
			c.order = nil
			err = json.NewDecoder(r.Body).Decode(&c.order)
			if len(c.orderReplies) > 0 {
				w.WriteHeader(c.orderReplies[0].status)
				_, _ = w.Write([]byte(c.orderReplies[0].body))
				c.orderReplies = c.orderReplies[1:]
			}
		case strings.HasPrefix(r.URL.Path, "/api/v1/apps/pushed/"):
			c.pushed = nil
			err = json.NewDecoder(r.Body).Decode(&c.pushed)
		}
		if err != nil {
			t.Errorf("%s %s: %v", r.Method, r.URL.Path, err)
		}
	}))
	t.Cleanup(srv.Close)
	client, err := awtrix.NewClient(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

// Follows the firmware's GET /api/v1/apps: "metro" is a pushed app that
// hides a script of that name, and "refused" a script refused at boot.
const apps = `[
 {"name":"Nimbus","enabled":true,"slot":0,"present":true,"origin":"script"},
 {"name":"saldanha","enabled":true,"slot":1,"present":true,"origin":"pushed"},
 {"name":"Nimbus","enabled":true,"slot":2,"present":true,"origin":"script"},
 {"name":"b65","enabled":true,"slot":3,"present":false,"origin":null},
 {"name":"paused","enabled":false,"slot":4,"present":true,"origin":"pushed"},
 {"name":"metro","enabled":true,"slot":5,"present":true,"origin":"pushed"},
 {"name":"refused","enabled":false,"slot":6,"present":false,"origin":null},
 {"name":"Time","enabled":false,"slot":null,"present":true,"origin":"builtin"},
 {"name":"Weather","enabled":false,"slot":null,"present":true,"origin":"script"},
 {"name":"off","enabled":false,"slot":null,"present":false,"origin":null},
 {"name":"loose","enabled":true,"slot":null,"present":true,"origin":"pushed"},
 {"name":"location","import":"location","origin":"module","error":null}
]`

// The same clock with scripting off: arranged scripts lose their origin, and
// every stored script is listed again at the end, as disabled.
const appsScriptingOff = `[
 {"name":"Nimbus","enabled":true,"slot":0,"present":false,"origin":null},
 {"name":"saldanha","enabled":true,"slot":1,"present":true,"origin":"pushed"},
 {"name":"b65","enabled":true,"slot":2,"present":false,"origin":null},
 {"name":"Time","enabled":true,"slot":null,"present":true,"origin":"builtin"},
 {"name":"Weather","enabled":false,"slot":null,"present":false,"origin":null},
 {"name":"Nimbus","enabled":false,"slot":null,"present":true,"origin":"script"},
 {"name":"Weather","enabled":false,"slot":null,"present":true,"origin":"script"},
 {"name":"Solo","enabled":false,"slot":null,"present":true,"origin":"script"}
]`

var storedScripts = map[string]reply{
	"Nimbus":  {http.StatusOK, "class Nimbus end"},
	"Weather": {http.StatusOK, "class Weather end"},
	"metro":   {http.StatusOK, "class Metro end"},
	"refused": {http.StatusOK, "syntax error"},
}

func TestForgetApps(t *testing.T) {
	const order = "PUT /api/v1/apps/order"
	all := map[string][]string{
		"order":    {"Nimbus", "saldanha", "Nimbus", "b65", "paused", "metro", "refused"},
		"disabled": {"paused", "refused", "Time", "Weather", "off"},
	}
	without := func(name string) map[string][]string {
		out := map[string][]string{}
		for k, names := range all {
			for _, n := range names {
				if n != name {
					out[k] = append(out[k], n)
				}
			}
		}
		return out
	}
	tests := []struct {
		name    string
		forget  []string
		writes  []string
		order   map[string][]string
		removed []string
	}{
		{"pushed app", []string{"saldanha"}, []string{order, "PUT /api/v1/apps/pushed/saldanha"}, without("saldanha"), []string{"saldanha"}},
		{"expired app keeping a slot", []string{"b65"}, []string{order}, without("b65"), []string{"b65"}},
		{"expired app that was switched off", []string{"off"}, []string{order}, without("off"), []string{"off"}},
		{"pushed app without a slot", []string{"loose"}, []string{order, "PUT /api/v1/apps/pushed/loose"}, all, []string{"loose"}},
		{"pushed app hiding a script", []string{"metro"}, []string{"PUT /api/v1/apps/pushed/metro"}, nil, nil},
		{"script refused at boot", []string{"refused"}, nil, nil, nil},
		{"names the clock uses otherwise", []string{"Nimbus", "Weather", "Time", "location"}, nil, nil, nil},
		{"invalid name", []string{"a/../b"}, nil, nil, nil},
		// The arrangement is written even so: see TestForgetAppsRetriesUnsavedOrder.
		{"unknown to the clock", []string{"ghost"}, []string{order}, all, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &fakeClock{apps: apps, scripting: true, scripts: storedScripts}
			removed, err := c.client(t).ForgetApps(t.Context(), tt.forget)
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(tt.writes, c.writes); diff != "" {
				t.Errorf("writes (-want +got):\n%s", diff)
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

// Stored scripts keep their places and switches: the entries listed as
// disabled only because scripting is off are not written back.
func TestForgetAppsScriptingOff(t *testing.T) {
	c := &fakeClock{apps: appsScriptingOff, scripts: storedScripts}
	if _, err := c.client(t).ForgetApps(t.Context(), []string{"b65", "Nimbus", "Solo"}); err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{"order": {"Nimbus", "saldanha"}, "disabled": {"Weather"}}
	if diff := cmp.Diff(want, c.order); diff != "" {
		t.Errorf("order (-want +got):\n%s", diff)
	}
}

// The list itself also shows ownership, whatever the script lookup says.
func TestForgetAppsTrustsListOverLookup(t *testing.T) {
	for _, list := range []string{apps, appsScriptingOff} {
		c := &fakeClock{apps: list, scripting: list == apps}
		removed, err := c.client(t).ForgetApps(t.Context(), []string{"Nimbus", "Weather"})
		if err != nil || len(removed) != 0 || len(c.writes) != 0 {
			t.Errorf("removed %v, writes %v, err %v; want nothing", removed, c.writes, err)
		}
	}
}

func TestForgetAppsWithoutScriptingSupport(t *testing.T) {
	c := &fakeClock{apps: apps, scripts: map[string]reply{"b65": {http.StatusServiceUnavailable, noScripting}}}
	removed, err := c.client(t).ForgetApps(t.Context(), []string{"b65"})
	if err != nil || !cmp.Equal(removed, []string{"b65"}) {
		t.Errorf("removed %v, err %v", removed, err)
	}
}

func TestForgetAppsWritesNothingWhenUnsure(t *testing.T) {
	const builtin = `{"name":"Time","enabled":true,"slot":null,"present":true,"origin":"builtin"}`
	lookup := func(status int, body string) fakeClock {
		return fakeClock{apps: apps, scripting: true, scripts: map[string]reply{"b65": {status, body}}}
	}
	list := func(entries string) fakeClock { return fakeClock{apps: entries, scripting: true} }
	tests := map[string]fakeClock{
		"lookup: generic 503":       lookup(http.StatusServiceUnavailable, "busy"),
		"lookup: generic 404":       lookup(http.StatusNotFound, "<html>"),
		"lookup: other code":        lookup(http.StatusNotFound, `{"error":{"code":"noRoute"}}`),
		"lookup: auth":              lookup(http.StatusUnauthorized, ""),
		"device: no scripting flag": {apps: apps, device: `{"version":"1.2.3"}`},
		"list: null":                list(`null`),
		"list: empty":               list(`[]`),
		"list: no built-in app":     list(`[{"name":"b65","enabled":true,"slot":0,"present":false,"origin":null}]`),
		"list: missing slot":        list(`[{"name":"a","enabled":true,"slot":0,"present":true,"origin":"pushed"},{"name":"b65","enabled":true,"slot":2,"present":false,"origin":null},` + builtin + `]`),
		"list: slot after the rest": list(`[` + builtin + `,{"name":"b65","enabled":true,"slot":0,"present":false,"origin":null}]`),
		"list: no name":             list(`[{"enabled":true,"slot":0,"present":true,"origin":"pushed"},` + builtin + `]`),
		"list: no enabled":          list(`[{"name":"a","slot":0,"present":true,"origin":"pushed"},` + builtin + `]`),
	}
	for name, c := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := c.client(t).ForgetApps(t.Context(), []string{"b65"}); err == nil {
				t.Error("want an error")
			}
			if len(c.writes) != 0 {
				t.Errorf("writes = %v, want none", c.writes)
			}
		})
	}
}

// A 507 means the order is in use but not saved, so the retry must write it
// again although the name is already gone from the list.
func TestForgetAppsRetriesUnsavedOrder(t *testing.T) {
	c := &fakeClock{apps: apps, scripting: true, orderReplies: []reply{{http.StatusInsufficientStorage, notSaved}}}
	client := c.client(t)
	if _, err := client.ForgetApps(t.Context(), []string{"b65"}); err == nil {
		t.Fatal("want an error for an unsaved order")
	}
	c.apps = `[{"name":"Time","enabled":true,"slot":0,"present":true,"origin":"builtin"}]`
	if _, err := client.ForgetApps(t.Context(), []string{"b65"}); err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]string{"PUT /api/v1/apps/order", "PUT /api/v1/apps/order"}, c.writes); diff != "" {
		t.Errorf("writes (-want +got):\n%s", diff)
	}
}

// The firmware requires both lists, so empty ones are sent as [].
func TestForgetAppsSendsEmptyLists(t *testing.T) {
	c := &fakeClock{scripting: true, apps: `[
	 {"name":"b65","enabled":true,"slot":0,"present":false,"origin":null},
	 {"name":"Time","enabled":true,"slot":null,"present":true,"origin":"builtin"}]`}
	if _, err := c.client(t).ForgetApps(t.Context(), []string{"b65"}); err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(map[string][]string{"order": {}, "disabled": {}}, c.order); diff != "" {
		t.Errorf("order (-want +got):\n%s", diff)
	}
}
