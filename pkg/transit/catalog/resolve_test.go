package catalog_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/mjrossi/urbanist-timetable/pkg/transit/catalog"
)

func TestOptionsDecodeStrictly(t *testing.T) {
	var v struct {
		Lines []struct {
			ID string `toml:"id"`
		} `toml:"lines"`
	}
	opts := catalog.Options{"lines": []any{map[string]any{"id": "a", "colr": "x"}}}
	if err := opts.Decode(&v); err == nil {
		t.Error("unknown provider key accepted")
	}
	opts = catalog.Options{"clientsecret": "SUPERSECRET"}
	err := opts.Decode(&v)
	if err == nil || strings.Contains(err.Error(), "SUPERSECRET") || !strings.Contains(err.Error(), "clientsecret") {
		t.Errorf("err = %v; want the key named and its value left out", err)
	}
}

func TestResolve(t *testing.T) {
	tests := map[string]struct {
		name     string
		settings map[string]any
		wantType string
		wantKeys []string
	}{
		"type key":     {"nyc", map[string]any{"type": "gtfsrt", "realtime_urls": []any{"https://x"}}, "gtfsrt", []string{"realtime_urls"}},
		"name is type": {"demo", map[string]any{}, "demo", nil},
		"system":       {"nyc", map[string]any{"system": "nyc-subway", "headers": map[string]any{"X-Test": "1"}}, "gtfsrt", []string{"headers", "realtime_urls", "static_url"}},
		"nil settings": {"carrismetropolitana", nil, "carrismetropolitana", nil},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			typ, opts, err := catalog.Resolve(tt.name, tt.settings)
			if err != nil {
				t.Fatal(err)
			}
			keys := slices.Sorted(func(yield func(string) bool) {
				for k := range opts {
					if !yield(k) {
						return
					}
				}
			})
			if typ != tt.wantType || !slices.Equal(keys, tt.wantKeys) {
				t.Errorf("Resolve = %q %v, want %q %v", typ, keys, tt.wantType, tt.wantKeys)
			}
		})
	}
	if _, _, err := catalog.Resolve("x", map[string]any{"system": "sf-bart", "type": "gtfsrt"}); err == nil || !strings.Contains(err.Error(), "not both") {
		t.Errorf("system and type: err = %v", err)
	}
	if _, _, err := catalog.Resolve("x", map[string]any{"type": 3}); err == nil {
		t.Error("non-string type accepted")
	}
}

func TestResolveErrorsAreTyped(t *testing.T) {
	if _, _, err := catalog.Resolve("x", map[string]any{"system": "atlantis"}); !errors.Is(err, catalog.ErrUnknownSystem) {
		t.Errorf("err = %v, want ErrUnknownSystem", err)
	}
	_, _, err := catalog.Resolve("x", map[string]any{"system": "lisbon-metro"})
	var missing *catalog.MissingOptionsError
	if !errors.As(err, &missing) || missing.System != "lisbon-metro" || !slices.Equal(missing.Options, []string{"client_id", "client_secret"}) {
		t.Errorf("err = %v, want both missing options", err)
	}
	if err == nil || !strings.Contains(err.Error(), "client_id and client_secret") {
		t.Errorf("message = %v", err)
	}
}

func TestEnvName(t *testing.T) {
	s, _ := catalog.Get("lisbon-metro")
	if got := s.EnvName("client_id"); got != "LISBON_METRO_CLIENT_ID" {
		t.Errorf("EnvName = %q", got)
	}
}
