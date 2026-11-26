package config_test

import (
	"os"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/mjrossi/urbanist-timetable/internal/config"
)

func TestExampleMatchesTheRepositoryCopy(t *testing.T) {
	root, err := os.ReadFile("../../config.example.toml")
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(string(root), config.Example()); diff != "" {
		t.Errorf("internal/config/example.toml differs from config.example.toml; copy it (-root +embedded):\n%s", diff)
	}
}

func TestExampleIsValid(t *testing.T) {
	lookup := func(string) (string, bool) { return "x", true }
	if _, err := config.Parse([]byte(config.Example()), lookup); err != nil {
		t.Error(err)
	}
}
