package ffmpeg_test

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/daimoc/jitsi-media-tools/internal/ffmpeg"
	"github.com/daimoc/jitsi-media-tools/internal/ffmpeg/ffmpegtest"
)

// makeClip writes a short clip with the given lavfi inputs to dir/name.
func makeClip(t *testing.T, bin, dir, name string, inputs ...string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	args := []string{"-hide_banner", "-loglevel", "error"}
	for _, in := range inputs {
		args = append(args, "-f", "lavfi", "-i", in)
	}
	args = append(args, "-t", "1", "-c:v", "libvpx", "-c:a", "libopus", path)
	if out, err := exec.Command(bin, args...).CombinedOutput(); err != nil {
		t.Fatalf("making %s: %v: %s", name, err, out)
	}
	return path
}

func TestFileStreams(t *testing.T) {
	bin := ffmpegtest.Require(t)
	dir := t.TempDir()
	for _, tc := range []struct {
		name   string
		inputs []string
		want   ffmpeg.Streams
	}{
		{"both.webm", []string{"testsrc2=size=320x180:rate=30", "sine"}, ffmpeg.Streams{Audio: true, Video: true}},
		{"audio.webm", []string{"sine"}, ffmpeg.Streams{Audio: true}},
		{"video.webm", []string{"testsrc2=size=320x180:rate=30"}, ffmpeg.Streams{Video: true}},
	} {
		got, err := ffmpeg.FileStreams(context.Background(), bin, makeClip(t, bin, dir, tc.name, tc.inputs...))
		if err != nil || got != tc.want {
			t.Errorf("%s: %+v, %v; want %+v", tc.name, got, err, tc.want)
		}
	}
	if _, err := ffmpeg.FileStreams(context.Background(), bin, filepath.Join(dir, "missing.webm")); err == nil {
		t.Error("missing file: no error")
	}
}
