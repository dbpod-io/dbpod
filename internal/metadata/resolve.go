package metadata

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// HostCompat carries the host's compatibility ranks in the two OSVersion
// dimensions packages may use: the distribution/system version (ubuntu2404,
// macos15) and the libc version (glibc2.28, musl1.2). A package matches
// when its rank is ≤ the host's rank in its dimension; packages with no
// OSVersion are always compatible.
type HostCompat struct {
	Distro int
	Libc   int
}

// Select picks the best installable package for the given platform
// (see docs/download-link-acquisition.md §6):
//  1. exact os/arch match
//  2. full packages only (no minimal/test/debug/installer/source)
//  3. kind priority tar.gz > tar.xz > zip
//  4. newest OS build wins (e.g. macos15 over macos14, glibc2.28 over glibc2.17)
func (v *VersionInfo) Select(goos, goarch string) (*Package, error) {
	return v.selectPackage(goos, goarch, 0, 0, false)
}

// SelectForHost picks the best package the HOST can run: packages tagged
// with a distribution/libc version newer than the host's are skipped
// (older builds run on newer hosts, not vice versa). Packages without an
// OSVersion are always compatible.
func (v *VersionInfo) SelectForHost(goos, goarch string, host HostCompat) (*Package, error) {
	return v.selectPackage(goos, goarch, host.Distro, host.Libc, true)
}

func (v *VersionInfo) selectPackage(goos, goarch string, distroRank, libcRank int, hostMatch bool) (*Package, error) {
	var best *Package
	for i := range v.Packages {
		p := &v.Packages[i]
		if p.OS != goos || p.Arch != goarch {
			continue
		}
		if p.Variant != "" {
			continue
		}
		if kindRank(p.Kind) == 0 {
			continue // unsupported kind (dmg/msi/deb/rpm/plain tar/source)
		}
		if hostMatch && !hostCompatible(p.OSVersion, distroRank, libcRank) {
			continue
		}
		if best == nil || betterThan(p, best) {
			best = p
		}
	}
	if best == nil {
		return nil, fmt.Errorf("no installable package of %s for %s/%s", v.Version, goos, goarch)
	}
	return best, nil
}

// hostCompatible reports whether a package's OSVersion fits the host:
// libc-tagged versions compare against the host libc rank, everything
// else against the host distribution rank; untagged packages always fit.
func hostCompatible(osVersion string, distroRank, libcRank int) bool {
	rank, isLibc := LibcRankOf(osVersion)
	if isLibc {
		return rank <= libcRank
	}
	if rank := DistroRank(osVersion); rank > 0 {
		return rank <= distroRank
	}
	return true // no version tag: generic build
}

// LibcRankOf recognizes libc-tagged OSVersions ("glibc2.28", "musl1.2")
// and returns their rank.
func LibcRankOf(osVersion string) (int, bool) {
	switch {
	case strings.HasPrefix(osVersion, "glibc"):
		return osVersionRank(strings.TrimPrefix(osVersion, "glibc")), true
	case strings.HasPrefix(osVersion, "musl"):
		return osVersionRank(strings.TrimPrefix(osVersion, "musl")), true
	}
	return 0, false
}

// DistroRank ranks a distribution-tagged OSVersion ("ubuntu2404" → 2404,
// "rhel93" → 903, "macos15" → 15); "" and unparseable → 0.
func DistroRank(osVersion string) int {
	return osVersionRank(osVersion)
}

// LibcRank ranks a libc version string ("glibc2.39" → 239); "" → 0.
func LibcRank(libcVersion string) int {
	return osVersionRank(libcVersion)
}

func betterThan(a, b *Package) bool {
	if ka, kb := kindRank(a.Kind), kindRank(b.Kind); ka != kb {
		return ka < kb
	}
	return osVersionRank(a.OSVersion) > osVersionRank(b.OSVersion)
}

// kindRank: lower is better; 0 means not installable directly.
func kindRank(kind string) int {
	switch kind {
	case "tar.gz":
		return 1
	case "tar.xz":
		return 2
	case "zip":
		return 3
	default:
		return 0
	}
}

var reOSVerNum = regexp.MustCompile(`(\d+)(?:\.(\d+))?`)

func osVersionRank(osVersion string) int {
	if osVersion == "" {
		return 0
	}
	m := reOSVerNum.FindStringSubmatch(osVersion)
	if m == nil {
		return 0
	}
	rank, _ := strconv.Atoi(m[1])
	if m[2] != "" {
		minor, _ := strconv.Atoi(m[2])
		rank = rank*100 + minor
	}
	return rank
}
