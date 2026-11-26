package transit_test

import (
	"testing"

	"github.com/mjrossi/urbanist-timetable/pkg/transit"
)

func TestMatchesSearch(t *testing.T) {
	tests := []struct {
		name, search string
		want         bool
	}{
		{"Cais do Sodré (Metro)", "cais sodre", true},
		{"Cais do Sodré (Metro)", "SODRÉ metro", true},
		{"São Sebastião", "sao sebastiao", true},
		{"Baixa/Chiado", "chiado baixa", true},
		{"Baixa/Chiado", "chiado rossio", false},
		{"Rautatientori", "tori", true},
		{"King's Cross St. Pancras", "kings cross", true},
		{"Alameda", "", false},
		{"Alameda", "   ", false},
	}
	for _, tt := range tests {
		if got := transit.MatchesSearch(tt.name, tt.search); got != tt.want {
			t.Errorf("MatchesSearch(%q, %q) = %v, want %v", tt.name, tt.search, got, tt.want)
		}
	}
}
