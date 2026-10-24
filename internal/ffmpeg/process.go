package ffmpeg

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// stopGrace is how long Stop waits for a process to exit after closing its
// stdin before killing it.
const stopGrace = 2 * time.Second

// running counts the processes started and not yet reaped, for tests of
// constitution IV ("none may outlive the session").
var running atomic.Int64

// Running returns the number of processes started by this package that
// have not exited yet.
func Running() int { return int(running.Load()) }

// Spec describes a process to start.
type Spec struct {
	Bin    string
	Args   []string
	Name   string // For logs
	Stdin  bool   // Give the process a stdin pipe (Process.Stdin)
	Stdout bool   // Give the process a stdout pipe (Process.Stdout)
	// StdoutTo, when set, receives the process's stdout instead (e.g. the
	// stdin of a second process). Exclusive with Stdout.
	StdoutTo io.Writer
	// StdinFrom, when set, is the process's stdin (e.g. another process's
	// stdout). Exclusive with Stdin.
	StdinFrom io.Reader
	Logger    *slog.Logger
}

// Process is a running ffmpeg or ffplay.
type Process struct {
	Stdin  io.WriteCloser // Nil unless Spec.Stdin
	Stdout io.ReadCloser  // Nil unless Spec.Stdout

	cmd    *exec.Cmd
	name   string
	logger *slog.Logger
	group  processGroup

	done     chan struct{}
	err      error // Set before done is closed
	stopOnce sync.Once

	tailMu sync.Mutex
	tail   []string // Last stderr lines, for error messages
}

// Start starts the process. It is stopped (Stop) when ctx ends.
func Start(ctx context.Context, s Spec) (*Process, error) {
	if s.Logger == nil {
		s.Logger = slog.Default()
	}
	if s.Name == "" {
		s.Name = s.Bin
	}
	cmd := exec.Command(s.Bin, s.Args...)
	p := &Process{cmd: cmd, name: s.Name, logger: s.Logger.With(slog.String("process", s.Name)), done: make(chan struct{})}
	setProcAttr(cmd)
	var err error
	switch {
	case s.Stdin:
		if p.Stdin, err = cmd.StdinPipe(); err != nil {
			return nil, err
		}
	case s.StdinFrom != nil:
		cmd.Stdin = s.StdinFrom
	}
	switch {
	case s.Stdout:
		if p.Stdout, err = cmd.StdoutPipe(); err != nil {
			return nil, err
		}
	case s.StdoutTo != nil:
		cmd.Stdout = s.StdoutTo
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	p.logger.Debug("Starting", slog.String("bin", s.Bin), slog.String("args", strings.Join(s.Args, " ")))
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting %s: %w", s.Name, err)
	}
	running.Add(1)
	p.group, err = newProcessGroup(cmd)
	if err != nil {
		p.logger.Warn("Process not attached to a kill group", slog.String("error", err.Error()))
	}

	logged := make(chan struct{})
	go func() {
		defer close(logged)
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			line := sc.Text()
			p.logger.Debug(line)
			p.tailMu.Lock()
			p.tail = append(p.tail, line)
			if len(p.tail) > 10 {
				p.tail = p.tail[1:]
			}
			p.tailMu.Unlock()
		}
	}()
	go func() {
		<-logged // Wait must follow the stderr reads
		err := cmd.Wait()
		running.Add(-1)
		p.group.close()
		if err != nil {
			err = fmt.Errorf("%s exited: %w%s", p.name, err, p.stderrTail())
		}
		p.err = err
		close(p.done)
	}()
	go func() {
		select {
		case <-ctx.Done():
			p.Stop()
		case <-p.done:
		}
	}()
	return p, nil
}

func (p *Process) stderrTail() string {
	p.tailMu.Lock()
	defer p.tailMu.Unlock()
	if len(p.tail) == 0 {
		return ""
	}
	return ": " + strings.Join(p.tail, " | ")
}

// Done is closed when the process has exited.
func (p *Process) Done() <-chan struct{} { return p.done }

// Err returns why the process exited (nil for a clean exit); valid after
// Done is closed.
func (p *Process) Err() error { return p.err }

// Stop ends the process: it closes stdin (ffmpeg then finishes its outputs),
// waits up to stopGrace, then kills it and its children. It returns once
// the process has exited and is safe to call several times.
func (p *Process) Stop() {
	p.stopOnce.Do(func() {
		if p.Stdin != nil {
			p.Stdin.Close()
		}
		select {
		case <-p.done:
			return
		case <-time.After(stopGrace):
		}
		p.logger.Debug("Killing after the grace period")
		p.group.kill(p.cmd)
	})
	<-p.done
}

// Kill ends the process at once.
func (p *Process) Kill() {
	p.stopOnce.Do(func() { p.group.kill(p.cmd) })
	<-p.done
}

// ErrStopped is returned by Supervise when its context ends.
var ErrStopped = errors.New("stopped")
