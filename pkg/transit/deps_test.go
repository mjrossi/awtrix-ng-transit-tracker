package transit_test

import (
	"os/exec"
	"strings"
	"testing"
)

// The core is what every importer pulls in, so it stays standard library
// only.
func TestCoreHasNoDependencies(t *testing.T) {
	out, err := exec.CommandContext(t.Context(), "go", "list", "-deps", ".").Output()
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range strings.Fields(string(out)) {
		first, _, _ := strings.Cut(path, "/")
		if strings.Contains(first, ".") && !strings.HasPrefix(path, "github.com/mjrossi/urbanist-timetable/") {
			t.Errorf("pkg/transit depends on %s", path)
		}
	}
}
