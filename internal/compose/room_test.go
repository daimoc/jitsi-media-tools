package compose

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func video(y byte) *Frame {
	f := NewFrame(32, 18)
	f.Fill(y, 128, 128)
	return f
}

func names(tiles []Tile) string {
	var s string
	for _, t := range tiles {
		s += t.ID
	}
	return s
}

func TestRoom_TilesPreferVideoUpToLimit(t *testing.T) {
	now := time.Unix(100, 0)
	r := NewRoom(640, 360, 2)
	r.SetParticipant("a", "A", false, time.Time{})
	r.SetParticipant("b", "B", false, now) // Spoke, but no video
	r.SetParticipant("c", "C", false, time.Time{})
	r.PushVideo("a", video(100), now)
	r.PushVideo("c", video(150), now)
	if got := names(r.Tiles(now)); got != "ac" {
		t.Errorf("tiles %s, want ac (video first, limit 2)", got)
	}
	// With room for everyone, cards fill in, in grid order.
	r.SetLimit(3)
	if got := names(r.Tiles(now)); got != "bac" {
		t.Errorf("tiles %s, want bac", got)
	}
	r.Remove("b")
	if got := names(r.Tiles(now)); got != "ac" {
		t.Errorf("tiles after remove %s, want ac", got)
	}
}

func TestRoom_StaleVideoBecomesCard(t *testing.T) {
	now := time.Unix(100, 0)
	r := NewRoom(640, 360, 9)
	r.SetParticipant("a", "A", false, time.Time{})
	r.PushVideo("a", video(100), now)
	if r.Tiles(now.Add(time.Second))[0].Frame == nil {
		t.Error("video dropped after 1 s")
	}
	if r.Tiles(now.Add(videoStale + time.Millisecond))[0].Frame != nil {
		t.Error("video kept after it stopped")
	}
}

func TestRoom_VideoTickDrawsGrid(t *testing.T) {
	now := time.Unix(100, 0)
	r := NewRoom(640, 360, 9)
	r.SetParticipant("a", "A", false, time.Time{})
	r.PushVideo("a", video(120), now)
	g := r.VideoTick(now)
	if g.W != 640 || g.H != 360 {
		t.Fatalf("grid %dx%d", g.W, g.H)
	}
	rs := Layout(1, 640, 360)
	if v := g.Y[(rs[0].Y+10)*640+rs[0].X+rs[0].W/2]; v != 120 {
		t.Errorf("tile luma %d, want 120", v)
	}
	// An empty room is a black grid.
	r.Remove("a")
	if v := r.VideoTick(now).Y[180*640+320]; v != 16 {
		t.Errorf("empty grid luma %d, want 16", v)
	}
}

func TestRoom_AudioTickMixes(t *testing.T) {
	r := NewRoom(640, 360, 9)
	r.SetParticipant("a", "A", false, time.Time{})
	for i := 0; i < 3; i++ {
		r.PushAudio("a", chunk(1000))
		r.PushAudio("b", chunk(500)) // Audio before presence still mixes
	}
	mix := make([]int16, ChunkSamples)
	r.AudioTick(mix, nil)
	if mix[0] != 1500 {
		t.Errorf("mix %d, want 1500", mix[0])
	}
	r.Remove("b")
	r.AudioTick(mix, nil)
	if mix[0] != 1000 {
		t.Errorf("mix after remove %d, want 1000", mix[0])
	}
}

func TestClock_TicksAndCatchesUp(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 210*time.Millisecond)
	defer cancel()
	var n atomic.Int64
	var times []time.Time
	slow := true
	Clock(ctx, 20*time.Millisecond, func(at time.Time) {
		if slow { // One slow tick: the missed ones are caught up
			slow = false
			time.Sleep(70 * time.Millisecond)
		}
		times = append(times, at)
		n.Add(1)
	})
	if got := n.Load(); got < 9 || got > 11 {
		t.Errorf("%d ticks in 210 ms of 20 ms, want about 10", got)
	}
	// Each tick gets its own due time, caught-up ones too, so that
	// timestamped outputs (recordings) get a regular frame rate.
	for i := 1; i < len(times); i++ {
		if d := times[i].Sub(times[i-1]); d != 20*time.Millisecond {
			t.Errorf("tick %d %v after the previous one, want 20ms", i, d)
		}
	}
}

// A participant's tile stream: their latest frame scaled to fill the
// frame, or their name card without fresh video, or when they are gone.
func TestRoom_DrawTile(t *testing.T) {
	r := NewRoom(64, 36, 9)
	now := time.Now()
	r.SetParticipant("a", "Alice", false, now)
	dst := NewFrame(320, 180)
	center := func() byte { return dst.Y[90*320+160] }
	corner := func() byte { return dst.Y[2*320+2] } // Away from the name label

	if r.DrawTile(dst, "a", "Alice", now) {
		t.Error("video reported before any frame")
	}
	if corner() != 40 {
		t.Errorf("no card: corner %d, want the card's 40", corner())
	}
	r.PushVideo("a", video(200), now)
	if !r.DrawTile(dst, "a", "Alice", now) || center() != 200 {
		t.Errorf("video tile: centre %d, want 200", center())
	}
	if r.DrawTile(dst, "a", "Alice", now.Add(videoStale+time.Second)) {
		t.Error("stale video reported as video")
	}
	r.Remove("a")
	if r.DrawTile(dst, "a", "Alice", now) {
		t.Error("a participant who left has video")
	}
	if corner() != 40 {
		t.Errorf("a participant who left: corner %d, want the card's 40", corner())
	}
}
