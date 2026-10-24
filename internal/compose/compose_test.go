package compose

import (
	"math"
	"testing"
	"time"
)

func TestLayout(t *testing.T) {
	for n := 1; n <= 25; n++ {
		rs := Layout(n, 1280, 720)
		if len(rs) != n {
			t.Fatalf("n=%d: %d rects", n, len(rs))
		}
		cols := int(math.Ceil(math.Sqrt(float64(n))))
		for i, r := range rs {
			if r.X%2 != 0 || r.Y%2 != 0 || r.W%2 != 0 || r.H%2 != 0 {
				t.Errorf("n=%d tile %d not even: %+v", n, i, r)
			}
			if r.X < 0 || r.Y < 0 || r.X+r.W > 1280 || r.Y+r.H > 720 {
				t.Errorf("n=%d tile %d outside: %+v", n, i, r)
			}
			if ratio := float64(r.W) / float64(r.H); ratio < 1.7 || ratio > 1.85 {
				t.Errorf("n=%d tile %d ratio %.2f", n, i, ratio)
			}
			for j := 0; j < i; j++ {
				o := rs[j]
				if r.X < o.X+o.W && o.X < r.X+r.W && r.Y < o.Y+o.H && o.Y < r.Y+r.H {
					t.Errorf("n=%d tiles %d and %d overlap", n, i, j)
				}
			}
		}
		if n > 1 && rs[1].Y == rs[0].Y && cols == 1 {
			t.Errorf("n=%d: unexpected layout", n)
		}
	}
	// 3 tiles: 2 columns, the last row's single tile centered.
	rs := Layout(3, 1280, 720)
	if rs[2].X+rs[2].W/2 < 630 || rs[2].X+rs[2].W/2 > 650 {
		t.Errorf("last row not centered: %+v", rs[2])
	}
}

func TestSortTiles(t *testing.T) {
	now := time.Unix(100, 0)
	tiles := []Tile{
		{ID: "a", Order: 1},
		{ID: "b", Order: 2, LastSpoke: now},
		{ID: "c", Order: 3, Pinned: true},
		{ID: "d", Order: 4, LastSpoke: now.Add(time.Second)},
	}
	SortTiles(tiles)
	var got string
	for _, t := range tiles {
		got += t.ID
	}
	if got != "cdba" {
		t.Errorf("order %s, want cdba", got)
	}
}

func TestScaleInto_Pattern(t *testing.T) {
	src := NewFrame(64, 36)
	for j := 0; j < 36; j++ {
		for i := 0; i < 64; i++ {
			if i < 32 {
				src.Y[j*64+i] = 50
			} else {
				src.Y[j*64+i] = 200
			}
		}
	}
	dst := NewFrame(320, 180)
	ScaleInto(dst, Rect{0, 0, 320, 180}, src)
	if l, r := dst.Y[90*320+20], dst.Y[90*320+300]; l != 50 || r != 200 {
		t.Errorf("left %d right %d, want 50 200", l, r)
	}
	if mid := dst.Y[90*320+160]; mid <= 50 || mid >= 200 {
		t.Errorf("edge %d not interpolated", mid)
	}
	// Taller source: letterboxed (pillars black).
	tall := NewFrame(36, 64)
	tall.Fill(200, 128, 128)
	ScaleInto(dst, Rect{0, 0, 320, 180}, tall)
	if dst.Y[90*320+5] != 16 || dst.Y[90*320+160] != 200 {
		t.Errorf("pillarbox %d center %d", dst.Y[90*320+5], dst.Y[90*320+160])
	}
}

func TestDrawGrid_LabelsAndCards(t *testing.T) {
	video := NewFrame(64, 36)
	video.Fill(120, 128, 128)
	dst := NewFrame(640, 360)
	DrawGrid(dst, []Tile{{Name: "Alice", Frame: video}, {Name: "Bob"}})
	rs := Layout(2, 640, 360)
	// Bob's card is dark grey, with white text somewhere in it.
	card := rs[1]
	if v := dst.Y[(card.Y+4)*640+card.X+4]; v != 40 {
		t.Errorf("card background %d, want 40", v)
	}
	if !hasWhite(dst, card) {
		t.Error("no name text on the card")
	}
	// Alice: video luma, label bottom-left with white text.
	a := rs[0]
	if v := dst.Y[(a.Y+10)*640+a.X+a.W/2]; v != 120 {
		t.Errorf("tile luma %d, want 120", v)
	}
	if !hasWhite(dst, Rect{a.X, a.Y + a.H - labelHeight - 4, a.W / 2, labelHeight}) {
		t.Error("no label on the video tile")
	}
}

func hasWhite(f *Frame, r Rect) bool {
	for j := r.Y; j < r.Y+r.H; j++ {
		for i := r.X; i < r.X+r.W; i++ {
			if f.Y[j*f.W+i] == 235 {
				return true
			}
		}
	}
	return false
}

func chunk(v int16) []int16 {
	c := make([]int16, ChunkSamples)
	for i := range c {
		c[i] = v
	}
	return c
}

func TestJitter(t *testing.T) {
	var j Jitter
	out := make([]int16, ChunkSamples)
	j.Push(chunk(1))
	j.Push(chunk(2))
	if j.Pop(out) {
		t.Fatal("played before 60 ms were buffered")
	}
	j.Push(chunk(3))
	if !j.Pop(out) || out[0] != 1 {
		t.Fatalf("first chunk %d", out[0])
	}
	// Overflow: past 120 ms the oldest samples go.
	for v := int16(10); v < 20; v++ {
		j.Push(chunk(v))
	}
	j.Pop(out)
	if out[0] != 14 {
		t.Errorf("after overflow, chunk %d, want 14 (the 6 newest kept)", out[0])
	}
}

func TestMixer_SumsAndSaturates(t *testing.T) {
	m := NewMixer()
	for i := 0; i < 3; i++ {
		m.Push("a", chunk(30000))
		m.Push("b", chunk(10000))
	}
	mix := make([]int16, ChunkSamples)
	seen := map[string]int16{}
	m.Tick(mix, func(id string, c []int16) { seen[id] = c[0] })
	if mix[0] != math.MaxInt16 {
		t.Errorf("mix %d, want saturated %d", mix[0], math.MaxInt16)
	}
	if seen["a"] != 30000 || seen["b"] != 10000 {
		t.Errorf("per participant %v", seen)
	}
	m.Remove("a")
	m.Tick(mix, nil)
	if mix[0] != 10000 {
		t.Errorf("mix after removing a: %d", mix[0])
	}
}
