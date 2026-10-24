//go:build !linux && !windows

package ffmpeg

import "syscall"

// setDeathSignal: macOS has no parent-death signal; a process whose parent
// dies sees its pipes close (stdin EOF, SIGPIPE on stdout) and exits.
func setDeathSignal(*syscall.SysProcAttr) {}
