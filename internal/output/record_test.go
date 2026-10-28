package output

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/daimoc/jitsi-media-tools/internal/compose"
	"github.com/daimoc/jitsi-media-tools/internal/ffmpeg"
	"github.com/daimoc/jitsi-media-tools/internal/ffmpeg/ffmpegtest"
)

func TestRecordPath(t *testing.T) {
	at := time.Date(2026, 10, 28, 9, 5, 7, 0, time.Local)
	for _, c := range []struct{ room, name, want string }{
		{"Standup", "Alice", "Standup-20261028-090507-Alice.mkv"},
		{"Standup", MixedName, "Standup-20261028-090507-mixed.mkv"},
		{"acme/Standup", "../etc/passwd", "acme_Standup-20261028-090507-.._etc_passwd.mkv"},
		{"Room", `a\b:c*d?"e<f>g|h`, "Room-20261028-090507-a_b_c_d__e_f_g_h.mkv"},
		{"Room", "Bob (2)", "Room-20261028-090507-Bob (2).mkv"},
		{"Room", "  ", "Room-20261028-090507-participant.mkv"},
	} {
		got := RecordPath("/rec", c.room, at, c.name)
		if want := filepath.Join("/rec", c.want); got != want {
			t.Errorf("RecordPath(%q, %q) = %q, want %q", c.room, c.name, got, want)
		}
	}
}

func TestRecordArgs(t *testing.T) {
	args := RecordArgs("/rec/a.mkv", 640, 360)
	for _, run := range [][]string{
		{"-f", "nut", "-i", "pipe:0"},
		{"-c:v", "libx264"},
		{"-preset", "veryfast"},
		{"-c:a", "aac"},
		{"-flush_packets", "1"},
		{"-cluster_time_limit", "1000"},
		{"-f", "matroska", "/rec/a.mkv"},
	} {
		if !containsRun(args, run) {
			t.Errorf("record args lack %v: %v", run, args)
		}
	}
}

// probeFile decodes a recording and returns ffmpeg's report and the time
// decoded.
func probeFile(t *testing.T, bin, path string) (string, time.Duration) {
	t.Helper()
	out, err := exec.Command(bin, "-hide_banner", "-i", path, "-f", "null", "-").CombinedOutput()
	if err != nil {
		t.Fatalf("decoding %s: %v\n%s", path, err, out)
	}
	m := regexp.MustCompile(`time=(\d+):(\d+):(\d+\.\d+)`).FindAllStringSubmatch(string(out), -1)
	if m == nil {
		t.Fatalf("no time decoded from %s:\n%s", path, out)
	}
	last := m[len(m)-1]
	d, _ := time.ParseDuration(last[1] + "h" + last[2] + "m" + last[3] + "s")
	return string(out), d
}

// record writes a tile stream for d into a new recording at path.
func record(t *testing.T, bin, path string, w, h int, d time.Duration) *Recording {
	t.Helper()
	r, err := StartRecording(context.Background(), RecordConfig{FFmpeg: bin, Name: "test", Path: path, Width: w, Height: h, Logger: quiet})
	if err != nil {
		t.Fatal(err)
	}
	frame := compose.NewFrame(w, h)
	frame.Fill(160, 90, 200)
	video := time.NewTicker(compose.VideoInterval)
	audio := time.NewTicker(compose.AudioInterval)
	defer video.Stop()
	defer audio.Stop()
	for end := time.After(d); ; {
		select {
		case <-end:
			return r
		case <-video.C:
			r.WriteVideo(frame.Bytes(nil))
		case <-audio.C:
			r.WriteAudio(make([]byte, ffmpeg.DecodedChunkBytes))
		}
	}
}

func recordCaps(t *testing.T) string {
	t.Helper()
	bin := ffmpegtest.Require(t)
	caps, err := ffmpeg.Probe(context.Background(), bin)
	if err != nil {
		t.Fatal(err)
	}
	if err := caps.Missing(RecordNeeds...); err != nil {
		t.Skip(err)
	}
	return bin
}

// TestRecording_PlayableAfterStop checks T057: per-participant and mixed
// recordings are Matroska files with H.264 and AAC covering what was
// written, once stopped as Ctrl+C stops the session.
func TestRecording_PlayableAfterStop(t *testing.T) {
	bin := recordCaps(t)
	dir := t.TempDir()
	at := time.Now()
	for _, c := range []struct {
		name string
		w, h int
	}{{"Alice", 640, 360}, {MixedName, 1280, 720}} {
		path := RecordPath(dir, "Room", at, c.name)
		r := record(t, bin, path, c.w, c.h, 2*time.Second)
		r.Stop()
		report, d := probeFile(t, bin, path)
		if !strings.Contains(report, "matroska") || !strings.Contains(report, "Video: h264") || !strings.Contains(report, "Audio: aac") {
			t.Fatalf("%s:\n%s", path, report)
		}
		if size := fmt.Sprintf("%dx%d", c.w, c.h); !strings.Contains(report, size) {
			t.Errorf("%s not %s:\n%s", path, size, report)
		}
		if d < 1500*time.Millisecond {
			t.Errorf("%s holds %v, want about 2 s", path, d)
		}
	}
	if n := ffmpeg.Running(); n != 0 {
		t.Errorf("%d processes after Stop", n)
	}
}

// A recording whose ffmpeg is killed (the tool killed hard) keeps what was
// written up to the last flushed cluster.
func TestRecording_PlayableAfterKill(t *testing.T) {
	bin := recordCaps(t)
	path := filepath.Join(t.TempDir(), "killed.mkv")
	r := record(t, bin, path, 320, 180, 3*time.Second)
	r.p.Kill()
	r.Stop()
	if _, d := probeFile(t, bin, path); d < time.Second {
		t.Errorf("killed recording holds %v, want at least 1 s of the 3 s written", d)
	}
}

// A directory that cannot be written fails at start, not later.
func TestRecording_UnwritableDir(t *testing.T) {
	bin := ffmpegtest.Require(t)
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := StartRecording(context.Background(), RecordConfig{FFmpeg: bin, Path: filepath.Join(file, "a.mkv"), Width: 320, Height: 180, Logger: quiet}); err == nil {
		t.Fatal("recording into a file's path started")
	}
}
