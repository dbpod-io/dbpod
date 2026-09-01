package cmd

import (
	"reflect"
	"testing"
)

// fixture builds a small version universe mocking different installed
// states. It mirrors the real shape: classic series (5.7...9.7), calendar
// innovation releases (26.7.0) and LTS releases.
func fixture() []lsEntry {
	return []lsEntry{
		{Version: "26.7.0"},            // calendar, non-LTS (innovation)
		{Version: "9.7.2", LTS: true},  // LTS
		{Version: "9.7.1"},             // non-LTS
		{Version: "8.4.11", LTS: true}, // LTS
		{Version: "8.4.6"},             // non-LTS
		{Version: "8.0.46"},            // non-LTS
		{Version: "8.0.40"},            // non-LTS
		{Version: "5.7.44"},            // non-LTS
	}
}

func labels(rows []lsRow) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.Label
	}
	return out
}

// withInstalled mocks installed versions onto the fixture.
func withInstalled(t *testing.T, versions ...string) []lsEntry {
	t.Helper()
	set := map[string]bool{}
	for _, v := range versions {
		set[v] = true
	}
	entries := fixture()
	for i := range entries {
		entries[i].Installed = set[entries[i].Version]
	}
	return entries
}

func TestEngineLsDefaultInstalledBranch(t *testing.T) {
	// 8.0.46 installed and equal to branch latest: dedup keeps only the
	// branch entry — the plain version row must NOT appear
	entries := withInstalled(t, "8.0.46")
	rows := buildLsRows(entries, true, false, false, true)

	want := []string{
		"innovation (26.7.0)",
		"9.7 (9.7.2)",
		"8.4 (8.4.11)",
		"8.0 (8.0.46)",
		"5.7 (5.7.44)",
	}
	if !reflect.DeepEqual(labels(rows), want) {
		t.Fatalf("rows = %v, want %v", labels(rows), want)
	}
	for _, r := range rows {
		if r.Label == "8.0 (8.0.46)" && !r.Installed {
			t.Error("8.0 branch should carry installed state of its latest")
		}
	}
}

func TestEngineLsInstalledNotBranchLatest(t *testing.T) {
	// 8.0.40 installed but the 8.0 branch latest is 8.0.46: the branch
	// stands for 8.0.46 (not installed) and 8.0.40 appears as its own row
	entries := withInstalled(t, "8.0.40")
	rows := buildLsRows(entries, true, false, false, true)

	want := []string{
		"innovation (26.7.0)",
		"9.7 (9.7.2)",
		"8.4 (8.4.11)",
		"8.0 (8.0.46)",
		"8.0.40",
		"5.7 (5.7.44)",
	}
	if !reflect.DeepEqual(labels(rows), want) {
		t.Fatalf("rows = %v, want %v", labels(rows), want)
	}
	for _, r := range rows {
		switch r.Label {
		case "8.0 (8.0.46)":
			if r.Installed {
				t.Error("8.0 branch must not be installed: its latest 8.0.46 is not")
			}
		case "8.0.40":
			if !r.Installed {
				t.Error("8.0.40 should be installed")
			}
		}
	}
}

func TestEngineLsInstalledOnly(t *testing.T) {
	entries := withInstalled(t, "8.0.46", "5.7.44")
	rows := buildLsRows(entries, true, false, false, false)

	want := []string{"8.0.46", "5.7.44"} // newest first
	if !reflect.DeepEqual(labels(rows), want) {
		t.Fatalf("rows = %v, want %v", labels(rows), want)
	}
	for _, r := range rows {
		if !r.Installed {
			t.Errorf("%s should be installed", r.Label)
		}
	}
}

func TestEngineLsLtsOnly(t *testing.T) {
	rows := buildLsRows(fixture(), false, false, true, false)
	want := []string{"9.7.2", "8.4.11"} // newest first
	if !reflect.DeepEqual(labels(rows), want) {
		t.Fatalf("rows = %v, want %v", labels(rows), want)
	}
	for _, r := range rows {
		if !r.LTS {
			t.Errorf("%s should be LTS", r.Label)
		}
	}
}

func TestEngineLsAllOnly(t *testing.T) {
	rows := buildLsRows(fixture(), false, true, false, false)
	want := []string{
		"26.7.0", "9.7.2", "9.7.1", "8.4.11", "8.4.6", "8.0.46", "8.0.40", "5.7.44",
	}
	if !reflect.DeepEqual(labels(rows), want) {
		t.Fatalf("rows = %v, want %v", labels(rows), want)
	}
}

func TestEngineLsUnionInstalledLts(t *testing.T) {
	// union: installed (8.0.46) ∪ LTS (9.7.2, 8.4.11)
	entries := withInstalled(t, "8.0.46")
	rows := buildLsRows(entries, true, false, true, false)
	want := []string{"9.7.2", "8.4.11", "8.0.46"}
	if !reflect.DeepEqual(labels(rows), want) {
		t.Fatalf("rows = %v, want %v", labels(rows), want)
	}
}

func TestEngineLsBranchOnly(t *testing.T) {
	rows := buildLsRows(fixture(), false, false, false, true)
	want := []string{
		"innovation (26.7.0)",
		"9.7 (9.7.2)",
		"8.4 (8.4.11)",
		"8.0 (8.0.46)",
		"5.7 (5.7.44)",
	}
	if !reflect.DeepEqual(labels(rows), want) {
		t.Fatalf("rows = %v, want %v", labels(rows), want)
	}
	// branch rows are never "installed" unless their latest is
	for _, r := range rows {
		if r.Installed {
			t.Errorf("%s should not be installed (nothing mocked)", r.Label)
		}
	}
}

func TestEngineLsCalendarBranching(t *testing.T) {
	entries := []lsEntry{
		{Version: "27.1.0"},             // calendar non-LTS -> innovation
		{Version: "26.7.0"},             // calendar non-LTS -> innovation
		{Version: "26.10.1", LTS: true}, // calendar LTS -> own branch
		{Version: "8.0.46"},             // classic
	}
	rows := buildLsRows(entries, false, false, false, true)
	want := []string{
		"innovation (27.1.0)", // collapses ALL non-LTS calendar, latest wins
		"26.10 (26.10.1)",     // calendar LTS keeps its own branch
		"8.0 (8.0.46)",
	}
	if !reflect.DeepEqual(labels(rows), want) {
		t.Fatalf("rows = %v, want %v", labels(rows), want)
	}
	for _, r := range rows {
		wantLTS := r.Label == "26.10 (26.10.1)"
		if r.LTS != wantLTS {
			t.Errorf("%s LTS = %v, want %v", r.Label, r.LTS, wantLTS)
		}
	}
}

func TestEngineLsDedupPrefersBranch(t *testing.T) {
	// a version equal to a branch latest is listed once, as the branch,
	// regardless of which filter matched it (8.4.11 is LTS and installed)
	entries := withInstalled(t, "8.4.11")
	rows := buildLsRows(entries, true, false, true, true)

	got := labels(rows)
	want := []string{
		"innovation (26.7.0)",
		"9.7 (9.7.2)",
		"8.4 (8.4.11)", // branch wins over the plain 8.4.11 version row
		"8.0 (8.0.46)",
		"5.7 (5.7.44)",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("rows = %v, want %v", got, want)
	}
	if !rows[2].Installed {
		t.Error("8.4 branch should carry the installed state of 8.4.11")
	}
}

func TestEngineLsFlags(t *testing.T) {
	reset := func() {
		engineLsAll, engineLsInstalled, engineLsLts, engineLsBranch = false, false, false, false
	}
	reset()
	t.Cleanup(reset)

	cases := []struct {
		name                        string
		all, installed, lts, branch bool
		wantI, wantA, wantL, wantB  bool
	}{
		{"default", false, false, false, false, true, false, false, true},
		{"installed", false, true, false, false, true, false, false, false},
		{"branch", false, false, false, true, false, false, false, true},
		{"all+lts", true, false, true, false, false, true, true, false},
		{"installed+branch", false, true, false, true, true, false, false, true},
	}
	for _, c := range cases {
		reset()
		engineLsAll, engineLsInstalled, engineLsLts, engineLsBranch = c.all, c.installed, c.lts, c.branch
		i, a, l, b := engineLsFlags()
		if i != c.wantI || a != c.wantA || l != c.wantL || b != c.wantB {
			t.Errorf("%s: engineLsFlags() = (%v,%v,%v,%v), want (%v,%v,%v,%v)",
				c.name, i, a, l, b, c.wantI, c.wantA, c.wantL, c.wantB)
		}
	}
}

func TestVersionLess(t *testing.T) {
	cases := []struct {
		a, b string
		less bool
	}{
		{"8.0.40", "8.0.46", true},
		{"8.0.46", "8.0.40", false},
		{"8.0.46", "8.4.11", true},
		{"9.7.2", "26.7.0", true},   // calendar majors sort higher
		{"5.7.44", "5.7.9", false},  // numeric, not lexicographic
		{"8.0.46", "8.0.46", false}, // equal is not less
	}
	for _, c := range cases {
		if got := versionLess(c.a, c.b); got != c.less {
			t.Errorf("versionLess(%q, %q) = %v, want %v", c.a, c.b, got, c.less)
		}
	}
}

func TestBranchNameOf(t *testing.T) {
	cases := []struct {
		version string
		lts     bool
		want    string
	}{
		{"8.0.46", false, "8.0"},
		{"5.7.44", false, "5.7"},
		{"9.7.2", true, "9.7"},
		{"26.7.0", false, "innovation"},
		{"26.10.1", true, "26.10"}, // calendar LTS keeps its own branch
	}
	for _, c := range cases {
		if got := branchNameOf(c.version, c.lts); got != c.want {
			t.Errorf("branchNameOf(%q, %v) = %q, want %q", c.version, c.lts, got, c.want)
		}
	}
}
