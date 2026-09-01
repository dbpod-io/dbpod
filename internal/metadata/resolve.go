package metadata

import (
	"fmt"
	"regexp"
	"strconv"
)

// Select picks the best installable package for the given platform
// (see docs/download-link-acquisition.md §6):
//  1. exact os/arch match
//  2. full packages only (no minimal/test/debug/installer/source)
//  3. kind priority tar.gz > tar.xz > zip
//  4. newest OS build wins (e.g. macos15 over macos14, glibc2.28 over glibc2.17)
func (v *VersionInfo) Select(goos, goarch string) (*Package, error) {
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
		if best == nil || betterThan(p, best) {
			best = p
		}
	}
	if best == nil {
		return nil, fmt.Errorf("no installable package of %s for %s/%s", v.Version, goos, goarch)
	}
	return best, nil
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
