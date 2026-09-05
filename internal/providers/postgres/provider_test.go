package postgres

import "testing"

// A dotted PG version (major.minor) is already full and must be returned
// as-is without any repository traversal — "17.11" used to fall through to
// series resolution because full-version detection counted two dots
// (a MySQL-shaped assumption).
func TestResolveVersionFullPassThrough(t *testing.T) {
	for _, v := range []string{"17.11", "16.9", "12.22"} {
		got, err := Provider{}.ResolveVersion(v, "")
		if err != nil {
			t.Fatalf("ResolveVersion(%q): %v", v, err)
		}
		if got != v {
			t.Errorf("ResolveVersion(%q) = %q, want unchanged", v, got)
		}
	}
}
