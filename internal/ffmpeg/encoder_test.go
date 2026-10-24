package ffmpeg_test

import (
	"context"
	"io"
	"log/slog"
	"slices"
	"testing"
	"time"

	"github.com/daimoc/jitsi-media-tools/internal/ffmpeg"
	"github.com/daimoc/jitsi-media-tools/internal/ffmpeg/ffmpegtest"
	"github.com/daimoc/jitsi-media-tools/internal/rtpmedia"
)

var testLogger = slog.New(slog.NewTextHandler(io.Discard, nil))

// lavfiInput is 3 s of test pattern and tone, read as fast as possible.
var lavfiInput = []string{
	"-f", "lavfi", "-i", "testsrc2=size=640x360:rate=30:duration=3",
	"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration=3",
}

// collect drains a channel until it closes.
func collect(t *testing.T, ch <-chan rtpmedia.Frame) []rtpmedia.Frame {
	t.Helper()
	var out []rtpmedia.Frame
	timeout := time.After(30 * time.Second)
	for {
		select {
		case f, ok := <-ch:
			if !ok {
				return out
			}
			out = append(out, f)
		case <-timeout:
			t.Fatal("encoder output did not end")
		}
	}
}

func TestEncoder_AudioAndVideo(t *testing.T) {
	bin := ffmpegtest.Require(t)
	enc, err := ffmpeg.StartEncoder(context.Background(), ffmpeg.EncoderConfig{
		Bin: bin, Input: lavfiInput, Logger: testLogger,
		Options: ffmpeg.EncodeOptions{Audio: true, Video: true, Width: 1280, Height: 720},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer enc.Stop()

	videoDone := make(chan []rtpmedia.Frame)
	go func() { videoDone <- collect(t, enc.Video()) }()
	audio := collect(t, enc.Audio())
	video := <-videoDone
	<-enc.Done()
	if err := enc.Err(); err != nil {
		t.Fatalf("encoder: %v", err)
	}

	if n := len(video); n < 85 || n > 95 {
		t.Errorf("%d video frames, want about 90", n)
	}
	if len(video) > 0 && !video[0].Keyframe {
		t.Error("first video frame is not a keyframe")
	}
	gap := 0
	for _, f := range video {
		if f.Keyframe {
			gap = 0
			continue
		}
		if gap++; gap >= 60 {
			t.Fatal("no keyframe for 2 s")
		}
	}
	if n := len(audio); n < 140 || n > 155 {
		t.Errorf("%d Opus packets, want about 150 (20 ms each)", n)
	}
	for _, f := range audio {
		if d := opusDuration(f.Data); d != 20*time.Millisecond {
			t.Fatalf("Opus packet of %v, want 20ms", d)
		}
	}
}

func TestEncoder_AudioOnly(t *testing.T) {
	bin := ffmpegtest.Require(t)
	enc, err := ffmpeg.StartEncoder(context.Background(), ffmpeg.EncoderConfig{
		Bin: bin, Input: lavfiInput, Logger: testLogger,
		Options: ffmpeg.EncodeOptions{Audio: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer enc.Stop()
	if enc.Video() != nil {
		t.Error("audio-only encoder has a video channel")
	}
	if n := len(collect(t, enc.Audio())); n < 140 {
		t.Errorf("%d Opus packets, want about 150", n)
	}
}

func TestEncoderArgs(t *testing.T) {
	args := ffmpeg.EncoderArgs([]string{"-re", "-i", "clip.mp4"},
		ffmpeg.EncodeOptions{Audio: true, Video: true, Width: 640, Height: 360}, "tcp://127.0.0.1:9")
	for _, want := range []string{"libvpx", "libopus", "ivf", "ogg", "pipe:1", "tcp://127.0.0.1:9"} {
		if !slices.Contains(args, want) {
			t.Errorf("args lack %q: %v", want, args)
		}
	}
	if args[0] != "-hide_banner" || !slices.Contains(args, "clip.mp4") {
		t.Errorf("unexpected args %v", args)
	}
}

// opusDuration is the duration of an Opus packet from its TOC byte (RFC
// 6716 3.1), for code 0 packets (one frame).
func opusDuration(p []byte) time.Duration {
	if len(p) == 0 {
		return 0
	}
	config := p[0] >> 3
	var frame time.Duration
	switch {
	case config < 12: // SILK: 10, 20, 40, 60 ms
		frame = []time.Duration{10, 20, 40, 60}[config%4] * time.Millisecond
	case config < 16: // Hybrid: 10, 20 ms
		frame = []time.Duration{10, 20}[config%2] * time.Millisecond
	default: // CELT: 2.5, 5, 10, 20 ms
		frame = []time.Duration{2500, 5000, 10000, 20000}[config%4] * time.Microsecond
	}
	switch p[0] & 3 {
	case 1, 2:
		return 2 * frame
	case 3:
		if len(p) < 2 {
			return 0
		}
		return time.Duration(p[1]&0x3f) * frame
	}
	return frame
}
