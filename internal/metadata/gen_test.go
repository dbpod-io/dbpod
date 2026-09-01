package metadata

import (
	"net/http"
	"net/http/httptest"
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
