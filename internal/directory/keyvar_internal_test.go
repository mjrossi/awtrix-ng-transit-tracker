package directory

import "testing"

func TestKeyVar(t *testing.T) {
	tests := map[string]struct{ agency, id, want string }{
		"acronym":           {"Rural Community Transportation (RCT)", "mdb-448", "RCT_API_KEY"},
		"first two words":   {"Caltrain Peninsula Corridor", "mdb-54", "CALTRAIN_PENINSULA_API_KEY"},
		"no letters":        {"東京都交通局", "mdb-1", "MDB_1_API_KEY"},
		"this tool's own":   {"Transitland", "mdb-2", "MDB_2_API_KEY"},
		"the clock's":       {"Awtrix (AWTRIX)", "mdb-3", "MDB_3_API_KEY"},
		"a reserved prefix": {"Timetable Co", "mdb-4", "MDB_4_API_KEY"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if got := keyVar(tt.agency, tt.id); got != tt.want {
				t.Errorf("keyVar(%q) = %q, want %q", tt.agency, got, tt.want)
			}
		})
	}
}

func TestStartsWord(t *testing.T) {
	for text, want := range map[string]bool{
		"lisboa–metro":  true, // en dash
		"lisboa·metro":  true, // middle dot
		"metropolitano": true, // the word's start
		"sfmetro":       false,
		"lisbõametro":   false, // a letter, though not ASCII
	} {
		if got := startsWord(text, "metro"); got != want {
			t.Errorf("startsWord(%q, metro) = %v, want %v", text, got, want)
		}
	}
}
