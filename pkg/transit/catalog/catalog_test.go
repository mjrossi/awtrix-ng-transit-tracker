package catalog_test

import (
	"flag"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/mjrossi/awtrix-ng-transit-tracker/internal/config"
	"github.com/mjrossi/awtrix-ng-transit-tracker/pkg/transit"
	"github.com/mjrossi/awtrix-ng-transit-tracker/pkg/transit/catalog"
	"github.com/mjrossi/awtrix-ng-transit-tracker/pkg/transit/providers"
)

var live = flag.Bool("live", false, "check every system against its live feeds (just catalog-check)")

// TestSystemsBuild checks every entry offline: it parses, names a
// registered provider, and its options decode, with dummy values for
// required settings such as API keys.
func TestSystemsBuild(t *testing.T) {
	all, err := catalog.All()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) == 0 {
		t.Fatal("empty catalog")
	}
	reg := providers.Registry()
	for _, s := range all {
		t.Run(s.ID, func(t *testing.T) {
			if _, err := time.Parse(time.DateOnly, s.Verified); err != nil {
				t.Errorf("verified %q: %v", s.Verified, err)
			}
			if !strings.HasPrefix(s.InfoURL, "https://") {
				t.Errorf("info_url %q should be an https URL", s.InfoURL)
			}
			user := map[string]any{}
			for _, k := range s.RequiredOptions {
				user[k] = "dummy"
			}
			if _, err := reg.New(s.Provider, config.ProviderOptions(s.Merge(user))); err != nil {
				t.Errorf("build: %v", err)
			}
		})
	}
}

func TestGetAndMerge(t *testing.T) {
	s, ok := catalog.Get("lisbon-metro")
	if !ok {
		t.Fatal("lisbon-metro missing")
	}
	if got := s.Missing(s.Merge(nil)); len(got) != 2 {
		t.Errorf("Missing = %v, want client_id and client_secret", got)
	}
	if got := s.Missing(s.Merge(map[string]any{"client_id": "a", "client_secret": "b"})); len(got) != 0 {
		t.Errorf("Missing = %v after setting both", got)
	}
	if _, ok := catalog.Get("atlantis-metro"); ok {
		t.Error("Get found a system that does not exist")
	}
}

// TestLive fetches departures for every system's sample stop. It is off by
// default because tests never call live APIs; run it with `just
// catalog-check` before updating an entry's verified date. Systems that
// need keys read them from the environment as <ID>_<OPTION>, such as
// LISBON_METRO_CLIENT_ID, and are skipped without them.
func TestLive(t *testing.T) {
	if !*live {
		t.Skip("live check; run with -live")
	}
	all, err := catalog.All()
	if err != nil {
		t.Fatal(err)
	}
	reg := providers.Registry()
	for _, s := range all {
		t.Run(s.ID, func(t *testing.T) {
			user := map[string]any{}
			for _, k := range s.RequiredOptions {
				env := strings.ToUpper(strings.ReplaceAll(s.ID+"_"+k, "-", "_"))
				v := os.Getenv(env)
				if v == "" {
					t.Skipf("set %s to check this system", env)
				}
				user[k] = v
			}
			p, err := reg.New(s.Provider, config.ProviderOptions(s.Merge(user)))
			if err != nil {
				t.Fatal(err)
			}
			deps, err := p.Departures(t.Context(), transit.Query{StopID: s.SampleStop, Now: time.Now(), Window: time.Hour})
			if err != nil {
				t.Fatal(err)
			}
			if len(deps) == 0 {
				t.Errorf("no departures at %s in the next hour", s.SampleStop)
			}
			t.Logf("%d departures at %s", len(deps), s.SampleStop)
		})
	}
}
