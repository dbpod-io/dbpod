package dist

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ulikunitz/xz"
)

func TestParseRef(t *testing.T) {
	for in, want := range map[string]PackageRef{
		"mysql@8.0.35": {Engine: "mysql", Version: "8.0.35"},
		"mysql@8.0":    {Engine: "mysql", Version: "8.0"},
		"postgres@16":  {Engine: "postgres", Version: "16"},
	} {
		got, err := ParseRef(in)
		if err != nil || got != want {
			t.Errorf("ParseRef(%q) = %+v, %v; want %+v", in, got, err, want)
		}
	}
	for _, bad := range []string{"mysql", "mysql@", "@8.0.35", "foo"} {
		if _, err := ParseRef(bad); err == nil {
			t.Errorf("ParseRef(%q) should fail", bad)
		}
	}
}

func makeTar(t *testing.T, entries []tar.Header, contents map[string]string, useXZ bool) string {
	t.Helper()
	name := "pkg.tar.gz"
	if useXZ {
		name = "pkg.tar.xz"
	}
	path := filepath.Join(t.TempDir(), name)
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	var w io.WriteCloser = f
	if useXZ {
		xw, err := xz.NewWriter(f)
		if err != nil {
			t.Fatal(err)
		}
		defer xw.Close()
		w = xw
	} else {
		gw := gzip.NewWriter(f)
		defer gw.Close()
		w = gw
	}
	tw := tar.NewWriter(w)
	defer tw.Close()
	for _, h := range entries {
		h.Format = tar.FormatPAX
		if h.Typeflag == tar.TypeReg {
			h.Size = int64(len(contents[h.Name]))
		}
		if err := tw.WriteHeader(&h); err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg {
			if _, err := tw.Write([]byte(contents[h.Name])); err != nil {
				t.Fatal(err)
			}
		}
	}
	return path
}

func TestUntarGzAndXz(t *testing.T) {
	entries := []tar.Header{
		{Name: "pkg-1.0/", Typeflag: tar.TypeDir},
		{Name: "pkg-1.0/bin/", Typeflag: tar.TypeDir},
		{Name: "pkg-1.0/bin/mysqld", Typeflag: tar.TypeReg, Mode: 0o755},
		{Name: "pkg-1.0/share/notes.txt", Typeflag: tar.TypeReg, Mode: 0o644},
	}
	contents := map[string]string{
		"pkg-1.0/bin/mysqld":      "#!/bin/sh\n",
		"pkg-1.0/share/notes.txt": "hello",
	}
	for _, tc := range []struct {
		name string
		xz   bool
	}{{"gz", false}, {"xz", true}} {
		kind := "tar.gz"
		if tc.xz {
			kind = "tar.xz"
		}
		archive := makeTar(t, entries, contents, tc.xz)
		dest := t.TempDir()
		root, err := extract(kind, archive, dest)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if root != "pkg-1.0" {
			t.Errorf("%s: root = %q, want pkg-1.0", tc.name, root)
		}
		data, err := os.ReadFile(filepath.Join(dest, "pkg-1.0", "share", "notes.txt"))
		if err != nil || string(data) != "hello" {
			t.Errorf("%s: content wrong: %q, %v", tc.name, data, err)
		}
		info, err := os.Stat(filepath.Join(dest, "pkg-1.0", "bin", "mysqld"))
		if err != nil || info.Mode().Perm() != 0o755 {
			t.Errorf("%s: mode not preserved: %v", tc.name, err)
		}
	}
}

func TestUnzip(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "pkg.zip")
	f, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for _, e := range []struct{ name, body string }{
		{"pkg-1.0/bin/mysqld.exe", "binary"},
		{"pkg-1.0/README", "readme"},
	} {
		w, err := zw.Create(e.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(e.body)); err != nil {
			t.Fatal(err)
		}
	}
	zw.Close()
	f.Close()

	dest := t.TempDir()
	root, err := extract("zip", archive, dest)
	if err != nil {
		t.Fatal(err)
	}
	if root != "pkg-1.0" {
		t.Errorf("root = %q, want pkg-1.0", root)
	}
	data, _ := os.ReadFile(filepath.Join(dest, "pkg-1.0", "README"))
	if string(data) != "readme" {
		t.Errorf("content = %q", data)
	}
}

func TestExtractRejectsPathTraversal(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "evil.tar.gz")
	f, _ := os.Create(archive)
	gw := gzip.NewWriter(f)
	tw := tar.NewWriter(gw)
	_ = tw.WriteHeader(&tar.Header{Name: "../evil.txt", Typeflag: tar.TypeReg, Mode: 0o644})
	_, _ = tw.Write([]byte("bad"))
	tw.Close()
	gw.Close()
	f.Close()

	dest := t.TempDir()
	root, err := extract("tar.gz", archive, dest)
	if err != nil {
		t.Fatalf("traversal entries should be skipped, got error: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(filepath.Dir(dest), "evil.txt")); statErr == nil {
		t.Error("path traversal succeeded!")
	}
	if root != "." {
		t.Errorf("root = %q, want .", root)
	}
}

func TestExtractUnsupportedFormat(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "pkg.msi")
	_ = os.WriteFile(archive, []byte("x"), 0o644)
	if _, err := extract("msi", archive, t.TempDir()); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Errorf("err = %v, want unsupported format", err)
	}
}

func TestSafeJoin(t *testing.T) {
	dest := "/tmp/dest"
	for _, bad := range []string{"../../etc/passwd", "/abs/path", "../x"} {
		if _, ok := safeJoin(dest, bad); ok {
			t.Errorf("safeJoin(%q) should be rejected", bad)
		}
	}
	got, ok := safeJoin(dest, "a/b/c.txt")
	if !ok || got != filepath.Join(dest, "a/b/c.txt") {
		t.Errorf("safeJoin ok-case = %q, %v", got, ok)
	}
}
