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
		"King’s Cross":      "kings cross",
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

func TestSlug(t *testing.T) {
	tests := []struct {
		in    string
		sep   byte
		limit int
		want  string
	}{
		{"Times Sq-42 St", '-', 0, "times-sq-42-st"},
		{"  Marquês de Pombal!  ", '-', 0, "marques-de-pombal"},
		{"Metro de Lisboa", '_', 0, "metro_de_lisboa"},
		{"aaaa bbbb", '-', 5, "aaaa"},
		{"東京メトロ", '-', 0, ""},
		{"King's Cross", '-', 0, "kings-cross"},
	}
	for _, tt := range tests {
		if got := fold.Slug(tt.in, tt.sep, tt.limit); got != tt.want {
			t.Errorf("Slug(%q, %q, %d) = %q, want %q", tt.in, tt.sep, tt.limit, got, tt.want)
		}
	}
}
