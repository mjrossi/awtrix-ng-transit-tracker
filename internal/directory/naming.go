package directory

import (
	"cmp"
	"regexp"
	"slices"
	"strings"

	"github.com/mjrossi/urbanist-timetable/internal/fold"
	"github.com/mjrossi/urbanist-timetable/pkg/transit/catalog"
)

var acronym = regexp.MustCompile(`\(([A-Za-z][A-Za-z0-9 .\-]{1,11})\)`)

// agencyShort is an agency's acronym in parentheses, as the list often
// gives one, or its first two words. Key variables and block names are
// made from it, so a block and its key read alike.
func agencyShort(agency string) string {
	if m := acronym.FindStringSubmatch(agency); m != nil {
		return m[1]
	}
	words := strings.Fields(agency)
	return strings.Join(words[:min(len(words), 2)], " ")
}

// BlockName names the system's [providers] block after its agency, as its
// key variable is, or after its ID when the agency's name has no Latin
// letters or digits.
func (s System) BlockName() string {
	return cmp.Or(fold.Slug(s.shortName(), '-', 32), s.ID)
}

// shortName is what blocks and key variables are named after: the agency's
// short form, or the place where the agency is a list of several, as a
// regional feed's often is, since its first entry is no more the feed's
// than the others.
func (s System) shortName() string {
	if isAgencyList(s.Agency) {
		if city, _, _ := strings.Cut(s.Place, ","); city != "" {
			return city
		}
	}
	return agencyShort(s.Agency)
}

// isAgencyList reports whether agency names three or more agencies, as
// "A Oy, B Ab, C Oy" does. Commas before a company form, as in
// "X Authority, Inc., dba Y", are part of one name.
func isAgencyList(agency string) bool {
	n := 0
	for part := range strings.SplitSeq(agency, ",") {
		if !companyForm[strings.ToLower(strings.Trim(part, " ."))] {
			n++
		}
	}
	return n >= 3
}

var companyForm = map[string]bool{
	"inc": true, "llc": true, "ltd": true, "co": true, "corp": true, "lp": true, "plc": true,
	"s.a": true, "sa": true, "gmbh": true, "ag": true, "oy": true, "ab": true, "bv": true, "srl": true,
}

// keyVar names the environment variable for a system's key, from its
// shortName. Names this tool or its catalog already use get the system's
// ID instead, so a directory entry cannot be handed another service's key.
func keyVar(s System) string {
	id := s.ID
	name := strings.ToUpper(fold.Slug(s.shortName(), '_', 0))
	v := name + "_API_KEY"
	if name == "" || reservedVar(v) {
		v = strings.ToUpper(strings.ReplaceAll(id, "-", "_")) + "_API_KEY"
	}
	return v
}

// reservedVar reports whether v is a variable this tool reads for itself
// or for a catalog system.
func reservedVar(v string) bool {
	if v == TransitlandKeyVar || strings.HasPrefix(v, "TIMETABLE_") || strings.HasPrefix(v, "AWTRIX_") {
		return true
	}
	all, _ := catalog.All()
	for _, s := range all {
		for _, o := range s.RequiredOptions {
			if s.EnvName(o) == v {
				return true
			}
		}
	}
	return false
}

func joinNonEmpty(sep string, ss ...string) string {
	return strings.Join(slices.DeleteFunc(ss, func(s string) bool { return s == "" }), sep)
}
