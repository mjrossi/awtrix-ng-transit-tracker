// Package tomlout writes TOML values for files the tool generates, such as
// `config init` output and `systems show` blocks, whose strings may come
// from directories the user did not write.
package tomlout

import (
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"unicode"
)

// String quotes s as a TOML basic string, escaping control characters.
func String(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case unicode.IsControl(r):
			fmt.Fprintf(&b, "\\u%04X", r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// Key quotes a key that is not a bare TOML key, such as one with "~".
func Key(k string) string {
	if k == "" {
		return String(k)
	}
	for _, r := range k {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '-' && r != '_' {
			return String(k)
		}
	}
	return k
}

// Value writes a string, a bool, or a list or table of them inline.
func Value(v any) (string, error) {
	switch x := v.(type) {
	case string:
		return String(x), nil
	case bool:
		return strconv.FormatBool(x), nil
	case []any:
		parts := make([]string, len(x))
		for i, e := range x {
			s, err := Value(e)
			if err != nil {
				return "", err
			}
			parts[i] = s
		}
		return "[" + strings.Join(parts, ", ") + "]", nil
	case map[string]any:
		var parts []string
		for _, k := range slices.Sorted(maps.Keys(x)) {
			s, err := Value(x[k])
			if err != nil {
				return "", err
			}
			parts = append(parts, Key(k)+" = "+s)
		}
		return "{ " + strings.Join(parts, ", ") + " }", nil
	}
	return "", fmt.Errorf("cannot write %T as TOML", v)
}
