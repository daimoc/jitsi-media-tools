package session

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v3"

	"github.com/daimoc/jitsi-media-tools/internal/compose"
	"github.com/daimoc/jitsi-media-tools/internal/ffmpeg"
	"github.com/daimoc/jitsi-media-tools/internal/session/sessiontest"
	"github.com/daimoc/jitsi-media-tools/internal/signaling"
)

// Edge cases of spec.md (T066).

// tileWatch records what the single tile of the grid and the mix showed.
type tileWatch struct {
	mu                 sync.Mutex
	video, card, sound bool
	grids, mixes       int
}

func (w *tileWatch) grid(f *compose.Frame, _ time.Time) {
	r := compose.Layout(1, f.W, f.H)[0]
	v := f.Y[(r.Y+r.H/3)*f.W+r.X+r.W/2]
	w.mu.Lock()
	defer w.mu.Unlock()
	w.grids++
	switch v {
	case 16: // Black
	case 40:
		w.card = true
	default:
		w.video = true
	}
}

func (w *tileWatch) mix(s []int16, _ time.Time) {
	loud := false
	for _, v := range s {
		if v > 1000 || v < -1000 {
			loud = true
			break
		}
	}
	w.mu.Lock()
	w.mixes++
	w.sound = w.sound || loud
	w.mu.Unlock()
}

// waitFor polls cond for up to 5 s.
func (w *tileWatch) waitFor(cond func(*tileWatch) bool) bool {
	deadline := time.Now().Add(5 * time.Second)
	for {
		w.mu.Lock()
		ok := cond(w)
		w.mu.Unlock()
		if ok || time.Now().After(deadline) {
			return ok
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (w *tileWatch) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return "video=" + boolStr(w.video) + " card=" + boolStr(w.card) + " sound=" + boolStr(w.sound)
}

func boolStr(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// logBuf keeps a session's log lines.
type logBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *logBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *logBuf) count(level, msg string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, line := range strings.Split(l.b.String(), "\n") {
		if strings.Contains(line, "level="+level) && strings.Contains(line, msg) {
			n++
		}
	}
	return n
}

func startWatched(t *testing.T, w *tileWatch, logs *logBuf) *Session {
	t.Helper()
	logger := quietLogger
	if logs != nil {
		logger = slog.New(slog.NewTextHandler(logs, nil))
	}
	s, err := Start(context.Background(), Config{
		MeetingURL: "https://meet.test/Room", DisplayName: "Viewer", Logger: logger,
		OnGrid: w.grid, OnMix: w.mix,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestEdge_DuplicateNames(t *testing.T) {
	f := &fakeSignal{Occupants: []signaling.Occupant{{ID: "a", DisplayName: "Alice"}, {ID: "b", DisplayName: "Alice"}}}
	useFake(t, f)
	var c collected
	s, err := Start(context.Background(), c.config(Config{MeetingURL: "https://meet.test/Room", DisplayName: "Viewer"}))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	names := map[string]string{}
	for _, p := range s.Participants() {
		names[p.ID] = p.Name
	}
	if names["a"] != "Alice" || names["b"] != "Alice (2)" {
		t.Errorf("names %v, want Alice and Alice (2)", names)
	}
}

func TestEdge_AudioOnlyParticipant(t *testing.T) {
	f := &fakeSignal{Occupants: []signaling.Occupant{{ID: "alice1", DisplayName: "Alice"}}}
	useFake(t, f)
	b := sessiontest.NewSender(f, "alice1")
	b.AudioOnly = true
	var w tileWatch
	startWatched(t, &w, nil)
	if !w.waitFor(func(w *tileWatch) bool { return w.sound && w.card }) {
		t.Fatalf("after 5 s: %s, want a name card and sound", &w)
	}
	if w.video {
		t.Error("video shown for an audio-only participant")
	}
}

func TestEdge_VideoOnlyParticipant(t *testing.T) {
	f := &fakeSignal{Occupants: []signaling.Occupant{{ID: "alice1", DisplayName: "Alice"}}}
	useFake(t, f)
	b := sessiontest.NewSender(f, "alice1")
	b.VideoOnly = true
	var w tileWatch
	startWatched(t, &w, nil)
	if !w.waitFor(func(w *tileWatch) bool { return w.video && w.mixes > 50 }) {
		t.Fatalf("after 5 s: %s, %d mixes, want video and a running mix", &w, w.mixes)
	}
	if w.sound {
		t.Error("sound in the mix of a video-only participant")
	}
}

// av1Sender makes the bridge send AV1, which the tool negotiates but
// cannot decode (research R7), for owner.
func av1Sender(f *fakeSignal, owner string) {
	stop := make(chan struct{})
	f.T.Cleanup(func() { close(stop) })
	f.Setup = func(bridge *webrtc.PeerConnection) {
		tr, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeAV1, ClockRate: 90000}, "video", owner)
		if err != nil {
			f.T.Fatal(err)
		}
		snd, err := bridge.AddTrack(tr)
		if err != nil {
			f.T.Fatal(err)
		}
		f.Sources = []signaling.RemoteSource{{SSRC: uint32(snd.GetParameters().Encodings[0].SSRC), Kind: "video", Name: owner + "-v0", Owner: owner}}
		go func() {
			tick := time.NewTicker(time.Second / 30)
			defer tick.Stop()
			for i := uint16(0); ; i++ {
				select {
				case <-stop:
					return
				case <-tick.C:
				}
				pkt := &rtp.Packet{Header: rtp.Header{Version: 2, Marker: true, SequenceNumber: i, Timestamp: uint32(i) * 3000}, Payload: []byte{0x10, 0, 0, 0}}
				if err := tr.WriteRTP(pkt); err != nil && !errors.Is(err, context.Canceled) {
					return
				}
			}
		}()
	}
}

func TestEdge_UndecodableCodec(t *testing.T) {
	f := &fakeSignal{Occupants: []signaling.Occupant{{ID: "alice1", DisplayName: "Alice"}}}
	useFake(t, f)
	av1Sender(f, "alice1")
	var w tileWatch
	var logs logBuf
	startWatched(t, &w, &logs)
	if !w.waitFor(func(w *tileWatch) bool { return w.card && logs.count("WARN", "Cannot decode video/AV1") > 0 }) {
		t.Fatalf("after 5 s: %s, %d warnings, want a name card and a warning", &w, logs.count("WARN", "Cannot decode"))
	}
	time.Sleep(time.Second) // More packets: still one warning
	if n := logs.count("WARN", "Cannot decode"); n != 1 {
		t.Errorf("%d warnings, want 1", n)
	}
	if w.video {
		t.Error("video shown for an undecodable codec")
	}
	if n := ffmpeg.Running(); n != 0 {
		t.Errorf("%d processes running for an undecodable codec", n)
	}
}

func TestEdge_LostConnectionLeavesNoProcess(t *testing.T) {
	f := &fakeSignal{Occupants: []signaling.Occupant{{ID: "alice1", DisplayName: "Alice"}}}
	useFake(t, f)
	sessiontest.NewSender(f, "alice1")
	var w tileWatch
	s := startWatched(t, &w, nil)
	if !w.waitFor(func(w *tileWatch) bool { return w.video && w.sound }) {
		t.Fatalf("after 5 s: %s", &w)
	}
	if ffmpeg.Running() == 0 {
		t.Fatal("no decoder running")
	}
	f.Handler().OnSignalingLost(errFake)
	select {
	case <-s.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("session did not end after the connection was lost")
	}
	if s.State() != StateFailed || !errors.Is(s.Err(), errFake) {
		t.Errorf("state %s err %v, want failed", s.State(), s.Err())
	}
	if n := ffmpeg.Running(); n != 0 {
		t.Errorf("%d processes after the session ended", n)
	}
}
