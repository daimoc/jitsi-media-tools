package jmt

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"syscall"
	"testing"
	"time"

	"github.com/daimoc/jitsi-media-tools/internal/ffmpeg"
)

// Exits of a recording session (spec edge cases, SC-008): no helper
// process is left running after a lost connection, Ctrl+C or a kill.

func recording(dir string) Options {
	return Options{MeetingURL: "https://meet.test/Room", DisplayName: "Rec", Logger: quiet, Record: &RecordOptions{Dir: dir}}
}

// waitRunning waits until this process runs ffmpeg processes.
func waitRunning(t *testing.T) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); ffmpeg.Running() == 0; time.Sleep(50 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("no ffmpeg process started")
		}
	}
}

func TestExit_LostConnection(t *testing.T) {
	f := fakeRoom(t, true)
	s, err := Join(context.Background(), recording(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	waitRunning(t)
	f.Handler().OnSignalingLost(errors.New("connection lost"))
	select {
	case <-s.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("the session did not end after the connection was lost")
	}
	if s.Wait() == nil {
		t.Error("Wait returned nil after the connection was lost; the CLI would exit 0")
	}
	if n := ffmpeg.Running(); n != 0 {
		t.Errorf("%d processes after the session ended", n)
	}
}

// TestExit_Helper runs a recording session the way the CLI does, in a
// process of its own, recording into $JMT_EXIT_HELPER; the tests in
// exit_unix_test.go start it.
func TestExit_Helper(t *testing.T) {
	dir := os.Getenv("JMT_EXIT_HELPER")
	if dir == "" {
		t.Skip("run by other tests")
	}
	fakeRoom(t, true)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	s, err := Join(ctx, recording(dir))
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout.WriteString("joined\n")
	<-ctx.Done()
	s.Close()
	if err := s.Wait(); err != nil {
		t.Fatal(err)
	}
	os.Exit(0)
}
