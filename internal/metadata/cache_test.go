package metadata

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dbpod-io/dbpod/internal/project"
)

// testIndex builds a small valid index for tests.
func testIndex(version string, withPackages bool) *Index {
	ix := &Index{
		Engine:   "mysql",
		Versions: map[string]*VersionInfo{},
	}
	ix.Versions[version] = &VersionInfo{Version: version, Series: SeriesOf(version), PackagesFetched: withPackages, Packages: []Package{
		{Filename: "mysql-" + version + "-macos15-arm64.tar.gz", URL: CDNURL(SeriesOf(version), "mysql-"+version+"-macos15-arm64.tar.gz"),
			OS: "darwin", Arch: "arm64", Kind: "tar.gz"},
	}}
	return ix
}

func TestEnsureBuiltinSeedsFromEmbedded(t *testing.T) {
	t.Setenv("DBPOD_HOME", t.TempDir())

	ix, err := EnsureBuiltin("mysql")
	if err != nil {
		t.Fatal(err)
	}
	if len(ix.Versions) == 0 {
		t.Fatal("embedded index is empty")
	}
	if ix.BaseURL != OfficialDownloadsBase {
		t.Errorf("BaseURL = %q, want official", ix.BaseURL)
	}

	// the file was materialized in the configuration directory
	dir, err := project.MetadataDir()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "mysql.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("metadata file not seeded: %v", err)
	}
	if !strings.Contains(string(data), "\"engine\": \"mysql\"") {
		t.Errorf("seeded file unexpected: %s", data)
	}
}

func TestEnsureBuiltinUsesFileOverEmbedded(t *testing.T) {
	t.Setenv("DBPOD_HOME", t.TempDir())

	// a pre-existing config file wins over the embedded copy
	mine := testIndex("9.9.9", true)
	if err := Save("mysql", mine); err != nil {
		t.Fatal(err)
	}
	ix, err := EnsureBuiltin("mysql")
	if err != nil {
		t.Fatal(err)
	}
	if ix.Version("9.9.9") == nil {
		t.Errorf("config file not used: versions %v", ix.ListVersions())
	}
}

func TestEnsureBuiltinDisabled(t *testing.T) {
	t.Setenv("DBPOD_HOME", t.TempDir())
	if _, err := EnsureBuiltin("mysql"); err != nil {
		t.Fatal(err)
	}

	if err := SetDisabled("mysql", true); err != nil {
		t.Fatal(err)
	}
	if !Disabled("mysql") {
		t.Error("Disabled = false after disable")
	}
	if _, err := EnsureBuiltin("mysql"); err == nil {
		t.Error("disabled engine must not be usable")
	}

	if err := SetDisabled("mysql", false); err != nil {
		t.Fatal(err)
	}
	if Disabled("mysql") {
		t.Error("still disabled after enable")
	}
	if _, err := EnsureBuiltin("mysql"); err != nil {
		t.Errorf("enabled engine unusable: %v", err)
	}
}

func TestEnsurePackages(t *testing.T) {
	t.Setenv("DBPOD_HOME", t.TempDir())
	if _, err := EnsureBuiltin("mysql"); err != nil {
		t.Fatal(err)
	}
	v := newestEmbeddedVersion(t)
	_, info, err := EnsurePackages("mysql", v)
	if err != nil {
		t.Fatal(err)
	}
	if len(info.Packages) == 0 {
		t.Errorf("no packages for %s", v)
	}
	if _, _, err := EnsurePackages("mysql", "0.0.0"); err == nil {
		t.Error("unknown version should error")
	}
}

// newestEmbeddedVersion picks a version that exists in the embedded index.
func newestEmbeddedVersion(t *testing.T) string {
	t.Helper()
	ix, err := Embedded("mysql")
	if err != nil {
		t.Fatal(err)
	}
	vs := ix.ListVersions()
	if len(vs) == 0 {
		t.Fatal("embedded index empty")
	}
	return vs[0]
}
