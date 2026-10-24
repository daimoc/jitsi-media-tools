//go:build linux

package ffmpeg

import "syscall"

// setDeathSignal has the kernel kill the process if the tool dies without
// stopping it (SC-008).
func setDeathSignal(a *syscall.SysProcAttr) { a.Pdeathsig = syscall.SIGKILL }
