package cmd

import (
	"reflect"
	"testing"
)

// fixture builds a small version universe mocking different installed
// states. It mirrors the real shape: classic series (5.7...9.7), calendar
// innovation releases (26.7.0) and LTS releases.
func fixture() []lsEntry {
	entries := []lsEntry{
		{Version: "26.7.0", Series: "innovation"},     // calendar, non-LTS
		{Version: "9.7.2", LTS: true, Series: "9.7"},  // LTS
		{Version: "9.7.1", Series: "9.7"},             // non-LTS
		{Version: "8.4.11", LTS: true, Series: "8.4"}, // LTS
		{Version: "8.4.6", Series: "8.4"},             // non-LTS
		{Version: "8.0.46", Series: "8.0"},            // non-LTS
		{Version: "8.0.40", Series: "8.0"},            // non-LTS
		{Version: "5.7.44", Series: "5.7"},            // non-LTS
	}
	for i := range entries {
		entries[i].Available = true
	}
	return entries
}

// markUnavailable mocks versions without a package for the current platform.
func markUnavailable(entries []lsEntry, versions ...string) []lsEntry {
	set := map[string]bool{}
	for _, v := range versions {
		set[v] = true
	}
	for i := range entries {
		if set[entries[i].Version] {
			entries[i].Available = false
		}
	}
	return entries
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

func TestEngineLsDefaultInstalledSeries(t *testing.T) {
	// 8.0.46 installed and equal to the series latest: dedup keeps only the
	// series entry — the plain version row must NOT appear
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
			t.Error("8.0 series should carry installed state of its latest")
		}
	}
}

func TestEngineLsInstalledNotSeriesLatest(t *testing.T) {
	// 8.0.40 installed but the 8.0 series latest is 8.0.46: the series
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
				t.Error("8.0 series must not be installed: its latest 8.0.46 is not")
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

func TestEngineLsSeriesOnly(t *testing.T) {
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
	// series rows are never "installed" unless their latest is
	for _, r := range rows {
		if r.Installed {
			t.Errorf("%s should not be installed (nothing mocked)", r.Label)
		}
	}
}

func TestEngineLsCalendarSeries(t *testing.T) {
	entries := []lsEntry{
		{Version: "27.1.0", Series: "innovation"},        // calendar non-LTS -> innovation
		{Version: "26.7.0", Series: "innovation"},        // calendar non-LTS -> innovation
		{Version: "26.10.1", LTS: true, Series: "26.10"}, // calendar LTS -> own series
		{Version: "8.0.46", Series: "8.0"},               // classic
	}
	rows := buildLsRows(entries, false, false, false, true)
	want := []string{
		"innovation (27.1.0)", // collapses ALL non-LTS calendar, latest wins
		"26.10 (26.10.1)",     // calendar LTS keeps its own series
		"8.0 (8.0.46)",
	}
	if !reflect.DeepEqual(labels(rows), want) {
		t.Fatalf("rows = %v, want %v", labels(rows), want)
	}
}

func TestEngineLsDedupPrefersSeries(t *testing.T) {
	// a version equal to a series latest is listed once, as the series,
	// regardless of which filter matched it (8.4.11 is LTS and installed)
	entries := withInstalled(t, "8.4.11")
	rows := buildLsRows(entries, true, false, true, true)

	got := labels(rows)
	want := []string{
		"innovation (26.7.0)",
		"9.7 (9.7.2)",
		"8.4 (8.4.11)", // series wins over the plain 8.4.11 version row
		"8.0 (8.0.46)",
		"5.7 (5.7.44)",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("rows = %v, want %v", got, want)
	}
	if !rows[2].Installed {
		t.Error("8.4 series should carry the installed state of 8.4.11")
	}
}

func TestEngineLsFlags(t *testing.T) {
	reset := func() {
		engineLsAll, engineLsInstalled, engineLsLts, engineLsSeries = false, false, false, false
	}
	reset()
	t.Cleanup(reset)

	cases := []struct {
		name                        string
		all, installed, lts, series bool
		wantI, wantA, wantL, wantB  bool
	}{
		{"default", false, false, false, false, true, false, false, true},
		{"installed", false, true, false, false, true, false, false, false},
		{"series", false, false, false, true, false, false, false, true},
		{"all+lts", true, false, true, false, false, true, true, false},
		{"installed+series", false, true, false, true, true, false, false, true},
	}
	for _, c := range cases {
		reset()
		engineLsAll, engineLsInstalled, engineLsLts, engineLsSeries = c.all, c.installed, c.lts, c.series
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

func TestEngineLsUnavailableStatus(t *testing.T) {
	// 5.7.44 has no package for the current platform: listed with an
	// explicit "unavailable" status; 8.0.46 installed stays "installed"
	entries := markUnavailable(withInstalled(t, "8.0.46"), "5.7.44", "9.7.1", "8.4.6")

	rows := buildLsRows(entries, false, true, false, false) // --all, version view
	status := map[string]string{}
	for _, r := range rows {
		status[r.Label] = r.Status
	}
	want := map[string]string{
		"26.7.0": "",            // installable
		"9.7.2":  "",            // installable (LTS)
		"9.7.1":  "unavailable", // no platform package
		"8.4.11": "",            // installable (LTS)
		"8.4.6":  "unavailable",
		"8.0.46": "installed",
		"8.0.40": "", // installable
		"5.7.44": "unavailable",
	}
	for label, wantStatus := range want {
		if status[label] != wantStatus {
			t.Errorf("status[%s] = %q, want %q", label, status[label], wantStatus)
		}
	}

	// series view: the series carries its representative's availability —
	// 5.7 (5.7.44) becomes "unavailable" while 8.0 (8.0.46) stays installed
	rows = buildLsRows(entries, false, true, false, true)
	status = map[string]string{}
	for _, r := range rows {
		status[r.Label] = r.Status
	}
	if status["5.7 (5.7.44)"] != "unavailable" {
		t.Errorf("series 5.7 status = %q, want unavailable", status["5.7 (5.7.44)"])
	}
	if status["8.0 (8.0.46)"] != "installed" {
		t.Errorf("series 8.0 status = %q, want installed", status["8.0 (8.0.46)"])
	}

	// installed wins even when the platform has no package (it got there
	// somehow — e.g. a future third-party channel)
	entries = markUnavailable(withInstalled(t, "5.7.44"), "5.7.44")
	rows = buildLsRows(entries, true, false, false, false)
	if len(rows) != 1 || rows[0].Status != "installed" {
		t.Fatalf("rows = %+v, want installed 5.7.44", rows)
	}
}
