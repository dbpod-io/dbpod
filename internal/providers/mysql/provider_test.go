package mysql

import (
	"reflect"
	"testing"
)

func TestSeriesOf(t *testing.T) {
	cases := []struct {
		version  string
		lts      bool
		isLatest bool
		want     []string
	}{
		{"8.0.46", false, false, []string{"8.0"}},
		{"5.7.44", false, false, []string{"5.7"}},
		{"9.7.2", true, false, []string{"9.7"}},
		{"26.7.0", false, false, []string{"innovation"}}, // calendar non-LTS: always innovation
		{"26.10.1", true, true, []string{"innovation"}},  // calendar LTS and globally newest
		{"26.10.1", true, false, []string{"26.10"}},      // calendar LTS, superseded: own series
	}
	p := MysqlProvider{}
	for _, c := range cases {
		if got := p.SeriesOf(c.version, c.lts, c.isLatest); !reflect.DeepEqual(got, c.want) {
			t.Errorf("SeriesOf(%q, lts=%v, latest=%v) = %v, want %v", c.version, c.lts, c.isLatest, got, c.want)
		}
	}
}
