package ffmpeg_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/daimoc/jitsi-media-tools/internal/ffmpeg"
	"github.com/daimoc/jitsi-media-tools/internal/ffmpeg/ffmpegtest"
)

// encodeTo runs ffmpeg with args and returns the file it wrote.
func encodeTo(t *testing.T, bin, name string, args ...string) []byte {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	args = append([]string{"-hide_banner", "-loglevel", "error"}, append(args, path)...)
	if out, err := exec.Command(bin, args...).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v: %s", err, out)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// ivfFrames splits an IVF file into its header and frames (each with its
// 12-byte frame header), adding shift to the timestamps.
func ivfFrames(t *testing.T, b []byte, shift uint64) ([]byte, [][]byte) {
	t.Helper()
	header, rest := b[:32], b[32:]
	var frames [][]byte
	for len(rest) >= 12 {
		n := 12 + int(binary.LittleEndian.Uint32(rest))
		f := append([]byte(nil), rest[:n]...)
		binary.LittleEndian.PutUint64(f[4:], binary.LittleEndian.Uint64(f[4:])+shift)
		frames, rest = append(frames, f), rest[n:]
	}
	if len(rest) != 0 {
		t.Fatalf("truncated IVF: %d bytes left", len(rest))
	}
	return header, frames
}

// drain counts the decoder's frames until its output ends, checking their
// size.
func drain(t *testing.T, d *ffmpeg.Decoder, size int) int {
	t.Helper()
	n := 0
	timeout := time.After(30 * time.Second)
	for {
		select {
		case f, ok := <-d.Frames():
			if !ok {
				return n
			}
			if len(f) != size {
				t.Fatalf("frame %d: %d bytes, want %d", n, len(f), size)
			}
			n++
		case <-timeout:
			t.Fatal("decoder output did not end")
		}
	}
}

// TestVideoDecoder_FixedSize checks that a VP8 stream whose size changes
// (as when the bridge switches layers) decodes to frames of one fixed size.
func TestVideoDecoder_FixedSize(t *testing.T) {
	bin := ffmpegtest.Require(t)
	vp8 := func(size string) []byte {
		return encodeTo(t, bin, "v.ivf", "-f", "lavfi", "-i", "testsrc2=size="+size+":rate=30:duration=1",
			"-c:v", "libvpx", "-deadline", "realtime", "-g", "30", "-f", "ivf")
	}
	// One IVF stream: the second file's frames follow the first's, with
	// continuing timestamps, as the receiver writes after a layer switch.
	header, frames := ivfFrames(t, vp8("640x360"), 0)
	_, more := ivfFrames(t, vp8("320x180"), 30)
	frames = append(frames, more...)

	d, err := ffmpeg.StartVideoDecoder(context.Background(), ffmpeg.DecoderConfig{Bin: bin, Format: "ivf", Height: 180, Logger: testLogger})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Stop()
	if d.Width() != 320 || d.Height() != 180 {
		t.Fatalf("size %dx%d, want 320x180", d.Width(), d.Height())
	}
	go func() {
		d.Write(header)
		for _, f := range frames { // Paced like a live stream
			d.Write(f)
			time.Sleep(5 * time.Millisecond)
		}
		d.CloseInput()
	}()
	if n := drain(t, d, 320*180*3/2); n < 58 || n > 60 {
		t.Errorf("%d frames, want about 60", n)
	}
}

func TestAudioDecoder_PCM(t *testing.T) {
	bin := ffmpegtest.Require(t)
	ogg := encodeTo(t, bin, "a.ogg", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration=1",
		"-c:a", "libopus", "-f", "ogg")

	d, err := ffmpeg.StartAudioDecoder(context.Background(), ffmpeg.DecoderConfig{Bin: bin, Format: "ogg", Logger: testLogger})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Stop()
	go func() {
		d.Write(ogg)
		d.CloseInput()
	}()
	// 20 ms chunks of 48 kHz stereo s16le: 960 * 2 * 2 bytes.
	if n := drain(t, d, 3840); n < 48 || n > 52 {
		t.Errorf("%d chunks, want about 50", n)
	}
}

// TestDecoder_StopEndsFrames checks that Stop ends a decoder whose input
// is still open.
func TestDecoder_StopEndsFrames(t *testing.T) {
	bin := ffmpegtest.Require(t)
	d, err := ffmpeg.StartAudioDecoder(context.Background(), ffmpeg.DecoderConfig{Bin: bin, Format: "ogg", Logger: testLogger})
	if err != nil {
		t.Fatal(err)
	}
	d.Stop()
	select {
	case <-d.Frames():
	case <-time.After(5 * time.Second):
		t.Fatal("frames not closed after Stop")
	}
	if _, err := d.Write(bytes.Repeat([]byte{0}, 10)); err == nil {
		t.Error("write after Stop succeeded")
	}
}
