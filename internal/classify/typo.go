package classify

import "strings"

// suggest returns the known command that word is probably a typo of. exact
// is true when word itself is a known command name (it is simply not
// installed), in which case there is nothing to correct.
func suggest(word string, extra []string) (sugg string, exact bool) {
	w := strings.ToLower(word)
	if len(w) < minTypoLen || !isASCIIName(w) ||
		promptStarters[w] || strongStopWords[w] || weakStopWords[w] {
		return "", false
	}
	candidates := func(yield func(string) bool) {
		for _, c := range knownCommands {
			if !yield(c) {
				return
			}
		}
		for _, c := range extra {
			if !yield(c) {
				return
			}
		}
	}
	for c := range candidates {
		if c == w {
			return c, true
		}
	}
	for c := range candidates {
		if len(w) <= transpositionOnly {
			if isTransposition(w, c) {
				return c, false
			}
			continue
		}
		if osaDistance(w, c) == 1 {
			return c, false
		}
	}
	return "", false
}

func isASCIIName(s string) bool {
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-', c == '_':
		default:
			return false
		}
	}
	return true
}

// isTransposition reports whether b is a with two adjacent letters swapped.
func isTransposition(a, b string) bool {
	if len(a) != len(b) || a == b {
		return false
	}
	i := 0
	for i < len(a) && a[i] == b[i] {
		i++
	}
	return i+1 < len(a) && a[i] == b[i+1] && a[i+1] == b[i] && a[i+2:] == b[i+2:]
}

// osaDistance is the optimal string alignment variant of the
// Damerau–Levenshtein distance (insertions, deletions, substitutions and
// transpositions of adjacent characters) over bytes. It returns early with 2
// when the lengths differ by more than one, since callers only care about <= 1.
func osaDistance(a, b string) int {
	if d := len(a) - len(b); d > 1 || d < -1 {
		return 2
	}
	// Three rolling rows: i-2, i-1, i.
	prev2 := make([]int, len(b)+1)
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
			if i > 1 && j > 1 && a[i-1] == b[j-2] && a[i-2] == b[j-1] {
				cur[j] = min(cur[j], prev2[j-2]+1)
			}
		}
		prev2, prev, cur = prev, cur, prev2
	}
	return prev[len(b)]
}
