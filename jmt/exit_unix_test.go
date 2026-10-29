//go:build !windows

package jmt

import (
	"bufio"
	"errors"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestExit_CtrlC(t *testing.T) {
	cmd, kids := startHelper(t)
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	select {
	case err := <-exited:
		if err != nil {
			t.Errorf("exit after Ctrl+C: %v, want 0", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the session did not end after Ctrl+C")
	}
	waitGone(t, kids)
}

func TestExit_Killed(t *testing.T) {
	cmd, kids := startHelper(t)
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	var exit *exec.ExitError
	if err := cmd.Wait(); !errors.As(err, &exit) {
		t.Fatalf("wait: %v", err)
	}
	waitGone(t, kids)
}

// startHelper starts TestExit_Helper in its own process and returns it
// with the processes it started, once it has joined and records.
func startHelper(t *testing.T) (*exec.Cmd, []int) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestExit_Helper$")
	cmd.Env = append(os.Environ(), "JMT_EXIT_HELPER="+t.TempDir())
	cmd.Stderr = os.Stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	lines := bufio.NewScanner(out)
	for lines.Scan() && lines.Text() != "joined" {
	}
	go io.Copy(io.Discard, out)
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		if kids := children(t, cmd.Process.Pid); len(kids) > 0 {
			return cmd, kids
		}
	}
	t.Fatal("the session started no process")
	return nil, nil
}

// children returns the processes whose parent is pid.
func children(t *testing.T, pid int) []int {
	out, err := exec.Command("pgrep", "-P", strconv.Itoa(pid)).Output()
	if err != nil {
		if _, ok := err.(*exec.ExitError); ok {
			return nil // None
		}
		t.Skipf("pgrep: %v", err)
	}
	var pids []int
	for _, f := range strings.Fields(string(out)) {
		if n, err := strconv.Atoi(f); err == nil {
			pids = append(pids, n)
		}
	}
	return pids
}

// waitGone fails unless pids all exit within 5 s.
func waitGone(t *testing.T, pids []int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for _, pid := range pids {
		for syscall.Kill(pid, 0) == nil {
			if time.Now().After(deadline) {
				t.Errorf("process %d still running", pid)
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
}
