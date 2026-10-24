//go:build windows

package ffmpeg

import (
	"os/exec"
	"unsafe"

	"golang.org/x/sys/windows"
)

// processGroup is a Job Object that kills its processes when its last
// handle closes, which also happens when the tool dies (SC-008).
type processGroup struct{ job windows.Handle }

func setProcAttr(*exec.Cmd) {}

func newProcessGroup(cmd *exec.Cmd) (processGroup, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return processGroup{}, err
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE},
	}
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		windows.CloseHandle(job)
		return processGroup{}, err
	}
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err != nil {
		windows.CloseHandle(job)
		return processGroup{}, err
	}
	defer windows.CloseHandle(h)
	if err := windows.AssignProcessToJobObject(job, h); err != nil {
		windows.CloseHandle(job)
		return processGroup{}, err
	}
	return processGroup{job: job}, nil
}

func (g processGroup) kill(cmd *exec.Cmd) {
	if g.job != 0 {
		_ = windows.TerminateJobObject(g.job, 1)
		return
	}
	_ = cmd.Process.Kill()
}

func (g processGroup) close() {
	if g.job != 0 {
		windows.CloseHandle(g.job)
	}
}
