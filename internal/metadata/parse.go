package metadata

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var (
	reOption = regexp.MustCompile(`(?is)<option\s+value="([^"]*)"[^>]*>\s*(.*?)\s*</option>`)
	reRow    = regexp.MustCompile(`(?is)<tr[^>]*>(.*?)</tr>`)
	reBold   = regexp.MustCompile(`<b>(.*?)</b>`)
	reHref   = regexp.MustCompile(`href="([^"]+)"`)
	reFileID = regexp.MustCompile(`/downloads/file/\?id=(\d+)`)
	reGetURL = regexp.MustCompile(`/archives/get/p/(\d+)/file/([^"]+)`)
	reSubTD  = regexp.MustCompile(`class="sub-text">\s*\(([^)]+)\)`)
	reMD5    = regexp.MustCompile(`class="md5">([0-9a-f]{32})`)
	reSize   = regexp.MustCompile(`class="col4">([^<]+)<`)
)

// parseGAIndexVersions extracts the latest version series from the GA page.
// Option values are major.minor, option text is the full version (maybe "x.y.z LTS").
func parseGAIndexVersions(html string) []VersionInfo {
	sel := extractSelect(html, "version")
	var out []VersionInfo
	for _, m := range reOption.FindAllStringSubmatch(sel, -1) {
		value, text := m[1], strings.TrimSpace(stripTags(m[2]))
		lts := strings.HasSuffix(text, "LTS")
		text = strings.TrimSuffix(strings.TrimSpace(strings.TrimSuffix(text, "LTS")), " ")
		if text == "" {
			continue
		}
		out = append(out, VersionInfo{Version: text, Series: value, LTS: lts, Latest: true})
	}
	return out
}

// parseArchiveVersions extracts all historical full versions from the archive page.
func parseArchiveVersions(html string) []string {
	sel := extractSelect(html, "version")
	var out []string
	for _, m := range reOption.FindAllStringSubmatch(sel, -1) {
		v := strings.TrimSpace(m[1])
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

// extractSelect returns the inner HTML of <select name="name">...</select>.
func extractSelect(html, name string) string {
	start := strings.Index(html, `<select name="`+name+`"`)
	if start < 0 {
		return ""
	}
	end := strings.Index(html[start:], "</select>")
	if end < 0 {
		return ""
	}
	return html[start : start+end]
}

func stripTags(s string) string {
	return reTagStrip.ReplaceAllString(s, "")
}

var reTagStrip = regexp.MustCompile(`<[^>]*>`)

// parsePackageRows parses the alternating two-row-per-package table shared by
// the GA and archive pages: odd rows carry description + download link + size,
// even rows carry (filename) + MD5.
func parsePackageRows(html, source string) []Package {
	var pkgs []Package
	cur := -1
	for _, m := range reRow.FindAllStringSubmatch(html, -1) {
		row := m[1]
		switch {
		case strings.Contains(row, "Download</a>") || reGetURL.MatchString(row):
			p := Package{Source: source}
			if d := reBold.FindStringSubmatch(row); d != nil {
				p.Description = strings.TrimSpace(stripTags(d[1]))
			}
			if s := reSize.FindStringSubmatch(row); s != nil {
				p.Size = parseSize(s[1])
			}
			if g := reGetURL.FindStringSubmatch(row); g != nil {
				p.Filename = g[2]
				p.FallbackURL = "https://downloads.mysql.com" + g[0]
			} else if id := reFileID.FindStringSubmatch(row); id != nil {
				p.FallbackURL = "https://dev.mysql.com/downloads/file/?id=" + id[1]
			}
			if p.Filename == "" && p.FallbackURL == "" {
				continue
			}
			pkgs = append(pkgs, p)
			cur = len(pkgs) - 1
		case cur >= 0:
			if f := reSubTD.FindStringSubmatch(row); f != nil && pkgs[cur].Filename == "" {
				pkgs[cur].Filename = f[1]
			}
			if md := reMD5.FindStringSubmatch(row); md != nil {
				pkgs[cur].MD5 = md[1]
			}
			if pkgs[cur].Filename != "" && pkgs[cur].MD5 != "" {
				cur = -1
			}
		}
	}
	for i := range pkgs {
		applyFilenameInfo(&pkgs[i])
	}
	return pkgs
}

var reKinds = []string{".tar.gz", ".tar.xz", ".tar", ".zip", ".dmg", ".msi", ".deb", ".rpm"}

// applyFilenameInfo derives version/os/arch/kind/variant from the filename
// (see docs/download-link-acquisition.md §3).
func applyFilenameInfo(p *Package) {
	name := p.Filename
	if name == "" {
		return
	}
	base := name
	kind := ""
	for _, k := range reKinds {
		if strings.HasSuffix(base, k) {
			kind = strings.TrimPrefix(k, ".")
			base = strings.TrimSuffix(base, k)
			break
		}
	}
	p.Kind = kind

	// test suites ship as mysql-test-<ver>-... archives
	if strings.HasPrefix(base, "mysql-test-") {
		p.Variant = "test"
		base = strings.TrimPrefix(base, "mysql-test-")
	} else {
		base = strings.TrimPrefix(base, "mysql-")
	}

	// leading version: 8.0.46 / 5.0.16a
	verIdx := reLeadingVer.FindStringSubmatch(base)
	if verIdx == nil {
		return
	}
	rest := strings.TrimPrefix(strings.TrimPrefix(base, verIdx[0]), "-")

	// trailing variant tokens
	for _, v := range []string{"minimal", "debug-test", "debug", "test"} {
		if rest == v || strings.HasSuffix(rest, "-"+v) {
			p.Variant = v
			rest = strings.TrimSuffix(strings.TrimSuffix(rest, v), "-")
			break
		}
	}
	p.OS, p.Arch, p.OSVersion = parsePlatform(rest)
}

var reLeadingVer = regexp.MustCompile(`^\d+\.\d+\.\d+[a-z]?`)

// parsePlatform maps the platform segment of a filename to os/arch/osVersion.
func parsePlatform(seg string) (os, arch, osVersion string) {
	s := strings.ToLower(seg)
	switch {
	case strings.HasPrefix(s, "winx64"), strings.HasPrefix(s, "winx86-64"):
		return "windows", "amd64", ""
	case strings.HasPrefix(s, "macos"), strings.HasPrefix(s, "osx"):
		m := reMacOS.FindStringSubmatch(s)
		osVersion = ""
		if m != nil {
			osVersion = "macos" + m[1]
		}
		return "darwin", archFrom(s), osVersion
	case strings.HasPrefix(s, "linux"):
		if m := regexp.MustCompile(`glibc([0-9.]+)`).FindStringSubmatch(s); m != nil {
			osVersion = "glibc" + m[1]
		}
		return "linux", archFrom(s), osVersion
	case strings.HasPrefix(s, "src"):
		return "source", "", ""
	default:
		return seg, archFrom(s), ""
	}
}

var reMacOS = regexp.MustCompile(`(?:macos|osx)(\d+(?:\.\d+)?)`)

func archFrom(s string) string {
	switch {
	case strings.Contains(s, "aarch64"), strings.Contains(s, "arm64"):
		return "arm64"
	case strings.Contains(s, "x86_64"), strings.Contains(s, "x86-64"), strings.Contains(s, "winx64"):
		return "amd64"
	default:
		return ""
	}
}

// parseSize converts human sizes like "595.8M", "933.4M", "12.1K", "1.2G" to bytes.
func parseSize(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	unit := s[len(s)-1]
	mult := float64(1)
	switch unit {
	case 'K', 'k':
		mult, s = 1<<10, s[:len(s)-1]
	case 'M', 'm':
		mult, s = 1<<20, s[:len(s)-1]
	case 'G', 'g':
		mult, s = 1<<30, s[:len(s)-1]
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0
	}
	return int64(f * mult)
}

// SeriesOf returns the major.minor series of a full version string.
func SeriesOf(version string) string {
	var a, b int
	if n, _ := fmt.Sscanf(version, "%d.%d", &a, &b); n == 2 {
		return fmt.Sprintf("%d.%d", a, b)
	}
	return version
}
