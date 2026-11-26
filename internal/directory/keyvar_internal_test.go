package directory

import (
	"testing"

	"github.com/google/go-cmp/cmp"
)

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
			if got := keyVar(System{Agency: tt.agency, ID: tt.id}); got != tt.want {
				t.Errorf("keyVar(%q) = %q, want %q", tt.agency, got, tt.want)
			}
		})
	}
}

func TestBlockNameForAListOfAgencies(t *testing.T) {
	sys := System{ID: "tld-832", Agency: "Kuopion Tila-Auto Oy, Oy Pohjolan liikenne Ab, Savonlinja Oy", Place: "Kuopio, North Savo, FI"}
	if b, k := sys.BlockName(), keyVar(sys); b != "kuopio" || k != "KUOPIO_API_KEY" {
		t.Errorf("block %q, key %q; want them named after the place", b, k)
	}
	one := System{ID: "mdb-1", Agency: "Transdev, Inc.", Place: "Boston, MA, US"}
	if b := one.BlockName(); b != "transdev-inc" {
		t.Errorf("block %q; one comma is part of a name, not a list", b)
	}
	for _, agency := range []string{"Metro Authority, Inc., dba MetroBus", "Coach Lines, Ltd., LLC"} {
		if isAgencyList(agency) {
			t.Errorf("%q read as a list; its commas come before company forms", agency)
		}
	}
}

func TestBlockName(t *testing.T) {
	for agency, want := range map[string]string{
		"Bay Area Rapid Transit (BART)":       "bart",
		"Caltrain":                            "caltrain",
		"Metro-North Railroad":                "metro-north-railroad",
		"Companhia Carris de Ferro de Lisboa": "companhia-carris",
		"Helsingin seudun liikenne (HSL)":     "hsl",
		"Transtejo Soflusa (TTSL)":            "ttsl",
		"SĮ 'Susisiekimo paslaugos'":          "si-susisiekimo",
		"Αστικές Συγκοινωνίες Αθηνών":         "mdb-1",
		"東京都交通局":                              "mdb-1",
		"":                                    "mdb-1",
	} {
		if got := (System{ID: "mdb-1", Agency: agency}).BlockName(); got != want {
			t.Errorf("BlockName(%q) = %q, want %q", agency, got, want)
		}
	}
}

// A block and its key variable are named from the same words.
func TestBlockAndKeyAgree(t *testing.T) {
	sys := System{ID: "mdb-9", Agency: "Metropolitan Transportation Authority (MTA)"}
	if b, k := sys.BlockName(), keyVar(sys); b != "mta" || k != "MTA_API_KEY" {
		t.Errorf("block %q, key %q", b, k)
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

func TestNamed(t *testing.T) {
	tests := []struct {
		agency, name, words string
		want                bool
	}{
		{"Metropolitan Transit Authority (MTA)", "Long Island Rail Road", "mta", true},
		{"Metropolitan Transit Authority (MTA)", "Long Island Rail Road", "long island rail road", true},
		{"Nashville MTA WeGo Public Transit", "", "mta", false},
		{"San Francisco Municipal Transportation Agency (SFMTA - Muni)", "", "muni", true},
		{"RATP (RER)", "", "ratp", true},
		{"RATP Cap Arc Sud et Ouest", "", "ratp", false},
		{"Carris", "", "CARRIS", true},
	}
	for _, tt := range tests {
		if got := named(System{Agency: tt.agency, Name: tt.name}, tt.words); got != tt.want {
			t.Errorf("named(%q, %q, %q) = %v, want %v", tt.agency, tt.name, tt.words, got, tt.want)
		}
	}
}

func TestLiveSchedules(t *testing.T) {
	feeds := func(specs ...string) []tlFeedRef {
		var out []tlFeedRef
		for i := 0; i < len(specs); i += 2 {
			out = append(out, tlFeedRef{OnestopID: specs[i], Spec: specs[i+1]})
		}
		return out
	}
	tests := map[string]struct {
		feeds []tlFeedRef
		want  map[string]bool
	}{
		"by prefix, the longest": {feeds("f-ttc", "GTFS", "f-ttc~surface", "GTFS", "f-ttc~surface~rt", "GTFS_RT"), map[string]bool{"f-ttc~surface": true}},
		"the only schedule":      {feeds("f-dp3-cta", "GTFS", "f-cta~rt", "GTFS_RT"), map[string]bool{"f-dp3-cta": true}},
		"unassigned":             {feeds("f-a", "GTFS", "f-b", "GTFS", "f-c~rt", "GTFS_RT"), map[string]bool{}},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if diff := cmp.Diff(tt.want, tlOperator{Feeds: tt.feeds}.liveSchedules()); diff != "" {
				t.Errorf("(-want +got):\n%s", diff)
			}
		})
	}
}
