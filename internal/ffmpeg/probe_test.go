package ffmpeg

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/daimoc/jitsi-media-tools/internal/ffmpeg/ffmpegtest"
)

func readTestdata(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func recordedCaps(t *testing.T, version string) *Caps {
	t.Helper()
	c, err := parseCaps(readTestdata(t, version), readTestdata(t, "encoders.txt"), readTestdata(t, "decoders.txt"),
		readTestdata(t, "protocols.txt"), readTestdata(t, "devices.txt"))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestParseCaps_Recorded(t *testing.T) {
	c := recordedCaps(t, "version.txt")
	if c.Major != 6 || c.Version != "6.1.1-3ubuntu5" {
		t.Errorf("version %q major %d, want 6.1.1-3ubuntu5 / 6", c.Version, c.Major)
	}
	for _, need := range []string{NeedVP8Encoder, NeedOpusEncoder, NeedH264Encoder, NeedAACEncoder, NeedSRT, NeedRTMP, NeedTCP,
		"decoder:vp8", "decoder:vp9", "decoder:h264", "decoder:opus", "device:v4l2", "device:pulse", "device:alsa"} {
		if err := c.Missing(need); err != nil {
			t.Errorf("%s: %v", need, err)
		}
	}
	err := c.Missing("device:dshow", "encoder:libsvtav1")
	if err == nil || !strings.Contains(err.Error(), "device dshow") || !strings.Contains(err.Error(), "encoder libsvtav1") {
		t.Errorf("Missing = %v, want both needs named", err)
	}
}

func TestParseCaps_Versions(t *testing.T) {
	for file, major := range map[string]int{"version-gyan.txt": 7, "version-git.txt": 0, "version-old.txt": 4} {
		if c := recordedCaps(t, file); c.Major != major {
			t.Errorf("%s: major %d, want %d", file, c.Major, major)
		}
	}
	if err := recordedCaps(t, "version-old.txt").Missing(); err == nil || !strings.Contains(err.Error(), "ffmpeg 6 or newer") {
		t.Errorf("ffmpeg 4: Missing = %v, want a version error", err)
	}
	if _, err := parseCaps("hello", "", "", "", ""); err == nil {
		t.Error("expected an error for a non-ffmpeg version output")
	}
}

func TestLocate_Missing(t *testing.T) {
	if _, err := Locate("ffmpeg", "/nonexistent/ffmpeg"); err == nil || !strings.Contains(err.Error(), "ffmpeg not found") {
		t.Errorf("Locate = %v, want a not-found error naming ffmpeg", err)
	}
}

func TestProbe_Real(t *testing.T) {
	bin := ffmpegtest.Require(t)
	c, err := Probe(context.Background(), bin)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Missing("decoder:vp8", "decoder:opus", NeedTCP); err != nil {
		t.Error(err)
	}
}
