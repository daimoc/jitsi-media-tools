package output

import (
	"context"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/daimoc/jitsi-media-tools/internal/compose"
	"github.com/daimoc/jitsi-media-tools/internal/ffmpeg"
	"github.com/daimoc/jitsi-media-tools/internal/ffmpeg/ffmpegtest"
)

func TestFeedArgs(t *testing.T) {
	args := FeedArgs("srt://0.0.0.0:9001?mode=listener", 640, 360)
	for _, run := range [][]string{
		{"-f", "nut", "-i", "pipe:0"},
		{"-c:v", "libx264"},
		{"-tune", "zerolatency"},
		{"-c:a", "aac"},
		{"-f", "mpegts", "srt://0.0.0.0:9001?mode=listener"},
	} {
		if !containsRun(args, run) {
			t.Errorf("feed args lack %v: %v", run, args)
		}
	}
}

// freeUDPPort returns a UDP port free now on loopback.
func freeUDPPort(t *testing.T) int {
	t.Helper()
	c, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	return c.LocalAddr().(*net.UDPAddr).Port
}

// readFeed reads the SRT feed at port for a second as OBS would, and
// returns ffmpeg's report of the streams.
func readFeed(t *testing.T, bin string, port int) string {
	t.Helper()
	out, err := exec.Command(bin, "-hide_banner", "-probesize", "500000", "-analyzeduration", "500000",
		"-i", "srt://127.0.0.1:"+strconv.Itoa(port)+"?mode=caller&latency=120000",
		"-t", "1", "-f", "null", "-").CombinedOutput()
	if err != nil {
		t.Fatalf("reading the feed: %v\n%s", err, out)
	}
	return string(out)
}

// TestFeed_ServesOneCallerAfterAnother checks T048: the feed is readable
// over SRT as H.264 and AAC, and after a caller leaves, the restarted
// feed takes the next one.
func TestFeed_ServesOneCallerAfterAnother(t *testing.T) {
	bin := ffmpegtest.Require(t)
	caps, err := ffmpeg.Probe(context.Background(), bin)
	if err != nil {
		t.Fatal(err)
	}
	if err := caps.Missing(ffmpeg.NeedSRT, ffmpeg.NeedH264Encoder, ffmpeg.NeedAACEncoder); err != nil {
		t.Skip(err)
	}
	port := freeUDPPort(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	const w, h = 320, 180
	f, err := StartFeed(ctx, FeedConfig{FFmpeg: bin, Name: "Alice", Width: w, Height: h, Logger: quiet,
		ListenURL: "srt://127.0.0.1:" + strconv.Itoa(port) + "?mode=listener&latency=120000&transtype=live"})
	if err != nil {
		t.Fatal(err)
	}
	defer f.Stop()

	// A participant's tile stream: a frame every 33 ms, a chunk every 20 ms.
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		frame := compose.NewFrame(w, h)
		frame.Fill(160, 90, 200)
		video := time.NewTicker(compose.VideoInterval)
		audio := time.NewTicker(compose.AudioInterval)
		defer video.Stop()
		defer audio.Stop()
		for {
			select {
			case <-stop:
				return
			case <-video.C:
				f.WriteVideo(frame.Bytes(nil), time.Now())
			case <-audio.C:
				f.WriteAudio(make([]byte, ffmpeg.DecodedChunkBytes), time.Now())
			}
		}
	}()

	for i := 1; i <= 2; i++ {
		report := readFeed(t, bin, port)
		if !strings.Contains(report, "Video: h264") || !strings.Contains(report, "Audio: aac") {
			t.Fatalf("caller %d: streams not as expected:\n%s", i, report)
		}
		if !strings.Contains(report, strconv.Itoa(w)+"x"+strconv.Itoa(h)) {
			t.Errorf("caller %d: not %dx%d:\n%s", i, w, h, report)
		}
	}
}

// Stop ends the feed's ffmpeg, also while it waits for a caller.
func TestFeed_StopWhileWaiting(t *testing.T) {
	bin := ffmpegtest.Require(t)
	f, err := StartFeed(context.Background(), FeedConfig{FFmpeg: bin, Name: "Bob", Width: 320, Height: 180, Logger: quiet,
		ListenURL: "srt://127.0.0.1:" + strconv.Itoa(freeUDPPort(t)) + "?mode=listener"})
	if err != nil {
		t.Fatal(err)
	}
	f.WriteVideo(compose.NewFrame(320, 180).Bytes(nil), time.Now())
	time.Sleep(500 * time.Millisecond) // ffmpeg is up and waiting
	done := make(chan struct{})
	go func() { f.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not return")
	}
}
