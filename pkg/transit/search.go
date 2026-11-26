package transit

import (
	"strings"

	"github.com/mjrossi/urbanist-timetable/internal/fold"
)

// MatchesSearch reports whether every word of search appears in name,
// ignoring case and common Latin accents. A word may match inside a word of
// name, so "bahnhof" finds "Hauptbahnhof". An empty search matches nothing.
func MatchesSearch(name, search string) bool {
	words := strings.Fields(fold.String(search))
	if len(words) == 0 {
		return false
	}
	n := fold.String(name)
	for _, w := range words {
		if !strings.Contains(n, w) {
			return false
		}
	}
	return true
}
