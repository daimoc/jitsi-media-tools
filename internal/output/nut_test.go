package output

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/daimoc/jitsi-media-tools/internal/ffmpeg"
	"github.com/daimoc/jitsi-media-tools/internal/ffmpeg/ffmpegtest"
)

// probePackets lists a file's packets as "stream,pts_time" lines. Warnings
// on stderr are left out: a NUT file without an index gets "read_timestamp
// failed" when ffprobe looks for its duration.
func probePackets(t *testing.T, path string) []string {
	t.Helper()
	probe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe not installed")
	}
	out, err := exec.Command(probe, "-v", "error", "-show_entries", "packet=stream_index,pts_time", "-of", "csv=p=0", path).Output()
	if err != nil {
		t.Fatalf("ffprobe: %v", err)
	}
	return strings.Fields(string(out))
}

func TestNUTWriter_ReadByFFmpeg(t *testing.T) {
	bin := ffmpegtest.Require(t)
	var buf bytes.Buffer
	w := newNUTWriter(&buf, 320, 180)
	frame := make([]byte, 320*180*3/2)
	chunk := make([]byte, ffmpeg.DecodedChunkBytes)
	// Audio from 0, video from 0.5 s: each packet keeps its own time.
	for i := 0; i < 50; i++ {
		at := time.Duration(i) * 20 * time.Millisecond
		if err := w.audio(at, chunk); err != nil {
			t.Fatal(err)
		}
		if at >= 500*time.Millisecond && i%5 < 3 {
			if err := w.video(at, frame); err != nil {
				t.Fatal(err)
			}
		}
	}
	path := filepath.Join(t.TempDir(), "out.nut")
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	info, err := exec.Command(bin, "-hide_banner", "-i", path, "-f", "null", "-").CombinedOutput()
	if err != nil {
		t.Fatalf("ffmpeg cannot read the stream: %v: %s", err, info)
	}
	if s := string(info); !strings.Contains(s, "Video: rawvideo") || !strings.Contains(s, "320x180") || !strings.Contains(s, "Audio: pcm_s16le") || !strings.Contains(s, "48000 Hz, stereo") {
		t.Fatalf("streams not as written:\n%s", s)
	}
	var video, audio []float64
	for _, l := range probePackets(t, path) {
		stream, ts, _ := strings.Cut(l, ",")
		v, err := strconv.ParseFloat(ts, 64)
		if err != nil {
			t.Fatalf("packet %q: %v", l, err)
		}
		if stream == "0" {
			video = append(video, v)
		} else {
			audio = append(audio, v)
		}
	}
	if len(video) != 15 || len(audio) != 50 {
		t.Fatalf("%d video and %d audio packets, want 15 and 50", len(video), len(audio))
	}
	if video[0] != 0.5 || audio[0] != 0 || audio[49] != 0.98 {
		t.Errorf("first video at %v, audio from %v to %v; want 0.5, 0 and 0.98", video[0], audio[0], audio[49])
	}
}

func TestAudioNUT_OnePacketPerChunk(t *testing.T) {
	bin := ffmpegtest.Require(t)
	var buf bytes.Buffer
	w := NewAudioNUT(&buf)
	chunk := make([]byte, ffmpeg.DecodedChunkBytes)
	for i := 0; i < 10; i++ {
		if _, err := w.Write(chunk); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(t.TempDir(), "audio.nut")
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := exec.Command(bin, "-hide_banner", "-i", path, "-f", "null", "-").CombinedOutput()
	if err != nil {
		t.Fatalf("ffmpeg cannot read the stream: %v: %s", err, info)
	}
	if s := string(info); strings.Contains(s, "Video:") || !strings.Contains(s, "Audio: pcm_s16le") || !strings.Contains(s, "48000 Hz, stereo") {
		t.Fatalf("streams not as written:\n%s", s)
	}
	pkts := probePackets(t, path)
	if len(pkts) != 10 || pkts[0] != "0,0.000000" || pkts[9] != "0,0.180000" {
		t.Errorf("packets %v, want 10 on stream 0 from 0 to 0.18 s", pkts)
	}
}
