//go:build live

// Package live holds end-to-end tests against a real Jitsi Meet server
// (tests/platforms/jitsi): JMT_URL is a meeting URL, JMT_INSECURE=1 accepts
// a self-signed certificate.
package live

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/daimoc/jitsi-media-tools/internal/jitsi"
	"github.com/daimoc/jitsi-media-tools/internal/peer"
	"github.com/daimoc/jitsi-media-tools/internal/signaling"
	"github.com/daimoc/jitsi-media-tools/jmt"
)

func liveURL(t *testing.T) (string, bool) {
	t.Helper()
	u := os.Getenv("JMT_URL")
	if u == "" {
		t.Skip("JMT_URL not set")
	}
	return u, os.Getenv("JMT_INSECURE") == "1"
}

func logger() *slog.Logger {
	if os.Getenv("JMT_DEBUG") == "1" {
		return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// receiver is a receive-only participant built from the signaling and peer
// packages, counting the RTP it gets per kind.
type receiver struct {
	mu      sync.Mutex
	packets map[string]int
}

func (r *receiver) count(kind string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.packets[kind]
}

func joinReceiver(t *testing.T, ctx context.Context, meetingURL string, insecure bool) *receiver {
	t.Helper()
	m, err := jitsi.FetchMeeting(ctx, meetingURL, jitsi.HTTPClient(insecure))
	if err != nil {
		t.Fatal(err)
	}
	a := jitsi.NewAdapter(logger())
	if err := a.ParseConfig(map[string]any{
		"xmpp_domain": m.Domain, "room_name": m.Room, "muc_domain": m.MUC,
		"websocket_url": m.WebSocket, "focus_jid": "focus." + m.Domain, "insecure": insecure,
	}); err != nil {
		t.Fatal(err)
	}
	p, err := peer.New(peer.Config{Logger: logger()})
	if err != nil {
		t.Fatal(err)
	}
	r := &receiver{packets: map[string]int{}}
	p.OnTrack(func(tr *peer.RemoteTrack) {
		go func() {
			for {
				if _, err := tr.ReadRTP(); err != nil {
					return
				}
				r.mu.Lock()
				r.packets[tr.Kind]++
				r.mu.Unlock()
			}
		}()
	})
	const id = "Receiver"
	if err := a.Connect(ctx, id, p); err != nil {
		t.Fatal(err)
	}
	a.SetReceivePolicy(id, &signaling.ReceivePolicy{LastN: 9, DefaultMaxHeight: 180})
	t.Cleanup(func() {
		a.Disconnect(id)
		p.Close()
	})
	return r
}

// TestLive_PublishFile checks SC-001: a published file reaches another
// participant, audio and video, within 10 s.
func TestLive_PublishFile(t *testing.T) {
	meetingURL, insecure := liveURL(t)
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	clip := filepath.Join(t.TempDir(), "clip.webm")
	if out, err := exec.Command(ff, "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc2=size=640x360:rate=30", "-f", "lavfi", "-i", "sine",
		"-t", "3", "-c:v", "libvpx", "-c:a", "libopus", clip).CombinedOutput(); err != nil {
		t.Fatalf("making clip: %v: %s", err, out)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	rx := joinReceiver(t, ctx, meetingURL, insecure)
	start := time.Now()
	s, err := jmt.Join(ctx, jmt.Options{
		MeetingURL: meetingURL, DisplayName: "Publisher", Insecure: insecure, Logger: logger(),
		Publish: jmt.WithSize(jmt.FileSource(clip, true), 640, 360),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	for time.Since(start) < 10*time.Second {
		if rx.count(peer.KindAudio) > 50 && rx.count(peer.KindVideo) > 50 {
			t.Logf("audio and video received after %v", time.Since(start).Round(100*time.Millisecond))
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("after 10 s: %d audio, %d video packets", rx.count(peer.KindAudio), rx.count(peer.KindVideo))
}

