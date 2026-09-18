//go:build !windows

package cmd

import "syscall"

// detachSysProcAttr puts the drainer in its own session, so it survives
// the hook process exiting and is not signalled when the client's
// process group is torn down at session end. Without Setsid a
// Ctrl-C-style group signal to the client would kill a drain that is
// deliberately meant to outlive it.
func detachSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
