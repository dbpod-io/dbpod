package dist

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/ulikunitz/xz"
)

// extract unpacks an engine archive (kind: tar.gz / tar.xz / zip) into dest
// and returns the relative path of the distribution root (the directory that
// contains bin/), or "." when the archive has no nested top-level directory.
func extract(kind, archive, dest string) (string, error) {
	switch kind {
	case "tar.gz":
		return untar(archive, dest, "gzip")
	case "tar.xz":
		return untar(archive, dest, "xz")
	case "zip":
		return unzip(archive, dest)
	default:
		return "", fmt.Errorf("unsupported archive format: %s (kind %q)", filepath.Base(archive), kind)
	}
}

func untar(archive, dest, compression string) (string, error) {
	f, err := os.Open(archive)
	if err != nil {
		return "", err
	}
	defer f.Close()

	var tr *tar.Reader
	switch compression {
	case "gzip":
		gz, err := gzip.NewReader(f)
		if err != nil {
			return "", err
		}
		defer gz.Close()
		tr = tar.NewReader(gz)
	case "xz":
		xr, err := xz.NewReader(f)
		if err != nil {
			return "", err
		}
		tr = tar.NewReader(xr)
	default:
		return "", fmt.Errorf("unknown compression %q", compression)
	}

	root := "."
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
		target, ok := safeJoin(dest, hdr.Name)
		if !ok {
			continue // skip unsafe paths (zip-slip)
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return "", err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return "", err
			}
			if err := writeFile(target, tr, hdr.FileInfo().Mode()); err != nil {
				return "", err
			}
		case tar.TypeSymlink:
			_ = os.MkdirAll(filepath.Dir(target), 0o755)
			_ = os.Remove(target)
			if err := os.Symlink(hdr.Linkname, target); err != nil && !os.IsExist(err) {
				return "", err
			}
		}
		if dir, _, ok := strings.Cut(hdr.Name, "/bin/"); ok {
			root = safeRel(dir)
		}
	}
	return root, nil
}

func unzip(archive, dest string) (string, error) {
	zr, err := zip.OpenReader(archive)
	if err != nil {
		return "", err
	}
	defer zr.Close()

	root := "."
	for _, zf := range zr.File {
		target, ok := safeJoin(dest, zf.Name)
		if !ok {
			continue
		}
		if zf.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return "", err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return "", err
		}
		rc, err := zf.Open()
		if err != nil {
			return "", err
		}
		err = writeFile(target, rc, zf.FileInfo().Mode())
		rc.Close()
		if err != nil {
			return "", err
		}
		if dir, _, ok := strings.Cut(zf.Name, "/bin/"); ok {
			root = safeRel(dir)
		}
	}
	return root, nil
}

func writeFile(target string, r io.Reader, mode os.FileMode) error {
	out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode.Perm())
	if err != nil {
		return err
	}
	_, err = io.Copy(out, r)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Chmod(target, mode.Perm())
}

// safeJoin resolves name under dest, rejecting path traversal and
// absolute/volume-qualified paths.
func safeJoin(dest, name string) (string, bool) {
	if filepath.IsAbs(name) || filepath.VolumeName(name) != "" {
		return "", false
	}
	rel := safeRel(name)
	if rel == "" || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return filepath.Join(dest, rel), true
}

func safeRel(name string) string {
	name = filepath.ToSlash(name)
	name = strings.TrimPrefix(name, "./")
	if vol := filepath.VolumeName(name); vol != "" {
		name = name[len(vol):]
	}
	name = strings.TrimLeft(name, "/")
	return filepath.Clean(name)
}
