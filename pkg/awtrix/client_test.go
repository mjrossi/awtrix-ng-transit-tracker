package awtrix_test

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/mjrossi/urbanist-timetable/pkg/awtrix"
)

type request struct {
	Method, Path, ContentType, User, Password string
	Body                                      map[string]any
}

func newServer(t *testing.T, status int, reply string) (*awtrix.Client, *[]request) {
	t.Helper()
	var got []request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := request{Method: r.Method, Path: r.URL.Path, ContentType: r.Header.Get("Content-Type")}
		req.User, req.Password, _ = r.BasicAuth()
		if b, _ := io.ReadAll(r.Body); len(b) > 0 {
			if err := json.Unmarshal(b, &req.Body); err != nil {
				t.Errorf("body is not JSON: %v", err)
			}
		}
		got = append(got, req)
		w.WriteHeader(status)
		_, _ = io.WriteString(w, reply)
	}))
	t.Cleanup(srv.Close)
	c, err := awtrix.NewClient(srv.URL, awtrix.WithBasicAuth("u", "p"))
	if err != nil {
		t.Fatal(err)
	}
	return c, &got
}

func TestPushApp(t *testing.T) {
	c, got := newServer(t, http.StatusOK, `{"ok":true}`)
	app := awtrix.App{
		LifetimeMs:     180000,
		LifetimeExpiry: awtrix.ExpiryRemove,
		Layout: &awtrix.Layout{Version: 1, Regions: []awtrix.Region{{
			ID:   "r0-min",
			Box:  awtrix.Box{40, 0, 12, 8},
			Text: awtrix.Fragments(awtrix.Fragment{Text: "3", Color: "#FFFFFF"}),
		}}},
	}
	if err := c.PushApp(t.Context(), "metro", app); err != nil {
		t.Fatal(err)
	}
	want := []request{{
		Method: http.MethodPut, Path: "/api/v1/apps/pushed/metro", ContentType: "application/json",
		User: "u", Password: "p",
		Body: map[string]any{
			"lifetimeMs":     180000.0,
			"lifetimeExpiry": "remove",
			"layout": map[string]any{
				"version": 1.0,
				"regions": []any{map[string]any{
					"id":   "r0-min",
					"box":  []any{40.0, 0.0, 12.0, 8.0},
					"text": []any{map[string]any{"text": "3", "color": "#FFFFFF"}},
				}},
			},
		},
	}}
	if diff := cmp.Diff(want, *got); diff != "" {
		t.Errorf("request mismatch (-want +got):\n%s", diff)
	}
}

func TestDeleteApp(t *testing.T) {
	c, got := newServer(t, http.StatusNoContent, "")
	if err := c.DeleteApp(t.Context(), "metro"); err != nil {
		t.Fatal(err)
	}
	if r := (*got)[0]; r.Method != http.MethodDelete || r.Path != "/api/v1/apps/metro" {
		t.Errorf("got %s %s", r.Method, r.Path)
	}
}

func TestDeleteMissingAppSucceeds(t *testing.T) {
	c, _ := newServer(t, http.StatusNotFound, `{"error":{"code":"notFound","message":"no app"}}`)
	if err := c.DeleteApp(t.Context(), "metro"); err != nil {
		t.Errorf("DeleteApp of a missing app: %v", err)
	}
	c, _ = newServer(t, http.StatusUnauthorized, "")
	if err := c.DeleteApp(t.Context(), "metro"); err == nil {
		t.Error("DeleteApp succeeded on 401")
	}
}

func TestAPIError(t *testing.T) {
	c, _ := newServer(t, http.StatusUnprocessableEntity,
		`{"error":{"code":"validationFailed","message":"outside the display","field":"layout.regions[0].box"}}`)
	err := c.PushApp(t.Context(), "metro", awtrix.App{Text: awtrix.PlainText("x")})
	var apiErr *awtrix.APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("got %v, want *APIError", err)
	}
	want := awtrix.APIError{Status: 422, Code: "validationFailed", Message: "outside the display", Field: "layout.regions[0].box"}
	if diff := cmp.Diff(want, *apiErr); diff != "" {
		t.Errorf("error mismatch (-want +got):\n%s", diff)
	}
}

func TestInvalidAppName(t *testing.T) {
	c, got := newServer(t, http.StatusOK, "")
	if err := c.PushApp(t.Context(), "bad name!", awtrix.App{}); err == nil {
		t.Error("PushApp accepted an invalid name")
	}
	if len(*got) != 0 {
		t.Error("request sent for an invalid name")
	}
}

func TestNewClient(t *testing.T) {
	if _, err := awtrix.NewClient("awtrixng-a1b2c3.local"); err != nil {
		t.Errorf("bare host: %v", err)
	}
	for _, bad := range []string{"ftp://clock", "http://", "://"} {
		if _, err := awtrix.NewClient(bad); err == nil {
			t.Errorf("NewClient(%q) succeeded", bad)
		}
	}
	if _, err := awtrix.NewClient("http://admin:hunter2@clock.local"); err != nil {
		t.Errorf("credentials in URL: %v", err)
	}
	_, err := awtrix.NewClient("http://admin:hunter2@clock.local:80x")
	if err == nil || strings.Contains(err.Error(), "hunter2") {
		t.Errorf("unparsable URL: err = %v, want an error without the password", err)
	}
}

// Host is the clock's identity for logs and state files, so a bare host
// keeps it and credentials never reach it.
func TestClientHost(t *testing.T) {
	tests := []struct{ url, want string }{
		{"192.168.1.50", "192.168.1.50"},
		{"awtrixng-a1b2c3.local", "awtrixng-a1b2c3.local"},
		{"http://awtrixng-a1b2c3.local", "awtrixng-a1b2c3.local"},
		{"http://admin:hunter2@clock.lan:8080", "clock.lan:8080"},
	}
	for _, tt := range tests {
		c, err := awtrix.NewClient(tt.url)
		if err != nil {
			t.Fatalf("NewClient(%q): %v", tt.url, err)
		}
		if got := c.Host(); got != tt.want {
			t.Errorf("NewClient(%q).Host() = %q, want %q", tt.url, got, tt.want)
		}
	}
}

// DeletePushedApp checks the script API first: a script sharing the name
// must not be deleted with the app.
func TestDeletePushedApp(t *testing.T) {
	script := false
	var got []request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := request{Method: r.Method, Path: r.URL.Path}
		got = append(got, req)
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/apps/script/metro":
			if script {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("print('hi')"))
			} else {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"error":{"code":"notFound","message":"no such script"}}`))
			}
		case r.Method == http.MethodDelete && r.URL.Path == "/api/v1/apps/metro":
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	t.Cleanup(srv.Close)
	c, err := awtrix.NewClient(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	// No script: the DELETE goes through.
	if err := c.DeletePushedApp(t.Context(), "metro"); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[1].Method != http.MethodDelete {
		t.Fatalf("requests = %+v, want a script check then a DELETE", got)
	}
	// A script has the name: no DELETE is sent.
	script = true
	got = nil
	if err := c.DeletePushedApp(t.Context(), "metro"); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Method != http.MethodGet {
		t.Fatalf("requests = %+v, want only the script check", got)
	}
	// A clock without scripting answers 503, which also means no scripts.
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		got = append(got, request{Method: r.Method, Path: r.URL.Path})
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv2.Close)
	c2, err := awtrix.NewClient(srv2.URL)
	if err != nil {
		t.Fatal(err)
	}
	got = nil
	if err := c2.DeletePushedApp(t.Context(), "metro"); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Method != http.MethodDelete {
		t.Fatalf("requests = %+v, want the DELETE after a 503", got)
	}
}

func TestTextJSON(t *testing.T) {
	for _, in := range []string{`"hello"`, `[{"text":"3","color":"#FFFFFF"},{"text":" "}]`} {
		var txt awtrix.Text
		if err := json.Unmarshal([]byte(in), &txt); err != nil {
			t.Fatal(err)
		}
		out, err := json.Marshal(txt)
		if err != nil {
			t.Fatal(err)
		}
		if string(out) != in {
			t.Errorf("round trip: got %s, want %s", out, in)
		}
	}
}
