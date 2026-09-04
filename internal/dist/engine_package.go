package dist

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// enginePackage.go: extraction pipelines for engine packages that carry
// their own layout rules (PGDG .deb/.rpm on linux) — files are mapped from
// distribution paths into the portable engine layout:
//
//	bin/          server, client and admin tools
//	lib/          engine-private extensions
//	share/        timezone data, sample configs, SQL scripts
//	shared_libs/  bundled runtime libraries (libssl, libicu, ...)
//
// glibc (libc6/libgcc-s1) is deliberately NOT bundled: injecting a private
// glibc via LD_LIBRARY_PATH breaks the host loader. Host glibc is used.

// extractEnginePackage extracts a deb/rpm main archive plus its dependency
// archives into dest, applying the prefix rules.
func extractEnginePackage(kind, mainArchive string, depArchives []string, dest string, rules [][2]string) error {
	extractOne := func(kind, archive string) error {
		switch kind {
		case "deb":
			return extractDeb(archive, dest, rules)
		case "rpm":
			return extractRPM(archive, dest, rules)
		default:
			return fmt.Errorf("unsupported engine package kind %q", kind)
		}
	}
	if err := extractOne(kind, mainArchive); err != nil {
		return err
	}
	for _, dep := range depArchives {
		// dependency archives share the main package's kind
		if err := extractOne(kind, dep); err != nil {
			return err
		}
	}
	return nil
}

// applyRules maps a distribution-relative path into the engine layout.
// Unmatched paths are dropped (returned ok=false).
func applyRules(rel string, rules [][2]string) (string, bool) {
	rel = strings.TrimPrefix(rel, "./")
	rel = strings.TrimPrefix(rel, "/")
	for _, r := range rules {
		src, dst := r[0], r[1]
		if rel == src {
			return dst, true
		}
		if strings.HasPrefix(rel, src+"/") {
			return dst + rel[len(src):], true
		}
	}
	return "", false
}

// extractDeb unpacks a .deb (ar archive containing data.tar.*) into dest,
// mapping paths through rules.
func extractDeb(debPath, dest string, rules [][2]string) error {
	f, err := os.Open(debPath)
	if err != nil {
		return err
	}
	defer f.Close()

	dataTar, err := debDataMember(f, "data.tar.")
	if err != nil {
		return err
	}
	return extractTarWithRules(dataTar, dest, rules)
}

// debDataMember scans the ar archive for the member whose name has the
// given prefix (e.g. "data.tar.") and returns a decompressed reader.
func debDataMember(f *os.File, namePrefix string) (io.Reader, error) {
	const arMagic = "!<arch>\n"
	header := make([]byte, len(arMagic))
	if _, err := io.ReadFull(f, header); err != nil {
		return nil, err
	}
	if string(header) != arMagic {
		return nil, fmt.Errorf("not an ar archive (missing %q magic)", arMagic)
	}
	for {
		hdr := make([]byte, 60)
		if _, err := io.ReadFull(f, hdr); err != nil {
			if err == io.EOF {
				return nil, fmt.Errorf("ar member %q not found", namePrefix)
			}
			return nil, err
		}
		name := strings.TrimSuffix(strings.TrimSpace(string(hdr[0:16])), "/")
		sizeStr := strings.TrimSpace(string(hdr[48:58]))
		var size int64
		if _, err := fmt.Sscanf(sizeStr, "%d", &size); err != nil {
			return nil, fmt.Errorf("bad ar member size %q", sizeStr)
		}
		if strings.HasPrefix(name, namePrefix) {
			var r io.Reader = io.LimitReader(f, size)
			switch {
			case strings.HasSuffix(name, ".gz"):
				gz, err := gzip.NewReader(r)
				if err != nil {
					return nil, err
				}
				defer gz.Close()
				return io.LimitReader(gz, 1<<40), nil
			case strings.HasSuffix(name, ".xz"):
				return xzReader(r)
			case strings.HasSuffix(name, ".zst"):
				return zstdReader(r)
			default:
				return r, nil
			}
		}
		if _, err := f.Seek((size+1)&^1, io.SeekCurrent); err != nil {
			return nil, err // members are 2-byte aligned
		}
	}
}

// extractTarWithRules reads a tar stream from r and writes entries into
// dest through the prefix rules (unmatched entries are dropped).
func extractTarWithRules(r io.Reader, dest string, rules [][2]string) error {
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		mapped, ok := applyRules(hdr.Name, rules)
		if !ok {
			continue
		}
		target := filepath.Join(dest, mapped)
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, hdr.FileInfo().Mode().Perm())
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return err
			}
			if err := out.Close(); err != nil {
				return err
			}
		case tar.TypeSymlink:
			_ = os.MkdirAll(filepath.Dir(target), 0o755)
			_ = os.Remove(target)
			if err := os.Symlink(hdr.Linkname, target); err != nil && !os.IsExist(err) {
				return err
			}
		}
	}
}

// extractRPM unpacks a .rpm into dest, mapping paths through rules.
// RPM = lead(96B) + signature header + header + cpio payload (gzip/xz/zst).
func extractRPM(rpmPath, dest string, rules [][2]string) error {
	f, err := os.Open(rpmPath)
	if err != nil {
		return err
	}
	defer f.Close()

	payload, err := rpmPayloadReader(f)
	if err != nil {
		return err
	}
	return extractCpioWithRules(payload, dest, rules)
}

// extractCpioWithRules reads a cpio archive (SVR4 "newc" format) and maps
// entries through the prefix rules.
func extractCpioWithRules(r io.Reader, dest string, rules [][2]string) error {
	for {
		hdr := make([]byte, 110)
		if _, err := io.ReadFull(r, hdr); err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		if string(hdr[0:6]) != "070701" {
			return fmt.Errorf("bad cpio entry magic %q", hdr[0:6])
		}
		parseHex := func(b []byte) int64 {
			var n int64
			for _, c := range b {
				var v int64
				switch {
				case c >= '0' && c <= '9':
					v = int64(c - '0')
				case c >= 'a' && c <= 'f':
					v = int64(c-'a') + 10
				case c >= 'A' && c <= 'F':
					v = int64(c-'A') + 10
				default:
					return n
				}
				n = n<<4 | v
			}
			return n
		}
		nameLen := parseHex(hdr[94:110])
		fileSize := parseHex(hdr[54:62])
		mode := parseHex(hdr[14:22])
		nameBytes := make([]byte, nameLen)
		if _, err := io.ReadFull(r, nameBytes); err != nil {
			return err
		}
		name := strings.TrimRight(string(nameBytes), "\x00")
		if name == "TRAILER!!!" {
			// consume padding to the 4-byte boundary and stop
			if pad := (110 + nameLen) % 4; pad != 0 {
				_, _ = io.CopyN(io.Discard, r, int64(4-pad))
			}
			return nil
		}
		if pad := (110 + nameLen) % 4; pad != 0 {
			if _, err := io.CopyN(io.Discard, r, int64(4-pad)); err != nil {
				return err
			}
		}

		if mode&0o170000 == 0o040000 { // directory
			if mapped, ok := applyRules(name, rules); ok {
				_ = os.MkdirAll(filepath.Join(dest, mapped), 0o755)
			}
		} else if mode&0o170000 == 0o100000 { // regular file
			body := make([]byte, fileSize)
			if _, err := io.ReadFull(r, body); err != nil {
				return err
			}
			if mapped, ok := applyRules(name, rules); ok {
				target := filepath.Join(dest, mapped)
				if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
					return err
				}
				if err := os.WriteFile(target, body, os.FileMode(mode&0o777)); err != nil {
					return err
				}
			}
		}
		if pad := fileSize % 4; pad != 0 {
			if _, err := io.CopyN(io.Discard, r, int64(4-pad)); err != nil {
				return err
			}
		}
	}
}
