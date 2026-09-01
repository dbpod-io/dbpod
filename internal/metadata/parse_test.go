package metadata

import (
	"os"
	"reflect"
	"testing"
)

func mb(f float64) int64 { return int64(f * (1 << 20)) }
func kb(f float64) int64 { return int64(f * (1 << 10)) }
func gb(f float64) int64 { return int64(f * (1 << 30)) }

func readFixture(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return string(data)
}

func TestParseGAIndexVersions(t *testing.T) {
	versions := parseGAIndexVersions(readFixture(t, "ga_index.html"))
	if len(versions) != 4 {
		t.Fatalf("want 4 GA versions, got %d: %+v", len(versions), versions)
	}
	first := versions[0]
	if first.Version != "26.7.0" || first.Series != "26.7" || !first.Latest {
		t.Errorf("first GA version = %+v, want 26.7.0/26.7/latest", first)
	}
	got9 := map[string]VersionInfo{}
	for _, v := range versions {
		got9[v.Version] = v
	}
	for want, lts := range map[string]bool{"9.7.2": true, "8.4.11": true, "8.0.46": false} {
		v, ok := got9[want]
		if !ok {
			t.Fatalf("missing GA version %s", want)
		}
		if v.LTS != lts {
			t.Errorf("%s LTS = %v, want %v", want, v.LTS, lts)
		}
		if v.Series != want[:len(want)-len(v.Version)+len(v.Series)] {
			t.Errorf("%s series = %q", want, v.Series)
		}
	}
}

func TestParseArchiveVersions(t *testing.T) {
	versions := parseArchiveVersions(readFixture(t, "archive_index.html"))
	if len(versions) < 250 {
		t.Fatalf("want >=250 archive versions, got %d", len(versions))
	}
	set := map[string]bool{}
	for _, v := range versions {
		set[v] = true
	}
	for _, want := range []string{"9.7.1", "8.0.35", "5.7.43", "5.0.15"} {
		if !set[want] {
			t.Errorf("archive versions missing %s", want)
		}
	}
}

func TestParseGAPackages(t *testing.T) {
	pkgs := parsePackageRows(readFixture(t, "ga_packages_8.0_macos.html"), "ga")
	if len(pkgs) < 8 {
		t.Fatalf("want >=8 GA packages, got %d", len(pkgs))
	}
	byName := map[string]Package{}
	for _, p := range pkgs {
		byName[p.Filename] = p
	}
	p, ok := byName["mysql-8.0.46-macos15-arm64.tar.gz"]
	if !ok {
		t.Fatalf("missing mysql-8.0.46-macos15-arm64.tar.gz in %+v", filenames(pkgs))
	}
	if p.OS != "darwin" || p.Arch != "arm64" || p.OSVersion != "macos15" || p.Kind != "tar.gz" {
		t.Errorf("parsed platform info wrong: %+v", p)
	}
	if p.MD5 != "aefb850c25a2c703a63554283fb94cae" {
		t.Errorf("md5 = %s", p.MD5)
	}
	if p.Source != "ga" {
		t.Errorf("source = %s", p.Source)
	}
	if p.FallbackURL == "" || !contains(p.FallbackURL, "id=") {
		t.Errorf("fallback url = %q", p.FallbackURL)
	}
	if p.Size != mb(164.4) {
		t.Errorf("size = %d, want %d", p.Size, mb(164.4))
	}
}

func TestParseArchivePackages(t *testing.T) {
	pkgs := parsePackageRows(readFixture(t, "archive_packages_9.7.1_linux.html"), "archive")
	if len(pkgs) < 8 {
		t.Fatalf("want >=8 archive packages, got %d", len(pkgs))
	}
	byName := map[string]Package{}
	for _, p := range pkgs {
		byName[p.Filename] = p
	}
	p, ok := byName["mysql-9.7.1-linux-glibc2.28-x86_64.tar.xz"]
	if !ok {
		t.Fatalf("missing linux tar.xz package in %+v", filenames(pkgs))
	}
	if p.OS != "linux" || p.Arch != "amd64" || p.OSVersion != "glibc2.28" || p.Kind != "tar.xz" {
		t.Errorf("parsed platform info wrong: %+v", p)
	}
	if p.MD5 != "18abe52f40af534e611ce02ee8ecaa8d" {
		t.Errorf("md5 = %s", p.MD5)
	}
	wantFallback := "https://downloads.mysql.com/archives/get/p/23/file/mysql-9.7.1-linux-glibc2.28-x86_64.tar.xz"
	if p.FallbackURL != wantFallback {
		t.Errorf("fallback = %s, want %s", p.FallbackURL, wantFallback)
	}
	if p.Size != mb(933.4) {
		t.Errorf("size = %d, want %d", p.Size, mb(933.4))
	}
}

func filenames(pkgs []Package) []string {
	out := make([]string, len(pkgs))
	for i, p := range pkgs {
		out[i] = p.Filename
	}
	return out
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}

func TestApplyFilenameInfo(t *testing.T) {
	cases := []struct {
		filename            string
		os, arch, osv, kind string
		variant             string
	}{
		{"mysql-8.0.46-macos15-arm64.tar.gz", "darwin", "arm64", "macos15", "tar.gz", ""},
		{"mysql-8.0.46-macos15-x86_64.tar.gz", "darwin", "amd64", "macos15", "tar.gz", ""},
		{"mysql-8.0.35-macos13-arm64.tar.gz", "darwin", "arm64", "macos13", "tar.gz", ""},
		{"mysql-9.7.1-linux-glibc2.28-x86_64.tar.xz", "linux", "amd64", "glibc2.28", "tar.xz", ""},
		{"mysql-9.7.1-linux-glibc2.28-aarch64.tar.xz", "linux", "arm64", "glibc2.28", "tar.xz", ""},
		{"mysql-5.7.43-linux-glibc2.12-x86_64.tar.gz", "linux", "amd64", "glibc2.12", "tar.gz", ""},
		{"mysql-9.7.1-winx64.zip", "windows", "amd64", "", "zip", ""},
		{"mysql-9.7.1-linux-glibc2.28-x86_64-minimal.tar.xz", "linux", "amd64", "glibc2.28", "tar.xz", "minimal"},
		{"mysql-test-9.7.1-linux-glibc2.28-x86_64.tar.xz", "linux", "amd64", "glibc2.28", "tar.xz", "test"},
		{"mysql-8.0.46-macos15-arm64.dmg", "darwin", "arm64", "macos15", "dmg", ""},
	}
	for _, c := range cases {
		p := Package{Filename: c.filename}
		applyFilenameInfo(&p)
		if p.OS != c.os || p.Arch != c.arch || p.OSVersion != c.osv || p.Kind != c.kind || p.Variant != c.variant {
			t.Errorf("%s => os=%s arch=%s osv=%s kind=%s variant=%s, want %s/%s/%s/%s/%q",
				c.filename, p.OS, p.Arch, p.OSVersion, p.Kind, p.Variant, c.os, c.arch, c.osv, c.kind, c.variant)
		}
	}
}

func TestParseSize(t *testing.T) {
	cases := map[string]int64{
		"595.8M": mb(595.8),
		"933.4M": mb(933.4),
		"79.2M":  mb(79.2),
		"12.1K":  kb(12.1),
		"1.2G":   gb(1.2),
		"100":    100,
		"":       0,
	}
	for in, want := range cases {
		if got := parseSize(in); got != want {
			t.Errorf("parseSize(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestSelect(t *testing.T) {
	v := &VersionInfo{Version: "8.0.46", Series: "8.0", Packages: []Package{
		{Filename: "a.dmg", OS: "darwin", Arch: "arm64", OSVersion: "macos15", Kind: "dmg"},
		{Filename: "macos14.tar.gz", OS: "darwin", Arch: "arm64", OSVersion: "macos14", Kind: "tar.gz"},
		{Filename: "macos15.tar.gz", OS: "darwin", Arch: "arm64", OSVersion: "macos15", Kind: "tar.gz"},
		{Filename: "minimal.tar.gz", OS: "darwin", Arch: "arm64", OSVersion: "macos15", Kind: "tar.gz", Variant: "minimal"},
		{Filename: "linux.tar.xz", OS: "linux", Arch: "amd64", OSVersion: "glibc2.28", Kind: "tar.xz"},
	}}
	p, err := v.Select("darwin", "arm64")
	if err != nil {
		t.Fatal(err)
	}
	if p.Filename != "macos15.tar.gz" {
		t.Errorf("selected %s, want macos15.tar.gz", p.Filename)
	}
	if _, err := v.Select("linux", "amd64"); err != nil {
		t.Errorf("linux select: %v", err)
	}
	if _, err := v.Select("linux", "arm64"); err == nil {
		t.Error("expected error selecting for linux/arm64")
	}
}

func TestSelectPrefersTarGzOverTarXz(t *testing.T) {
	v := &VersionInfo{Packages: []Package{
		{Filename: "b.tar.xz", OS: "linux", Arch: "amd64", Kind: "tar.xz"},
		{Filename: "a.tar.gz", OS: "linux", Arch: "amd64", Kind: "tar.gz"},
	}}
	p, err := v.Select("linux", "amd64")
	if err != nil || p.Filename != "a.tar.gz" {
		t.Errorf("selected %v (err %v), want a.tar.gz", p, err)
	}
}

func TestCompareVersions(t *testing.T) {
	if compareVersions("9.7.2", "9.7.1") <= 0 {
		t.Error("9.7.2 should be newer than 9.7.1")
	}
	if compareVersions("8.0.46", "8.4.11") >= 0 {
		t.Error("8.4.11 should be newer than 8.0.46")
	}
	if compareVersions("5.0.16a", "5.0.16") <= 0 {
		t.Error("5.0.16a should be newer than 5.0.16")
	}
	if compareVersions("8.0.46", "8.0.46") != 0 {
		t.Error("equal versions should compare 0")
	}
}

func TestSortVersions(t *testing.T) {
	in := []string{"8.0.46", "9.7.2", "5.7.43", "26.7.0", "8.4.11"}
	sortVersions(in)
	want := []string{"26.7.0", "9.7.2", "8.4.11", "8.0.46", "5.7.43"}
	if !reflect.DeepEqual(in, want) {
		t.Errorf("sorted = %v, want %v", in, want)
	}
}

func TestSeriesOf(t *testing.T) {
	cases := map[string]string{
		"8.0.46":  "8.0",
		"9.7.1":   "9.7",
		"5.0.16a": "5.0",
		"8.0":     "8.0",
	}
	for in, want := range cases {
		if got := SeriesOf(in); got != want {
			t.Errorf("SeriesOf(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDownloadURL(t *testing.T) {
	// relative URL stored in metadata: joined with the metadata parent dir
	ix := &Index{BaseURL: "https://mirror.example.com/dbpod"}
	p := &Package{Filename: "mysql-8.0.46-macos15-arm64.tar.gz", URL: RelURL("8.0", "mysql-8.0.46-macos15-arm64.tar.gz")}
	if got, want := p.URL, "MySQL-8.0/mysql-8.0.46-macos15-arm64.tar.gz"; got != want {
		t.Errorf("RelURL = %s, want %s", got, want)
	}
	want := "https://mirror.example.com/dbpod/MySQL-8.0/mysql-8.0.46-macos15-arm64.tar.gz"
	if got := ix.DownloadURL(p); got != want {
		t.Errorf("relative via mirror = %s, want %s", got, want)
	}

	// official base
	ix.BaseURL = OfficialDownloadsBase
	wantOfficial := "https://cdn.mysql.com/Downloads/MySQL-8.0/mysql-8.0.46-macos15-arm64.tar.gz"
	if got := ix.DownloadURL(p); got != wantOfficial {
		t.Errorf("relative via official = %s, want %s", got, wantOfficial)
	}
	if got := CDNURL("8.0", "mysql-8.0.46-macos15-arm64.tar.gz"); got != wantOfficial {
		t.Errorf("CDNURL = %s, want %s", got, wantOfficial)
	}

	// absolute URL passes through untouched regardless of base
	abs := &Package{URL: "https://elsewhere.example.com/file.tar.gz"}
	if got := ix.DownloadURL(abs); got != abs.URL {
		t.Errorf("absolute URL = %s, want %s", got, abs.URL)
	}

	// trailing slash on base must not double
	ix.BaseURL = "https://mirror.example.com/dbpod/"
	if got := ix.DownloadURL(p); got != want {
		t.Errorf("relative via mirror with trailing slash = %s, want %s", got, want)
	}
}
