//go:build windows

package cmd

import "syscall"

// detachSysProcAttr is the Windows counterpart to the Setsid call on
// POSIX. CREATE_NEW_PROCESS_GROUP detaches the drainer from the parent's
// console control events; HideWindow keeps a console window from
// flashing up when the hook fires.
//
// Windows is a cross-compile target for the release matrix rather than a
// tested platform, so this is deliberately the most conservative pair of
// flags that achieves detachment.
func detachSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP,
		HideWindow:    true,
	}
}
