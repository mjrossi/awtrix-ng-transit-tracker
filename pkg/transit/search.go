package transit

import "strings"

// MatchesSearch reports whether every word of search appears in name,
// ignoring case and the accents common in Portuguese and Spanish names.
// An empty search matches nothing.
func MatchesSearch(name, search string) bool {
	words := strings.Fields(foldAccents(search))
	if len(words) == 0 {
		return false
	}
	n := foldAccents(name)
	for _, w := range words {
		if !strings.Contains(n, w) {
			return false
		}
	}
	return true
}

func foldAccents(s string) string { return accents.Replace(strings.ToLower(s)) }

// accents covers Latin-1 vowels and cedillas; the standard library has no
// Unicode normalization, and golang.org/x/text is not an approved module.
var accents = strings.NewReplacer(
	"á", "a", "à", "a", "â", "a", "ã", "a", "ä", "a",
	"é", "e", "è", "e", "ê", "e", "ë", "e",
	"í", "i", "ì", "i", "î", "i", "ï", "i",
	"ó", "o", "ò", "o", "ô", "o", "õ", "o", "ö", "o",
	"ú", "u", "ù", "u", "û", "u", "ü", "u",
	"ç", "c", "ñ", "n",
)
