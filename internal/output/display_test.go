package output

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
	"time"

	"github.com/daimoc/jitsi-media-tools/internal/ffmpeg"
	"github.com/daimoc/jitsi-media-tools/internal/ffmpeg/ffmpegtest"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func TestDisplayArgs(t *testing.T) {
	args := DisplayArgs(640, 360, "tcp://127.0.0.1:5000")
	want := []string{"-f", "rawvideo", "-pix_fmt", "yuv420p", "-s", "640x360", "-r", "30", "-i", "pipe:0"}
	if !containsRun(args, want) {
		t.Errorf("video input %v not in %v", want, args)
	}
	if !containsRun(args, []string{"-f", "s16le", "-ar", "48000", "-ac", "2", "-i", "tcp://127.0.0.1:5000"}) {
		t.Errorf("audio input missing: %v", args)
	}
	if !containsRun(args, []string{"-f", "nut", "pipe:1"}) {
		t.Errorf("nut output missing: %v", args)
	}
}

func containsRun(s, run []string) bool {
	for i := 0; i+len(run) <= len(s); i++ {
		match := true
		for j := range run {
			if s[i+j] != run[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

// TestDisplay_MuxesGridAndAudio feeds 1 s of grid frames and audio chunks
// and checks the NUT stream the window would play.
func TestDisplay_MuxesGridAndAudio(t *testing.T) {
	bin := ffmpegtest.Require(t)
	path := filepath.Join(t.TempDir(), "out.nut")
	out, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	d, err := StartDisplay(context.Background(), DisplayConfig{FFmpeg: bin, Width: 320, Height: 180, Logger: quiet, Out: out})
	if err != nil {
		t.Fatal(err)
	}
	out.Close() // The display holds its own copy
	frame := make([]byte, 320*180*3/2)
	chunk := make([]byte, ffmpeg.DecodedChunkBytes)
	for i := range chunk {
		chunk[i] = byte(i) // Not silence
	}
	start := time.Now()
	for i := 0; i < 50; i++ { // 1 s: 50 audio chunks, 30 frames
		d.WriteAudio(chunk)
		if i%5 < 3 {
			d.WriteVideo(frame)
		}
		time.Sleep(time.Until(start.Add(time.Duration(i+1) * 20 * time.Millisecond)))
	}
	d.Stop()

	info, err := exec.Command(bin, "-hide_banner", "-i", path, "-f", "null", "-").CombinedOutput()
	if err != nil {
		t.Fatalf("reading the output: %v: %s", err, info)
	}
	s := string(info)
	if !regexp.MustCompile(`Video: rawvideo .*320x180`).MatchString(s) || !regexp.MustCompile(`Audio: pcm_s16le.*48000 Hz, stereo`).MatchString(s) {
		t.Fatalf("streams not as expected:\n%s", s)
	}
	m := regexp.MustCompile(`frame=\s*(\d+)`).FindAllStringSubmatch(s, -1)
	if len(m) == 0 {
		t.Fatalf("no frame count:\n%s", s)
	}
	if n, _ := strconv.Atoi(m[len(m)-1][1]); n < 20 {
		t.Errorf("%d video frames, want about 30", n)
	}
}

func TestDisplay_StopLeavesNoProcess(t *testing.T) {
	bin := ffmpegtest.Require(t)
	out, err := os.Create(filepath.Join(t.TempDir(), "out.nut"))
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	before := ffmpeg.Running()
	d, err := StartDisplay(context.Background(), DisplayConfig{FFmpeg: bin, Width: 320, Height: 180, Logger: quiet, Out: out})
	if err != nil {
		t.Fatal(err)
	}
	d.Stop()
	if n := ffmpeg.Running(); n != before {
		t.Errorf("%d processes running after Stop, want %d", n, before)
	}
}
