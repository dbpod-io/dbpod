package mysql

import (
	"testing"

	"github.com/shapled/dbpod/internal/metadata"
)

func TestMysqlCatalogResolveVersion(t *testing.T) {
	// series resolution through the catalog (installed preference)
	t.Setenv("DBPOD_HOME", t.TempDir())
	p := MysqlProvider{}
	v, err := p.ResolveVersion("8.0", "")
	if err != nil {
		t.Skipf("metadata unavailable in test env: %v", err)
	}
	if v == "" {
		t.Error("ResolveVersion returned empty")
	}
	_ = metadata.Package{}
}
