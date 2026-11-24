package awtrix_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/mjrossi/urbanist-timetable/pkg/awtrix"
)

// fakeClock answers the calls ForgetApps makes and records the writes.
type fakeClock struct {
	apps      string
	scripting bool
	writes    []string
	order     map[string][]string
}

func (c *fakeClock) client(t *testing.T) *awtrix.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/device":
			_ = json.NewEncoder(w).Encode(map[string]any{"version": "1.2.3", "scriptingRunning": c.scripting})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/apps":
			_, _ = w.Write([]byte(c.apps))
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

func TestForgetApps(t *testing.T) {
	const apps = `[
	 {"name":"Nimbus","enabled":true,"slot":0,"present":true,"origin":"script"},
	 {"name":"saldanha","enabled":true,"slot":1,"present":true,"origin":"pushed"},
	 {"name":"Nimbus","enabled":true,"slot":2,"present":true,"origin":"script"},
	 {"name":"b65","enabled":true,"slot":3,"present":false,"origin":null},
	 {"name":"paused","enabled":false,"slot":4,"present":true,"origin":"pushed"},
	 {"name":"Time","enabled":false,"slot":null,"present":true,"origin":"builtin"},
	 {"name":"Weather","enabled":false,"slot":null,"present":true,"origin":"script"},
	 {"name":"off","enabled":false,"slot":null,"present":false,"origin":null},
	 {"name":"loose","enabled":true,"slot":null,"present":true,"origin":"pushed"},
	 {"name":"location","import":"location","origin":"module","error":null}
	]`
	tests := []struct {
		name      string
		forget    []string
		scripting bool
		writes    []string
		order     map[string][]string
	}{
		{
			"expired app keeping a slot",
			[]string{"b65"},
			true,
			[]string{"DELETE /api/v1/apps/b65", "PUT /api/v1/apps/order"},
			map[string][]string{
				"order":    {"Nimbus", "saldanha", "Nimbus", "paused"},
				"disabled": {"paused", "Time", "Weather", "off"},
			},
		},
		{
			"expired app that was switched off",
			[]string{"off"},
			true,
			[]string{"DELETE /api/v1/apps/off", "PUT /api/v1/apps/order"},
			map[string][]string{
				"order":    {"Nimbus", "saldanha", "Nimbus", "b65", "paused"},
				"disabled": {"paused", "Time", "Weather"},
			},
		},
		{"not arranged", []string{"loose"}, true, []string{"DELETE /api/v1/apps/loose"}, nil},
		// DELETE on a script's name deletes its source.
		{"names the clock uses otherwise", []string{"Nimbus", "Weather", "Time", "location"}, true, nil, nil},
		{"unknown to the clock", []string{"ghost"}, true, nil, nil},
		// With scripting off, the clock lists every stored script as disabled.
		{
			"scripting off",
			[]string{"b65"},
			false,
			[]string{"DELETE /api/v1/apps/b65", "PUT /api/v1/apps/order"},
			map[string][]string{
				"order":    {"saldanha", "paused"},
				"disabled": {"paused", "Time", "off"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &fakeClock{apps: apps, scripting: tt.scripting}
			if err := c.client(t).ForgetApps(t.Context(), tt.forget); err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(tt.writes, c.writes); diff != "" {
				t.Errorf("writes (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tt.order, c.order); diff != "" {
				t.Errorf("order (-want +got):\n%s", diff)
			}
		})
	}
}

// The firmware requires both lists, so empty ones are sent as [].
func TestForgetAppsSendsEmptyLists(t *testing.T) {
	c := &fakeClock{apps: `[{"name":"b65","enabled":true,"slot":0,"present":false,"origin":null}]`, scripting: true}
	if err := c.client(t).ForgetApps(t.Context(), []string{"b65"}); err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(map[string][]string{"order": {}, "disabled": {}}, c.order); diff != "" {
		t.Errorf("order (-want +got):\n%s", diff)
	}
}
