// Package hostfacts collects generic, vendor-neutral facts about the host
// operating system: distribution identity (from os-release), libc flavor
// and version, and the Go platform. The facts are provided as input to
// extension resolvers (wasm plugins); dbpod core never interprets them.
package hostfacts

import (
	"bytes"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// Facts describes the host in a resolver-friendly shape.
type Facts struct {
	GOOS        string `json:"goos"`
	GOARCH      string `json:"goarch"`
	OSID        string `json:"os_id,omitempty"`         // os-release ID ("ubuntu")
	OSVersionID string `json:"os_version_id,omitempty"` // os-release VERSION_ID ("24.04")
	OSCodename  string `json:"os_codename,omitempty"`   // os-release VERSION_CODENAME ("noble")
	Libc        string `json:"libc,omitempty"`          // "glibc" | "musl" | "" (unknown)
	LibcVersion string `json:"libc_version,omitempty"`  // e.g. "2.39"
}

const osReleasePath = "/etc/os-release"

// Collect gathers the host facts. Non-linux platforms return the Go
// platform plus the OS product version when available (macOS via
// sw_vers); collection failures degrade individual fields instead of
// failing.
func Collect() Facts {
	f := Facts{GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}
	switch runtime.GOOS {
	case "linux":
		if id, ver, code := parseOSRelease(osReleasePath); id != "" {
			f.OSID, f.OSVersionID, f.OSCodename = id, ver, code
		}
		f.Libc, f.LibcVersion = detectLibc()
	case "darwin":
		f.OSID = "macos"
		f.OSVersionID = macOSProductVersion()
	}
	return f
}

// macOSProductVersion returns the macOS product version ("15.6") via
// sw_vers ("" when unavailable).
func macOSProductVersion() string {
	out, err := exec.Command("sw_vers", "-productVersion").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// parseOSRelease reads KEY=VALUE pairs from an os-release file (quotes
// stripped). Keys of interest: ID, VERSION_ID, VERSION_CODENAME.
func parseOSRelease(path string) (id, versionID, codename string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", "", ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		switch key {
		case "ID":
			id = value
		case "VERSION_ID":
			versionID = value
		case "VERSION_CODENAME":
			codename = value
		}
	}
	return id, versionID, codename
}

// detectLibc identifies the C library and version via ldd: glibc prints
// "ldd (Ubuntu GLIBC 2.39-0ubuntu8) 2.39"; musl prints "musl libc (x86_64)
// Version 1.2.4".
func detectLibc() (libc, version string) {
	out, err := exec.Command("ldd", "--version").Output()
	if err != nil {
		return "", ""
	}
	first := out
	if i := bytes.IndexByte(out, '\n'); i >= 0 {
		first = out[:i]
	}
	line := string(first)
	switch {
	case strings.Contains(line, "musl"):
		if _, v, ok := strings.Cut(line, "Version "); ok {
			return "musl", strings.TrimSpace(v)
		}
		return "musl", ""
	case strings.Contains(line, "libc"):
		fields := strings.Fields(line)
		if len(fields) > 0 {
			if v := fields[len(fields)-1]; strings.Contains(v, ".") {
				return "glibc", v
			}
		}
		return "glibc", ""
	}
	return "", ""
}
