//go:build live

package live

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/daimoc/jitsi-media-tools/jmt"
)

// TestLive_OBSFeed checks US4 against the local Jitsi: a participant's SRT
// feed carries their video (not a name card) as OBS reads it.
func TestLive_OBSFeed(t *testing.T) {
	meetingURL, insecure := liveURL(t)
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	meetingURL += "-obs"
	clip := filepath.Join(t.TempDir(), "clip.webm")
	if out, err := exec.Command(ff, "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc2=size=640x360:rate=30", "-f", "lavfi", "-i", "sine",
		"-t", "3", "-c:v", "libvpx", "-c:a", "libopus", clip).CombinedOutput(); err != nil {
		t.Fatalf("making clip: %v: %s", err, out)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	feeds, err := jmt.Join(ctx, jmt.Options{MeetingURL: meetingURL, DisplayName: "Feeds", Insecure: insecure, Logger: logger(),
		OBS: &jmt.OBSOptions{Host: "127.0.0.1", BasePort: 29500, MaxFeeds: 4}})
	if err != nil {
		t.Fatal(err)
	}
	defer feeds.Close()
	pub, err := jmt.Join(ctx, jmt.Options{MeetingURL: meetingURL, DisplayName: "Publisher", Insecure: insecure, Logger: logger(),
		Publish: jmt.WithSize(jmt.FileSource(clip, true), 640, 360)})
	if err != nil {
		t.Fatal(err)
	}
	defer pub.Close()

	var url string
	for deadline := time.Now().Add(15 * time.Second); url == "" && time.Now().Before(deadline); {
		select {
		case e := <-feeds.Events():
			if e.Kind == jmt.FeedReady && e.Participant.Name == "Publisher" {
				url = e.FeedURL
			}
		case <-time.After(100 * time.Millisecond):
		}
	}
	if url == "" {
		t.Fatalf("no feed for Publisher; feeds %v", feeds.FeedURLs())
	}
	t.Logf("Publisher's feed: %s", url)

	// One frame as grey levels: the test pattern is bright and varied, a
	// name card dark grey.
	readCtx, stop := context.WithTimeout(ctx, 30*time.Second)
	defer stop()
	out, err := exec.CommandContext(readCtx, ff, "-hide_banner", "-loglevel", "error",
		"-probesize", "500000", "-analyzeduration", "500000",
		"-i", url+"?mode=caller&latency=120000", "-ss", "1", "-frames:v", "1",
		"-f", "rawvideo", "-pix_fmt", "gray", "-").Output()
	if err != nil {
		t.Fatalf("reading the feed: %v", err)
	}
	if len(out) != 640*360 {
		t.Fatalf("frame of %d bytes, want 640x360", len(out))
	}
	var sum int
	for _, v := range out {
		sum += int(v)
	}
	if mean := sum / len(out); mean < 70 {
		t.Errorf("mean brightness %d: a name card or black, not the publisher's video", mean)
	} else {
		t.Logf("mean brightness %d", mean)
	}
	if !strings.HasPrefix(url, "srt://127.0.0.1:2950") {
		t.Errorf("feed address %s, want a port from 29501", url)
	}
}
