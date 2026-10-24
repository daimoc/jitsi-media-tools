// Package ffmpegtest helps tests that need a real ffmpeg.
package ffmpegtest

import (
	"os/exec"
	"testing"
)

// Require returns the ffmpeg executable in PATH, or skips the test when
// there is none.
func Require(t testing.TB) string {
	t.Helper()
	bin, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	return bin
}

// RequireFFplay returns the ffplay executable in PATH, or skips the test.
func RequireFFplay(t testing.TB) string {
	t.Helper()
	bin, err := exec.LookPath("ffplay")
	if err != nil {
		t.Skip("ffplay not installed")
	}
	return bin
}
