package session

import (
	"context"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/pion/webrtc/v3"

	"github.com/daimoc/jitsi-media-tools/internal/ffmpeg/ffmpegtest"
)

// bridgeCounter records the RTP the fake bridge receives, per kind.
type bridgeCounter struct {
	mu      sync.Mutex
	packets map[string]int
	first   map[string]uint32
	last    map[string]time.Time
	backErr string // A timestamp that went backwards
}

func newBridgeCounter() *bridgeCounter {
	return &bridgeCounter{packets: map[string]int{}, first: map[string]uint32{}, last: map[string]time.Time{}}
}

func (c *bridgeCounter) setup(bridge *webrtc.PeerConnection) {
	bridge.OnTrack(func(tr *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		kind := tr.Kind().String()
		var prev uint32
		for {
			p, _, err := tr.ReadRTP()
			if err != nil {
				return
			}
			c.mu.Lock()
			if c.packets[kind] == 0 {
				c.first[kind] = p.Timestamp
			} else if int32(p.Timestamp-prev) < 0 {
				c.backErr = kind
			}
			prev = p.Timestamp
			c.packets[kind]++
			c.last[kind] = time.Now()
			c.mu.Unlock()
		}
	})
}

func (c *bridgeCounter) get(kind string) (int, uint32, time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.packets[kind], c.first[kind], c.last[kind]
}

// makeClip writes a clip of the given seconds of test pattern and tone.
func makeClip(t *testing.T, seconds string) string {
	t.Helper()
	bin := ffmpegtest.Require(t)
	path := filepath.Join(t.TempDir(), "clip.webm")
	out, err := exec.Command(bin, "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc2=size=320x180:rate=30", "-f", "lavfi", "-i", "sine",
		"-t", seconds, "-c:v", "libvpx", "-c:a", "libopus", path).CombinedOutput()
	if err != nil {
		t.Fatalf("making clip: %v: %s", err, out)
	}
	return path
}

func TestPublish_FileOnceSendsBothKindsThenCloses(t *testing.T) {
	clip := makeClip(t, "1.5")
	c := newBridgeCounter()
	useFake(t, &fakeSignal{Setup: c.setup})
	var col collected
	s, err := Start(context.Background(), col.config(Config{
		MeetingURL: "https://meet.test/Room", DisplayName: "Clip",
		Publish: &Publish{Input: FileInput(clip, false), Audio: true, Video: true, Width: 320, Height: 180},
	}))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-s.Done():
	case <-time.After(15 * time.Second):
		s.Close()
		t.Fatal("session did not end after the file")
	}
	if st := s.State(); st != StateClosed || s.Err() != nil {
		t.Errorf("state %s, err %v; want closed", st, s.Err())
	}
	for _, kind := range []string{"audio", "video"} {
		n, first, _ := c.get(kind)
		if n < 20 {
			t.Errorf("%s: %d packets", kind, n)
		}
		if first != 1<<30 {
			t.Errorf("%s: first timestamp %d, want 2^30", kind, first)
		}
	}
	if c.backErr != "" {
		t.Errorf("%s timestamps went backwards", c.backErr)
	}
}

func TestPublish_LoopKeepsSending(t *testing.T) {
	clip := makeClip(t, "1")
	c := newBridgeCounter()
	useFake(t, &fakeSignal{Setup: c.setup})
	var col collected
	s, err := Start(context.Background(), col.config(Config{
		MeetingURL: "https://meet.test/Room", DisplayName: "Loop",
		Publish: &Publish{Input: FileInput(clip, true), Audio: true, Video: true, Width: 320, Height: 180},
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	time.Sleep(3500 * time.Millisecond)
	if s.State() != StateJoined {
		t.Fatalf("state %s, want joined", s.State())
	}
	for _, kind := range []string{"audio", "video"} {
		n, _, last := c.get(kind)
		if n == 0 || time.Since(last) > 500*time.Millisecond {
			t.Errorf("%s: %d packets, last %v ago; want still sending", kind, n, time.Since(last))
		}
	}
}

func TestPublish_AudioOnlyFile(t *testing.T) {
	bin := ffmpegtest.Require(t)
	path := filepath.Join(t.TempDir(), "tone.ogg")
	if out, err := exec.Command(bin, "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "sine",
		"-t", "1", "-c:a", "libopus", path).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	useFake(t, &fakeSignal{})
	var col collected
	s, err := Start(context.Background(), col.config(Config{
		MeetingURL: "https://meet.test/Room", DisplayName: "Tone",
		Publish: &Publish{Input: FileInput(path, true), Audio: true, Video: true},
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if kinds := s.media.publishKinds(); len(kinds) != 1 || kinds[0] != "audio" {
		t.Errorf("published kinds %v, want [audio]", kinds)
	}
}

func TestPublish_MissingFileFailsBeforeJoining(t *testing.T) {
	ffmpegtest.Require(t)
	f := &fakeSignal{}
	useFake(t, f)
	var col collected
	_, err := Start(context.Background(), col.config(Config{
		MeetingURL: "https://meet.test/Room", DisplayName: "Clip",
		Publish: &Publish{Input: FileInput(filepath.Join(t.TempDir(), "none.mp4"), false), Audio: true, Video: true},
	}))
	if err == nil {
		t.Fatal("no error for a missing file")
	}
	if f.Handler() != nil {
		t.Error("joined despite the missing file")
	}
}
