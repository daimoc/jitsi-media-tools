//go:build live

package live

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/daimoc/jitsi-media-tools/internal/session"
	"github.com/daimoc/jitsi-media-tools/jmt"
)

// whipFFmpeg is ffmpeg publishing a test pattern and a tone over WHIP, as
// OBS does: H.264 High profile without B-frames, and Opus.
func whipFFmpeg(ctx context.Context, ff, url, token string) *exec.Cmd {
	return exec.CommandContext(ctx, ff, "-hide_banner", "-loglevel", "error", "-re",
		"-f", "lavfi", "-i", "testsrc2=size=640x360:rate=30",
		"-f", "lavfi", "-i", "sine=frequency=1000:sample_rate=48000",
		"-c:v", "libx264", "-profile:v", "high", "-preset", "veryfast", "-tune", "zerolatency", "-g", "60", "-b:v", "1500k",
		"-c:a", "libopus", "-ar", "48000", "-ac", "2", "-b:a", "64k",
		"-f", "whip", "-authorization", token, url)
}

func present(s *session.Session, name string) (here, selected bool) {
	for _, p := range s.Participants() {
		if p.Name == name {
			return true, p.Selected
		}
	}
	return false, false
}

// TestLive_WHIPGateway checks US9 against the local Jitsi: a WHIP stream
// joins its meeting as a participant that a viewer sees and hears; it
// leaves when the publisher disappears; a wrong token joins nobody.
func TestLive_WHIPGateway(t *testing.T) {
	meetingURL, insecure := liveURL(t)
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	if out, _ := exec.Command(ff, "-hide_banner", "-h", "muxer=whip").CombinedOutput(); !strings.Contains(string(out), "WHIP") {
		t.Skip("ffmpeg has no whip muxer (ffmpeg 8 or newer)")
	}
	meetingURL += "-whip"
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	const secret = "live-secret"
	g, err := jmt.NewGateway(jmt.GatewayOptions{Listen: "127.0.0.1:0", Secret: secret, UDPPort: -1, Insecure: insecure, Logger: logger()})
	if err != nil {
		t.Fatal(err)
	}
	served := make(chan error, 1)
	go func() { served <- g.Serve(ctx) }()
	defer func() {
		g.Close()
		if err := <-served; err != nil {
			t.Errorf("Serve: %v", err)
		}
	}()
	whipURL := "http://" + g.Addr() + "/whip/" + strings.TrimPrefix(meetingURL, "https://") + "?name=OBS"

	var w gridWatch
	viewer, err := session.Start(ctx, session.Config{
		MeetingURL: meetingURL, DisplayName: "Viewer", Insecure: insecure, Logger: logger(),
		OnGrid: w.grid, OnMix: w.mix,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer viewer.Close()

	// A wrong token: ffmpeg fails, nobody joins.
	if out, err := whipFFmpeg(ctx, ff, whipURL, "wrong").CombinedOutput(); err == nil {
		t.Errorf("ffmpeg with a wrong token succeeded: %s", out)
	}
	if len(g.Streams()) != 0 {
		t.Fatalf("streams after a wrong token: %+v", g.Streams())
	}

	pubCtx, stopPub := context.WithCancel(ctx)
	defer stopPub()
	pub := whipFFmpeg(pubCtx, ff, whipURL, secret)
	var stderr strings.Builder
	pub.Stderr = &stderr
	start := time.Now()
	if err := pub.Start(); err != nil {
		t.Fatal(err)
	}
	for {
		video, audible := w.ok()
		_, selected := present(viewer, "OBS")
		if video && audible && selected {
			t.Logf("OBS shown and heard after %v", time.Since(start).Round(100*time.Millisecond))
			break
		}
		if time.Since(start) > 10*time.Second {
			t.Fatalf("after 10 s: video %v, audible %v, participants %+v, streams %+v, ffmpeg: %s",
				video, audible, viewer.Participants(), g.Streams(), stderr.String())
		}
		time.Sleep(100 * time.Millisecond)
	}
	if st := g.Streams(); len(st) != 1 || st[0].DisplayName != "OBS" {
		t.Errorf("streams %+v, want OBS", st)
	}

	// The publisher disappears without saying so: the participant leaves
	// after 10 s without media.
	stopPub()
	pub.Wait()
	gone := time.Now()
	for here, _ := present(viewer, "OBS"); here; here, _ = present(viewer, "OBS") {
		if time.Since(gone) > 11*time.Second {
			t.Fatalf("OBS still in the meeting 11 s after its publisher stopped (10 s without media, FR-028)")
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Logf("OBS left %v after its publisher stopped", time.Since(gone).Round(100*time.Millisecond))

	var kinds []string
	for len(kinds) < 2 {
		select {
		case e := <-g.Events():
			kinds = append(kinds, e.Kind.String())
		case <-time.After(5 * time.Second):
			t.Fatalf("events %v, want started then failed", kinds)
		}
	}
	if kinds[0] != "started" || kinds[1] != "failed" {
		t.Errorf("events %v, want started then failed (no media)", kinds)
	}
}
