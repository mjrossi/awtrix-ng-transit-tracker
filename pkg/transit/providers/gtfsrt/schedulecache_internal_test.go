package gtfsrt

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A file another process renames into place while this one reads must not
// break the read: the open file is what gets parsed.
func TestCacheFileSurvivesAReplacement(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.gtfs")
	if err := os.WriteFile(path, append([]byte("{}\n"), testZip(t)...), 0o600); err != nil {
		t.Fatal(err)
	}
	cf, err := openCacheFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer cf.Close()
	other := filepath.Join(dir, "other")
	if err := os.WriteFile(other, []byte("{\"etag\":\"a much longer header than before\"}\nnot a zip"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(other, path); err != nil {
		t.Fatal(err)
	}
	if _, err := cf.parse(); err != nil {
		t.Errorf("parse after a replacement: %v", err)
	}
}

func TestOldPartialDownloadsAreRemoved(t *testing.T) {
	dir := t.TempDir()
	old, young := filepath.Join(dir, ".download-old"), filepath.Join(dir, ".download-young")
	for _, p := range []string{old, young} {
		if err := os.WriteFile(p, []byte("partial"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	past := time.Now().Add(-2 * staticTimeout)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}
	removeAbandonedDownloads(dir, time.Now())
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Errorf("abandoned download kept: %v", err)
	}
	if _, err := os.Stat(young); err != nil {
		t.Errorf("a download that may still be running was removed: %v", err)
	}
}

// testZip zips the recorded New York schedule tables.
func testZip(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, name := range []string{"routes.txt", "stops.txt", "trips.txt"} {
		b, err := os.ReadFile(filepath.Join("testdata", "mta", name))
		if err != nil {
			t.Fatal(err)
		}
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(b); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
