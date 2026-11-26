package main

import "strings"

// closest returns the candidate most like name, or "" when none is close
// enough to be a likely typo.
func closest(name string, candidates []string) string {
	name = strings.ToLower(name)
	best, bestDist := "", -1
	for _, c := range candidates {
		d := distance(name, strings.ToLower(c))
		if d < bestDist || bestDist < 0 {
			best, bestDist = c, d
		}
	}
	if bestDist < 0 || bestDist > max(2, len(name)/3) {
		return ""
	}
	return best
}

// distance is the Levenshtein edit distance between a and b, by rune.
func distance(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(rb)]
}
