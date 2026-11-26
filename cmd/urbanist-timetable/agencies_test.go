package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"

	"github.com/mjrossi/urbanist-timetable/internal/directory"
)

// fakeDirectories serves the directory package's recorded Mobility Database
// list and no Transitland key.
func fakeDirectories(t *testing.T) {
	t.Helper()
	csv, err := os.ReadFile(filepath.Join("..", "..", "internal", "directory", "testdata", "feeds_v2.csv"))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(csv) }))
	t.Cleanup(srv.Close)
	old := directoryURLs
	directoryURLs.mobilityDB = srv.URL
	t.Cleanup(func() { directoryURLs = old })
	t.Setenv(directory.TransitlandKeyVar, "")
	t.Setenv("TIMETABLE_CACHE", t.TempDir())
}

func TestAgenciesSearch(t *testing.T) {
	fakeDirectories(t)
	out, stderr, err := runCLIOut(t, demoConfig, "agencies", "search", "mta")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 3 || !strings.HasPrefix(lines[0], "AGENCY") || !strings.HasPrefix(lines[1], "MTA ") || !strings.Contains(lines[1], "mta-subway") || !strings.Contains(lines[1], "verified") || !strings.Contains(lines[2], "mdb-507") {
		t.Errorf("output:\n%s\nwant the catalog's mta-subway first, then LIRR", out)
	}
	if strings.Contains(out, "mdb-516") {
		t.Errorf("the catalog's feed listed again:\n%s", out)
	}
	if !strings.Contains(stderr, directory.TransitlandKeyVar) {
		t.Errorf("stderr %q does not say how to add Transitland", stderr)
	}

	out, _, err = runCLIOut(t, demoConfig, "agencies", "search", "--json", "caltrain")
	if err != nil {
		t.Fatal(err)
	}
	var got []directory.System
	if err := json.Unmarshal([]byte(out), &got); err != nil || len(got) != 1 || got[0].Needs[0] != "CALTRAIN_API_KEY" {
		t.Errorf("JSON %s, err %v", out, err)
	}
}

func TestSystemsShowDirectory(t *testing.T) {
	fakeDirectories(t)
	out, _, err := runCLIOut(t, demoConfig, "systems", "show", "mdb-54")
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Providers map[string]map[string]any `toml:"providers"`
	}
	if err := toml.Unmarshal([]byte(out), &cfg); err != nil {
		t.Fatalf("not TOML: %v\n%s", err, out)
	}
	block := cfg.Providers["mdb-54"]
	if block["type"] != "gtfsrt" || !strings.Contains(out, "${CALTRAIN_API_KEY}") || !strings.Contains(out, "# Needs CALTRAIN_API_KEY") {
		t.Errorf("block:\n%s", out)
	}
}

func TestDirectorySystemAsSource(t *testing.T) {
	fakeDirectories(t)
	dir := &directory.Directory{MobilityDBURL: directoryURLs.mobilityDB}
	if _, err := openSource(t.Context(), filepath.Join(t.TempDir(), "none.toml"), "mdb-53", dir); err != nil {
		t.Errorf("mdb-53: %v", err)
	}
	t.Setenv("CALTRAIN_API_KEY", "")
	os.Unsetenv("CALTRAIN_API_KEY")
	_, err := openSource(t.Context(), filepath.Join(t.TempDir(), "none.toml"), "mdb-54", dir)
	if err == nil || !strings.Contains(err.Error(), "CALTRAIN_API_KEY") {
		t.Errorf("err = %v, want the key to set", err)
	}
}
