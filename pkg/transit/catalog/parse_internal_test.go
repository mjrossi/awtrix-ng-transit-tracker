package catalog

import "testing"

func TestParseSystem(t *testing.T) {
	const entry = `
agency = "Town Transit"
provider = "gtfsrt"
status = "community"
sample_stop = "S"
`
	tests := map[string]struct {
		file, body string
		ok         bool
	}{
		"agency entry":                  {"town.toml", entry, true},
		"agency and system":             {"town-tram.toml", entry, true},
		"mobility database lookalike":   {"mdb-53.toml", entry, false},
		"another directory's lookalike": {"tld-716.toml", entry, false},
		"transitland lookalike":         {"f-9q9-town.toml", entry, false},
		"uppercase":                     {"Town.toml", entry, false},
		"no agency": {"town.toml", `
provider = "gtfsrt"
status = "community"
sample_stop = "S"
`, false},
		"unknown status": {"town.toml", `
agency = "Town Transit"
provider = "gtfsrt"
status = "abandoned"
sample_stop = "S"
`, false},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := parseSystem(tt.file, []byte(tt.body))
			if (err == nil) != tt.ok {
				t.Errorf("err = %v, want ok %v", err, tt.ok)
			}
		})
	}
}
