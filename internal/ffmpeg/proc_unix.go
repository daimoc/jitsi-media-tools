//go:build !windows

package ffmpeg

import (
	"os/exec"
	"syscall"
)

// processGroup is the process's own process group, so killing it also
// kills what it started.
type processGroup struct{ pgid int }

func setProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	setDeathSignal(cmd.SysProcAttr)
}

func newProcessGroup(cmd *exec.Cmd) (processGroup, error) {
	return processGroup{pgid: cmd.Process.Pid}, nil
}

func (g processGroup) kill(cmd *exec.Cmd) {
	if g.pgid > 0 {
		_ = syscall.Kill(-g.pgid, syscall.SIGKILL)
		return
	}
	_ = cmd.Process.Kill()
}

func (g processGroup) close() {}
