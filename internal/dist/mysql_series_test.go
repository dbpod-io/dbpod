package dist

import (
	"testing"

	"github.com/shapled/dbpod/internal/metadata"
)

func TestMysqlSeriesOf(t *testing.T) {
	cases := []struct {
		version  string
		lts      bool
		isLatest bool
		want     string
	}{
		{"8.0.46", false, false, "8.0"},
		{"5.7.44", false, false, "5.7"},
		{"9.7.2", true, false, "9.7"},
		{"26.7.0", false, false, "innovation"}, // calendar non-LTS: always innovation
		{"26.10.1", true, true, "innovation"},  // calendar LTS and globally newest
		{"26.10.1", true, false, "26.10"},      // calendar LTS, superseded: own series
	}
	cat := MysqlCatalog{}
	for _, c := range cases {
		if got := cat.SeriesOf(c.version, c.lts, c.isLatest); got != c.want {
			t.Errorf("SeriesOf(%q, lts=%v, latest=%v) = %q, want %q", c.version, c.lts, c.isLatest, got, c.want)
		}
	}
}

func TestMysqlCatalogResolveVersion(t *testing.T) {
	// series resolution through the catalog (installed preference)
	t.Setenv("DBPOD_HOME", t.TempDir())
	cat := MysqlCatalog{}
	v, err := cat.ResolveVersion("8.0", "")
	if err != nil {
		t.Skipf("metadata unavailable in test env: %v", err)
	}
	if v == "" {
		t.Error("ResolveVersion returned empty")
	}
	_ = metadata.Package{}
}
