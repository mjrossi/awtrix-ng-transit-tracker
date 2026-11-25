package providers_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/mjrossi/urbanist-timetable/pkg/transit/providers"
	"github.com/mjrossi/urbanist-timetable/pkg/transit/registry"
)

func TestOpen(t *testing.T) {
	if _, err := providers.Open("bart", map[string]any{"system": "sf-bart"}); err != nil {
		t.Errorf("system: %v", err)
	}
	if _, err := providers.Open("agency", map[string]any{"type": "gtfsrt", "realtime_urls": []any{"https://x"}}, providers.WithScheduleCache(t.TempDir())); err != nil {
		t.Errorf("gtfsrt: %v", err)
	}
	if _, err := providers.Open("nope", nil); !errors.Is(err, registry.ErrUnknownProvider) {
		t.Errorf("unknown type: err = %v", err)
	}
	_, err := providers.Open("carrismetropolitana", map[string]any{"clientsecret": "SUPERSECRET"})
	if err == nil || !strings.Contains(err.Error(), "clientsecret") || strings.Contains(err.Error(), "SUPERSECRET") {
		t.Errorf("unknown setting: err = %v", err)
	}
}
