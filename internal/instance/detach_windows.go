package instance

import "syscall"

// detachedAttr starts the child in a new detached process group with no
// inherited handles so it survives this process.
func detachedAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		CreationFlags:    syscall.CREATE_NEW_PROCESS_GROUP,
		HideWindow:       true,
		NoInheritHandles: true,
	}
}
