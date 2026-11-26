package tomlout_test

import (
	"testing"

	"github.com/pelletier/go-toml/v2"

	"github.com/mjrossi/urbanist-timetable/internal/tomlout"
)

func TestValueRoundTrips(t *testing.T) {
	v := map[string]any{
		"plain":   "x",
		"quoted":  "a \"b\" \\ c",
		"control": "line\nbreak\x1b[31m",
		"a~b":     []any{"1", "2"},
		"flag":    false,
	}
	s, err := tomlout.Value(v)
	if err != nil {
		t.Fatal(err)
	}
	var got struct{ V map[string]any }
	if err := toml.Unmarshal([]byte("V = "+s), &got); err != nil {
		t.Fatalf("%s: %v", s, err)
	}
	for k, want := range v {
		if s, ok := want.(string); ok && got.V[k] != s {
			t.Errorf("%s = %q, want %q", k, got.V[k], s)
		}
	}
	if b, ok := got.V["flag"].(bool); !ok || b {
		t.Errorf("flag = %v, want false", got.V["flag"])
	}
	if _, err := tomlout.Value(3); err == nil {
		t.Error("an int written without error")
	}
}
