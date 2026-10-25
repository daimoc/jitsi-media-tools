package jmt

import (
	"context"
	"io"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/daimoc/jitsi-media-tools/internal/ffmpeg"
	"github.com/daimoc/jitsi-media-tools/internal/session"
	"github.com/daimoc/jitsi-media-tools/internal/session/sessiontest"
	"github.com/daimoc/jitsi-media-tools/internal/signaling"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// fakeRoom joins sessions to a fake server where Alice (alice1) is in the
// room; with media, she sends a 320x180 test pattern and a tone.
func fakeRoom(t *testing.T, media bool) *sessiontest.Signal {
	f := &sessiontest.Signal{T: t, Occupants: []signaling.Occupant{{ID: "alice1", DisplayName: "Alice"}}}
	t.Cleanup(session.UseTestServer(f, sessiontest.FetchMeeting))
	if media {
		sessiontest.NewSender(f, "alice1")
	}
	return f
}

func join(t *testing.T) *Session {
	t.Helper()
	s, err := Join(context.Background(), Options{MeetingURL: "https://meet.test/Room", DisplayName: "Lib", Logger: quiet})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func nextEvent(t *testing.T, s *Session) Event {
	t.Helper()
	select {
	case e, ok := <-s.Events():
		if !ok {
			t.Fatal("events closed")
		}
		return e
	case <-time.After(5 * time.Second):
		t.Fatal("no event within 5 s")
	}
	return Event{}
}

func TestSession_Events(t *testing.T) {
	f := fakeRoom(t, false)
	s := join(t)
	if e := nextEvent(t, s); e.Kind != ParticipantJoined || e.Participant.ID != "alice1" || e.Participant.Name != "Alice" {
		t.Fatalf("first event %v %+v, want Alice joined", e.Kind, e.Participant)
	}
	f.Room().OnOccupant(signaling.Occupant{ID: "alice1", DisplayName: "Alice", AudioMuted: true})
	if e := nextEvent(t, s); e.Kind != ParticipantChanged || !e.Participant.AudioMuted {
		t.Fatalf("after mute: %v %+v, want changed and muted", e.Kind, e.Participant)
	}
	f.Room().OnOccupant(signaling.Occupant{ID: "alice1", Left: true})
	if e := nextEvent(t, s); e.Kind != ParticipantLeft || e.Participant.ID != "alice1" {
		t.Fatalf("after leave: %v %+v, want Alice left", e.Kind, e.Participant)
	}
	s.Close()
	for range s.Events() { // Closed once the session ends
	}
}

// received counts what the callbacks get and checks its shape.
type received struct {
	t  *testing.T
	mu sync.Mutex
	n  map[string]int
}

func (r *received) add(kind string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.n == nil {
		r.n = map[string]int{}
	}
	r.n[kind]++
}

func (r *received) count(kind string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.n[kind]
}

func (r *received) video(kind, id string, w, h int) func(VideoFrame) {
	return func(f VideoFrame) {
		if f.ParticipantID != id || f.Width != w || f.Height != h || len(f.Y) != w*h || len(f.U) != w*h/4 || len(f.V) != w*h/4 || f.Time.IsZero() {
			r.t.Errorf("%s frame: id %q %dx%d planes %d/%d/%d, want id %q %dx%d", kind, f.ParticipantID, f.Width, f.Height, len(f.Y), len(f.U), len(f.V), id, w, h)
		}
		r.add(kind)
	}
}

func (r *received) audio(kind, id string) func(AudioChunk) {
	return func(c AudioChunk) {
		if c.ParticipantID != id || len(c.Samples) != ChunkSamples || c.Time.IsZero() {
			r.t.Errorf("%s chunk: id %q, %d samples, want id %q, %d", kind, c.ParticipantID, len(c.Samples), id, ChunkSamples)
		}
		if slices.ContainsFunc(c.Samples, func(v int16) bool { return v > 1000 || v < -1000 }) {
			r.add(kind + " audible")
		}
		r.add(kind)
	}
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("after 5 s: %s", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestSession_Callbacks(t *testing.T) {
	fakeRoom(t, true)
	s := join(t)
	r := &received{t: t}
	s.OnVideo(r.video("video", "alice1", 320, 180))
	s.OnGrid(r.video("grid", "", 1280, 720))
	s.OnAudio(r.audio("audio", "alice1"))
	s.OnMixedAudio(r.audio("mix", ""))
	waitFor(t, "frames and audible chunks of each kind", func() bool {
		return r.count("video") > 10 && r.count("grid") > 10 && r.count("audio audible") > 0 && r.count("mix audible") > 0
	})
	s.Close()
	if n := ffmpeg.Running(); n != 0 {
		t.Errorf("%d processes after Close", n)
	}
}

// TestSession_NoCallbackNoDecoder: decoding starts when a callback is set,
// not before.
func TestSession_NoCallbackNoDecoder(t *testing.T) {
	fakeRoom(t, true)
	s := join(t)
	time.Sleep(1500 * time.Millisecond)
	if n := ffmpeg.Running(); n != 0 {
		t.Fatalf("%d processes running with no callback", n)
	}
	r := &received{t: t}
	s.OnVideo(r.video("video", "alice1", 320, 180))
	waitFor(t, "video frames once OnVideo is set", func() bool { return r.count("video") > 0 })
}

// TestSession_BlockedCallbackStallsNothing: a callback that never returns
// loses frames, while the others keep their rate.
func TestSession_BlockedCallbackStallsNothing(t *testing.T) {
	fakeRoom(t, true)
	s := join(t)
	release := make(chan struct{})
	defer close(release)
	var blocked sync.Once
	s.OnVideo(func(VideoFrame) {
		blocked.Do(func() { <-release })
	})
	r := &received{t: t}
	s.OnGrid(r.video("grid", "", 1280, 720))
	s.OnMixedAudio(r.audio("mix", ""))
	start := time.Now()
	waitFor(t, "mixed audio", func() bool { return r.count("mix audible") > 0 })
	time.Sleep(time.Second)
	secs := time.Since(start).Seconds()
	// The grid runs at 30 fps and the mix at 50 chunks/s; a stall would
	// bring them near 0. Under the race detector, with the other packages'
	// ffmpeg tests running alongside, they drop to about half.
	if g := float64(r.count("grid")) / secs; g < 8 {
		t.Errorf("grid at %.0f fps with OnVideo blocked, want about 30", g)
	}
	if m := float64(r.count("mix")) / secs; m < 15 {
		t.Errorf("mix at %.0f chunks/s with OnVideo blocked, want 50", m)
	}
}

// TestSession_FrameStableDuringCallback: the session does not write into a
// frame while the callback holds it.
func TestSession_FrameStableDuringCallback(t *testing.T) {
	fakeRoom(t, true)
	s := join(t)
	var mu sync.Mutex
	changed, checked := 0, 0
	s.OnVideo(func(f VideoFrame) {
		y := slices.Clone(f.Y)
		time.Sleep(100 * time.Millisecond) // Several frames arrive meanwhile
		mu.Lock()
		defer mu.Unlock()
		checked++
		if !slices.Equal(y, f.Y) {
			changed++
		}
	})
	waitFor(t, "3 frames checked", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return checked >= 3
	})
	mu.Lock()
	defer mu.Unlock()
	if changed > 0 {
		t.Errorf("%d of %d frames changed during the callback", changed, checked)
	}
}
