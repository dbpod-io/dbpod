package fetch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

func TestCopyFetcher(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.tgz")
	if err := os.WriteFile(src, []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(dir, "dest.tgz")

	f := CopyFetcher{}
	res, err := f.Fetch(context.Background(), &url.URL{Scheme: "cp", Path: src}, dest)
	if err != nil {
		t.Fatal(err)
	}
	if res.Bytes != 7 {
		t.Errorf("bytes = %d, want 7", res.Bytes)
	}
	got, err := os.ReadFile(dest)
	if err != nil || string(got) != "payload" {
		t.Errorf("dest content = %q, err=%v", got, err)
	}
}

func TestHTTPFetcher(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/x", func(w http.ResponseWriter, r *http.Request) {
		if r.UserAgent() == "" {
			t.Error("request should carry a User-Agent")
		}
		w.Write([]byte("hello"))
	})
	mux.HandleFunc("/missing", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "out")
	f := &HTTPFetcher{}
	res, err := f.Fetch(context.Background(), &url.URL{Scheme: "http", Host: srv.Listener.Addr().String(), Path: "/x"}, dest)
	if err != nil {
		t.Fatal(err)
	}
	if res.Bytes != 5 {
		t.Errorf("bytes = %d, want 5", res.Bytes)
	}

	// 404 must surface as an error
	if _, err := f.Fetch(context.Background(), &url.URL{Scheme: "http", Host: srv.Listener.Addr().String(), Path: "/missing"}, dest); err == nil {
		t.Error("expected error for 404")
	}
}

func TestRegistryRoutingAndFallback(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "f")
	os.WriteFile(src, []byte("x"), 0o644)

	Register(CopyFetcher{})
	res, err := Fetch(context.Background(), "cp://"+filepath.ToSlash(src), filepath.Join(dir, "out"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Bytes != 1 {
		t.Errorf("bytes = %d", res.Bytes)
	}

	// unknown scheme with no default registered: error
	_, err = Fetch(context.Background(), "noscheme://x", filepath.Join(dir, "y"))
	if err == nil {
		t.Error("expected error for unhandled scheme without default")
	}
}

func TestProxyRebuildAndAudit(t *testing.T) {
	SetProxy(Proxy{HTTP: "http://proxy:8080"})
	defer SetProxy(Proxy{})
	c := HTTPClient()
	if c == nil {
		t.Fatal("HTTPClient nil after SetProxy")
	}
}

func TestFetchBytes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("data"))
	}))
	defer srv.Close()

	body, res, err := FetchBytes(context.Background(), srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "data" || res.Status != 200 {
		t.Errorf("body=%q res=%+v", body, res)
	}
}
