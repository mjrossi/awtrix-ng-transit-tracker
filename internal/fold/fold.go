// Package fold reduces text to lowercase unaccented letters, so stop search
// and board names treat "Marquês" and "marques" alike.
package fold

import "strings"

// String lowercases s, replaces common accented Latin letters with their
// base letter, and drops apostrophes, so "kings cross" finds "King's Cross".
func String(s string) string { return accents.Replace(strings.ToLower(s)) }

// Slug folds s and keeps its ASCII letters and digits, with one sep in
// place of each run of anything else, at most limit bytes long when limit is
// above zero. It is empty when s has no such letters or digits.
func Slug(s string, sep byte, limit int) string {
	var b strings.Builder
	pending := false
	for _, r := range String(s) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			if pending {
				b.WriteByte(sep)
				pending = false
			}
			b.WriteRune(r)
		} else if b.Len() > 0 {
			pending = true
		}
	}
	out := b.String()
	if limit > 0 && len(out) > limit {
		out = strings.TrimRight(out[:limit], string(sep))
	}
	return out
}

// accents covers common Latin accents; the standard library has no Unicode
// normalization, and golang.org/x/text is not an approved module.
var accents = strings.NewReplacer(
	"á", "a", "à", "a", "â", "a", "ã", "a", "ä", "a", "å", "a",
	"é", "e", "è", "e", "ê", "e", "ë", "e",
	"í", "i", "ì", "i", "î", "i", "ï", "i",
	"ó", "o", "ò", "o", "ô", "o", "õ", "o", "ö", "o", "ø", "o",
	"ú", "u", "ù", "u", "û", "u", "ü", "u",
	"ý", "y", "ÿ", "y",
	"ç", "c", "ñ", "n",
	"ā", "a", "ă", "a", "ą", "a", "ć", "c", "č", "c", "ď", "d", "đ", "d",
	"ē", "e", "ė", "e", "ę", "e", "ě", "e", "ğ", "g", "ī", "i", "į", "i",
	"ı", "i", "ķ", "k", "ĺ", "l", "ľ", "l", "ł", "l", "ļ", "l", "ń", "n",
	"ň", "n", "ņ", "n", "ő", "o", "ō", "o", "ŕ", "r", "ř", "r", "ś", "s",
	"š", "s", "ş", "s", "ș", "s", "ţ", "t", "ț", "t", "ť", "t", "ū", "u",
	"ů", "u", "ű", "u", "ų", "u", "ź", "z", "ż", "z", "ž", "z",
	"æ", "ae", "œ", "oe", "ß", "ss", "þ", "th", "ð", "d",
	"'", "", "’", "", "ʼ", "",
)
