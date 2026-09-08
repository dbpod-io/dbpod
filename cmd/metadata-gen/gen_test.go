package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// newFakeServer serves the saved HTML fixtures in place of the real pages
// (used by the generator tests). The gaIndex fixture is read through a
// pointer so tests can simulate a new release appearing between runs. If
// hits is non-nil every request is counted.
func newFakeServer(t *testing.T, gaIndex *string, hits *int64) *httptest.Server {
	t.Helper()
	// no politeness delay against the fake server
	oldPace := pace
	pace = func() {}
	t.Cleanup(func() { pace = oldPace })
	gaPkg := readFixture(t, "ga_packages_8.0_macos.html")
	archIdx := readFixture(t, "archive_index.html")
	archPkg := readFixture(t, "archive_packages_9.7.1_linux.html")
	mux := http.NewServeMux()
	count := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if hits != nil {
				atomic.AddInt64(hits, 1)
			}
			h(w, r)
		}
	}
	mux.HandleFunc("/ga", count(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Has("version") {
			_, _ = w.Write([]byte(gaPkg))
			return
		}
		_, _ = w.Write([]byte(*gaIndex))
	}))
	mux.HandleFunc("/arch", count(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Has("version") {
			_, _ = w.Write([]byte(archPkg))
			return
		}
		_, _ = w.Write([]byte(archIdx))
	}))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	t.Setenv("DBPOD_HOME", t.TempDir())
	return srv
}

func swapURLs(t *testing.T, srv *httptest.Server) {
	t.Helper()
	oldGA, oldArch := gaIndexURL, archiveBaseURL
	gaIndexURL, archiveBaseURL = srv.URL+"/ga", srv.URL+"/arch"
	t.Cleanup(func() { gaIndexURL, archiveBaseURL = oldGA, oldArch })
}
func TestGenerateIncremental(t *testing.T) {
	ga := readFixture(t, "ga_index.html")
	swapURLs(t, newFakeServer(t, &ga, nil))

	dir := t.TempDir()
	out := filepath.Join(dir, "mysql.json")

	// first generation from an empty file crawls everything the fake serves
	if err := generate(dir, "mysql", 4, os.Stderr); err != nil {
		t.Fatal(err)
	}
	first, err := loadGenerated(out)
	if err != nil || first == nil {
		t.Fatalf("generated file missing: %v", err)
	}
	got := first.Version("8.0.46")
	if got == nil || !got.PackagesFetched || len(got.Packages) == 0 {
		t.Fatalf("8.0.46 packages not generated: %+v", got)
	}
	marker := got.Packages[0].MD5

	// second generation with a new release on the GA page: only the new
	// version is crawled, existing entries stay untouched
	ga = strings.ReplaceAll(ga, "26.7.0", "26.7.1")
	if err := generate(dir, "mysql", 4, os.Stderr); err != nil {
		t.Fatal(err)
	}
	second, _ := loadGenerated(out)
	if second.Version("26.7.0") == nil || second.Version("26.7.1") == nil {
		t.Errorf("incremental merge broken: %v %v", second.Version("26.7.0"), second.Version("26.7.1"))
	}
	if now := second.Version("8.0.46"); now.Packages[0].MD5 != marker {
		t.Errorf("existing version was re-crawled/mutated")
	}
}
