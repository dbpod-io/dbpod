//go:build !windows

package instance

import "syscall"

// detachedAttr puts the child in its own session so it survives this
// process (no daemon involved).
func detachedAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
