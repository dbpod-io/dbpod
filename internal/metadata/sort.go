package metadata

import (
	"sort"
	"strconv"
	"strings"
)

// sortVersions sorts full version strings newest first (in place).
// Handles optional trailing letters (5.0.16a > 5.0.16) and ignores
// non-numeric entries by pushing them last.
func sortVersions(v []string) {
	sort.Slice(v, func(i, j int) bool { return compareVersions(v[i], v[j]) > 0 })
}

// compareVersions compares two version strings; >0 if a is newer.
func compareVersions(a, b string) int {
	as := strings.Split(strings.ToLower(a), ".")
	bs := strings.Split(strings.ToLower(b), ".")
	for i := 0; i < len(as) || i < len(bs); i++ {
		av, bv := verPart(as, i), verPart(bs, i)
		if c := compareVerComponent(av, bv); c != 0 {
			return c
		}
	}
	return 0
}

func verPart(parts []string, i int) string {
	if i < len(parts) {
		return parts[i]
	}
	return "0"
}

func compareVerComponent(a, b string) int {
	an, bn := numPrefix(a), numPrefix(b)
	if an != bn {
		return an - bn
	}
	return strings.Compare(strings.TrimLeft(a, "0123456789"), strings.TrimLeft(b, "0123456789"))
}

// numPrefix returns the numeric prefix of s (e.g. "46abc" -> 46).
func numPrefix(s string) int {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i == 0 {
		return 0
	}
	n, err := strconv.Atoi(s[:i])
	if err != nil {
		return 0
	}
	return n
}
