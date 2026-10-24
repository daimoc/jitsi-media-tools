package ffmpeg

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

var quietLogger = slog.New(slog.NewTextHandler(io.Discard, nil))

// TestMain lets the test binary stand in for ffmpeg: with JMT_HELPER set it
// behaves as asked instead of running tests.
func TestMain(m *testing.M) {
	switch os.Getenv("JMT_HELPER") {
	case "":
		os.Exit(m.Run())
	case "echo": // Copies stdin to stdout, exits on stdin EOF
		io.Copy(os.Stdout, os.Stdin)
		os.Exit(0)
	case "hang": // Ignores stdin EOF
		fmt.Fprintln(os.Stderr, "hanging")
		time.Sleep(time.Hour)
	case "fail":
		fmt.Fprintln(os.Stderr, "something went wrong")
		os.Exit(3)
	}
}

func helper(mode string) Spec {
	os.Setenv("JMT_HELPER", mode) // Inherited by the child; tests using helper do not run in parallel
	return Spec{Bin: os.Args[0], Name: "helper-" + mode, Logger: quietLogger}
}

func TestProcess_StdinStdout(t *testing.T) {
	s := helper("echo")
	s.Stdin, s.Stdout = true, true
	p, err := Start(context.Background(), s)
	os.Unsetenv("JMT_HELPER")
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintln(p.Stdin, "hello")
	line, err := bufio.NewReader(p.Stdout).ReadString('\n')
	if err != nil || line != "hello\n" {
		t.Fatalf("read %q, %v; want hello", line, err)
	}
	p.Stop()
	if p.Err() != nil {
		t.Errorf("clean exit expected, got %v", p.Err())
	}
}

func TestProcess_StopKillsAfterGrace(t *testing.T) {
	before := Running()
	s := helper("hang")
	s.Stdin = true
	p, err := Start(context.Background(), s)
	os.Unsetenv("JMT_HELPER")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	p.Stop()
	if d := time.Since(start); d < stopGrace || d > stopGrace+3*time.Second {
		t.Errorf("Stop took %v, want about %v", d, stopGrace)
	}
	if Running() != before {
		t.Errorf("%d processes running after Stop, want %d", Running(), before)
	}
}

func TestProcess_ContextCancelStops(t *testing.T) {
	before := Running()
	ctx, cancel := context.WithCancel(context.Background())
	s := helper("echo")
	s.Stdin = true
	p, err := Start(ctx, s)
	os.Unsetenv("JMT_HELPER")
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case <-p.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("process still running after context cancel")
	}
	if Running() != before {
		t.Errorf("%d processes running, want %d", Running(), before)
	}
}

func TestProcess_ErrorHasStderr(t *testing.T) {
	p, err := Start(context.Background(), helper("fail"))
	os.Unsetenv("JMT_HELPER")
	if err != nil {
		t.Fatal(err)
	}
	<-p.Done()
	if p.Err() == nil || !strings.Contains(p.Err().Error(), "something went wrong") {
		t.Errorf("Err = %v, want the stderr tail", p.Err())
	}
}

func TestSupervise_Restarts(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	os.Setenv("JMT_HELPER", "fail")
	defer os.Unsetenv("JMT_HELPER")
	var exits atomic.Int32
	done := make(chan struct{})
	go func() {
		defer close(done)
		Supervise(ctx, quietLogger, "fail", func(ctx context.Context) (*Process, error) {
			s := Spec{Bin: os.Args[0], Name: "fail", Logger: quietLogger}
			return Start(ctx, s)
		}, func(error) {
			if exits.Add(1) == 2 {
				cancel()
			}
		})
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		cancel()
		t.Fatal("Supervise did not restart twice")
	}
	if exits.Load() < 2 {
		t.Errorf("%d exits, want 2", exits.Load())
	}
}

func TestLoopback_Accept(t *testing.T) {
	l, err := Listen()
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if !strings.HasPrefix(l.URL(), "tcp://127.0.0.1:") {
		t.Fatalf("URL %s", l.URL())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := l.Accept(ctx); err == nil {
		t.Fatal("Accept without a caller should fail when ctx ends")
	}
}
