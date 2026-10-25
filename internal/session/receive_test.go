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
