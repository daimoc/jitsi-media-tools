package output

import (
	"context"
	"encoding/binary"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/daimoc/jitsi-media-tools/internal/ffmpeg"
	"github.com/daimoc/jitsi-media-tools/internal/ffmpeg/ffmpegtest"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func TestPlayerArgs(t *testing.T) {
	args := PlayerArgs("Room")
	if !containsRun(args, []string{"-f", "nut", "-i", "pipe:0"}) || !containsRun(args, []string{"-window_title", "Room"}) {
		t.Errorf("player args %v", args)
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

// TestDisplay_WritesGridAndAudio feeds 1 s of grid frames and audio chunks
// and checks the NUT stream the window would play.
func TestDisplay_WritesGridAndAudio(t *testing.T) {
	bin := ffmpegtest.Require(t)
	path := filepath.Join(t.TempDir(), "out.nut")
	out, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	d, err := StartDisplay(context.Background(), DisplayConfig{Width: 320, Height: 180, Logger: quiet, Out: out})
	if err != nil {
		t.Fatal(err)
	}
	frame := make([]byte, 320*180*3/2)
	chunk := make([]byte, ffmpeg.DecodedChunkBytes)
	for i := range chunk {
		chunk[i] = byte(i) // Not silence
	}
	start := time.Now()
	for i := 0; i < 50; i++ { // 1 s: 50 audio chunks, 30 frames
		d.WriteAudio(chunk, time.Now())
		if i%5 < 3 {
			d.WriteVideo(frame, time.Now())
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
	video, audio := 0, 0
	for _, l := range probePackets(t, path) {
		if strings.HasPrefix(l, "0,") {
			video++
		} else {
			audio++
		}
	}
	if video < 25 || audio < 45 {
		t.Errorf("%d video frames and %d audio chunks, want about 30 and 50", video, audio)
	}
}

func TestDisplay_StopLeavesNoProcess(t *testing.T) {
	out, err := os.Create(filepath.Join(t.TempDir(), "out.nut"))
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	before := ffmpeg.Running()
	d, err := StartDisplay(context.Background(), DisplayConfig{Width: 320, Height: 180, Logger: quiet, Out: out})
	if err != nil {
		t.Fatal(err)
	}
	d.Stop()
	if n := ffmpeg.Running(); n != before {
		t.Errorf("%d processes running after Stop, want %d", n, before)
	}
}

// TestDisplay_NoDelay checks that a grid frame reaches the player at once,
// even when audio started at another moment: video is not held back to
// line it up with audio.
func TestDisplay_NoDelay(t *testing.T) {
	bin := ffmpegtest.Require(t)
	const w, h = 320, 180
	r, out, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	// Turns the NUT stream back into raw frames, as the player decodes it.
	back := exec.Command(bin, "-hide_banner", "-loglevel", "error", "-probesize", "32", "-analyzeduration", "0",
		"-f", "nut", "-i", "pipe:0", "-map", "0:v", "-c:v", "rawvideo", "-threads", "1", "-fps_mode", "passthrough", "-f", "rawvideo", "pipe:1")
	back.Stdin = r
	frames, err := back.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := back.Start(); err != nil {
		t.Fatal(err)
	}
	r.Close()
	d, err := StartDisplay(context.Background(), DisplayConfig{Width: w, Height: h, Logger: quiet, Out: out})
	if err != nil {
		t.Fatal(err)
	}
	const n = 90
	var sent [n]atomic.Int64
	go func() {
		defer d.Stop()
		chunk := make([]byte, ffmpeg.DecodedChunkBytes)
		start, f := time.Now(), 0
		for i := 0; f < n; i++ { // 20 ms ticks
			if i >= 10 { // Audio starts 200 ms after video
				d.WriteAudio(chunk, time.Now())
			}
			if i%5 < 3 { // 30 fps
				frame := make([]byte, w*h*3/2)
				binary.LittleEndian.PutUint32(frame, uint32(f)) // Frame number in the first pixels
				sent[f].Store(time.Now().UnixNano())
				d.WriteVideo(frame, time.Now())
				f++
			}
			time.Sleep(time.Until(start.Add(time.Duration(i+1) * 20 * time.Millisecond)))
		}
	}()
	var delays []time.Duration
	buf := make([]byte, w*h*3/2)
	for {
		if _, err := io.ReadFull(frames, buf); err != nil {
			break
		}
		if f := binary.LittleEndian.Uint32(buf); f >= 30 && f < n { // Past the start
			delays = append(delays, time.Since(time.Unix(0, sent[f].Load())))
		}
	}
	back.Wait()
	if len(delays) < 30 {
		t.Fatalf("%d frames read back, want about 60", len(delays))
	}
	slices.Sort(delays)
	if med := delays[len(delays)/2]; med > 25*time.Millisecond {
		t.Errorf("median delay to the player %v, want a few ms", med)
	}
}
