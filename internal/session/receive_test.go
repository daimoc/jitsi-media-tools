package session

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/daimoc/jitsi-media-tools/internal/compose"
	"github.com/daimoc/jitsi-media-tools/internal/ffmpeg"
	"github.com/daimoc/jitsi-media-tools/internal/session/sessiontest"
	"github.com/daimoc/jitsi-media-tools/internal/signaling"
)

// watcher records what the grid and mix hooks see.
type watcher struct {
	mu       sync.Mutex
	video    bool // A grid with a video tile (neither black nor a card)
	audible  bool
	gridSeen int
}

func (w *watcher) grid(f *compose.Frame, _ time.Time) {
	r := compose.Layout(1, f.W, f.H)[0]
	v := f.Y[(r.Y+r.H/3)*f.W+r.X+r.W/2]
	w.mu.Lock()
	defer w.mu.Unlock()
	w.gridSeen++
	if v != 16 && v != 40 {
		w.video = true
	}
}

func (w *watcher) mix(s []int16, _ time.Time) {
	for _, v := range s {
		if v > 1000 || v < -1000 {
			w.mu.Lock()
			w.audible = true
			w.mu.Unlock()
			return
		}
	}
}

func (w *watcher) state() (video, audible bool, grids int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.video, w.audible, w.gridSeen
}

func TestReceive_GridShowsVideoAndMixIsAudible(t *testing.T) {
	f := &fakeSignal{Occupants: []signaling.Occupant{{ID: "alice1", DisplayName: "Alice"}}}
	useFake(t, f)
	b := sessiontest.NewSender(f, "alice1")
	var w watcher
	var col collected
	s, err := Start(context.Background(), col.config(Config{
		MeetingURL: "https://meet.test/Room", DisplayName: "Viewer",
		OnGrid: w.grid, OnMix: w.mix,
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if video, audible, _ := w.state(); video && audible {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	video, audible, grids := w.state()
	if !video || !audible {
		select {
		case err := <-b.Errors:
			t.Fatalf("bridge: %v", err)
		default:
		}
		t.Fatalf("after 5 s: video %v, audible %v (%d grids)", video, audible, grids)
	}
	if p := s.Participants(); len(p) != 1 || !p[0].Selected {
		t.Errorf("participants %+v, want Alice selected", p)
	}
	before := ffmpeg.Running()
	if before == 0 {
		t.Error("no decoder running")
	}
	s.Close()
	if n := ffmpeg.Running(); n != 0 {
		t.Errorf("%d processes after Close", n)
	}
}

func TestReceive_NoConsumerStartsNoDecoder(t *testing.T) {
	f := &fakeSignal{Occupants: []signaling.Occupant{{ID: "alice1", DisplayName: "Alice"}}}
	useFake(t, f)
	sessiontest.NewSender(f, "alice1")
	var col collected
	s, err := Start(context.Background(), col.config(Config{MeetingURL: "https://meet.test/Room", DisplayName: "Viewer"}))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	time.Sleep(1500 * time.Millisecond)
	if n := ffmpeg.Running(); n != 0 {
		t.Errorf("%d processes running with nothing consuming", n)
	}
}

func TestReceive_ScreenShareHasItsOwnTile(t *testing.T) {
	s := &Session{reg: newRegistry()}
	r := &receiver{m: &media{s: s}, room: compose.NewRoom(gridWidth, gridHeight, 9), known: map[string]bool{}}
	s.reg.occupant(signaling.Occupant{ID: "a", DisplayName: "Alice"})
	s.reg.sources([]signaling.RemoteSource{
		{SSRC: 5, Kind: "video", Name: "a-v0", Owner: "a"},
		{SSRC: 6, Kind: "video", Name: "a-v1", Owner: "a", Desktop: true},
	})
	r.sync()
	names := func() map[string]string {
		out := map[string]string{}
		for _, tl := range r.room.Tiles(time.Now()) {
			out[tl.ID] = tl.Name
		}
		return out
	}
	if got := names(); len(got) != 2 || got["a"] != "Alice" || got[screenTile("a")] != "Alice (screen)" {
		t.Errorf("tiles %v, want Alice and Alice (screen)", got)
	}
	// Frames of the two streams land on their own tiles.
	r.room.PushVideo("a", compose.NewFrame(4, 4), time.Now())
	r.room.PushVideo(screenTile("a"), compose.NewFrame(8, 8), time.Now())
	for _, tl := range r.room.Tiles(time.Now()) {
		if want := map[string]int{"a": 4, screenTile("a"): 8}[tl.ID]; tl.Frame == nil || tl.Frame.W != want {
			t.Errorf("tile %s frame %+v, want width %d", tl.ID, tl.Frame, want)
		}
	}
	// The share stops: the source stays, muted in presence; its tile goes.
	s.reg.occupant(signaling.Occupant{ID: "a", DisplayName: "Alice", SourceMuted: map[string]bool{"a-v0": false, "a-v1": true}})
	r.sync()
	if got := names(); len(got) != 1 || got["a"] != "Alice" {
		t.Errorf("tiles after the share %v, want Alice only", got)
	}
	// Shared again, then the source goes.
	s.reg.occupant(signaling.Occupant{ID: "a", DisplayName: "Alice", SourceMuted: map[string]bool{"a-v0": false, "a-v1": false}})
	r.sync()
	if got := names(); len(got) != 2 {
		t.Errorf("tiles when sharing again %v, want 2", got)
	}
	s.reg.sources([]signaling.RemoteSource{{SSRC: 5, Kind: "video", Name: "a-v0", Owner: "a"}})
	r.sync()
	if got := names(); len(got) != 1 || got["a"] != "Alice" {
		t.Errorf("tiles after the share %v, want Alice only", got)
	}
}
