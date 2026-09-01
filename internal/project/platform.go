package project

import "runtime"

// platformDir returns the normalized platform identifier used in
// ~/.dbpod/versions/<engine>/<version>/<platform>/ layout.
func platformDir() string {
	return goosArch()
}

// GoosArch returns the normalized "os-arch" pair for the current machine.
func GoosArch() string {
	return goosArch()
}

func goosArch() string {
	os := runtime.GOOS
	arch := runtime.GOARCH
	switch {
	case os == "darwin" && arch == "amd64":
		return "darwin-x86_64"
	case os == "darwin" && arch == "arm64":
		return "darwin-arm64"
	case os == "linux" && arch == "amd64":
		return "linux-x86_64"
	case os == "linux" && arch == "arm64":
		return "linux-aarch64"
	case os == "windows" && arch == "amd64":
		return "windows-x86_64"
	case os == "windows" && arch == "arm64":
		return "windows-arm64"
	default:
		return os + "-" + arch
	}
}
