package metadata

import (
	"reflect"
	"testing"
)

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
	p := &Package{Filename: "mysql-8.0.46-macos15-arm64.tar.gz", URL: "MySQL-8.0/mysql-8.0.46-macos15-arm64.tar.gz"}
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
