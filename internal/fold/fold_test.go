package fold_test

import (
	"testing"

	"github.com/mjrossi/urbanist-timetable/internal/fold"
)

func TestString(t *testing.T) {
	for in, want := range map[string]string{
		"Marquês de Pombal": "marques de pombal",
		"SØNDRE":            "sondre",
		"Ångström":          "angstrom",
		"Çais do Sodré":     "cais do sodre",
		"Plain":             "plain",
		"Łódź":              "lodz",
		"Brno–Černovice":    "brno–cernovice",
		"Győr":              "gyor",
		"Straße":            "strasse",
	} {
		if got := fold.String(in); got != want {
			t.Errorf("String(%q) = %q, want %q", in, got, want)
		}
	}
}
