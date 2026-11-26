// Package fold reduces text to lowercase unaccented letters, so stop search
// and board names treat "Marquês" and "marques" alike.
package fold

import "strings"

// String lowercases s and replaces common accented Latin letters with their
// base letter.
func String(s string) string { return accents.Replace(strings.ToLower(s)) }

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
)
