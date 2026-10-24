package compose

import (
	"cmp"
	"math"
	"slices"
	"time"
)

// Layout returns n tile rectangles in a w×h canvas, as Jitsi's tile view:
// ceil(√n) columns, 16:9 tiles as large as fit, the grid centered and an
// incomplete last row centered. Coordinates are even.
func Layout(n, w, h int) []Rect {
	if n <= 0 {
		return nil
	}
	cols := int(math.Ceil(math.Sqrt(float64(n))))
	rows := (n + cols - 1) / cols
	const gap = 4
	tw := (w - gap*(cols+1)) / cols
	th := tw * 9 / 16
	if maxH := (h - gap*(rows+1)) / rows; th > maxH {
		th = maxH
		tw = th * 16 / 9
	}
	tw, th = even(tw), even(th)
	gridH := rows*th + (rows-1)*gap
	y0 := (h - gridH) / 2
	out := make([]Rect, 0, n)
	for r := 0; r < rows; r++ {
		inRow := min(cols, n-r*cols)
		rowW := inRow*tw + (inRow-1)*gap
		x0 := (w - rowW) / 2
		for c := 0; c < inRow; c++ {
			out = append(out, Rect{X: even(x0 + c*(tw+gap)), Y: even(y0 + r*(th+gap)), W: tw, H: th})
		}
	}
	return out
}

// Tile is one participant in the grid.
type Tile struct {
	ID        string
	Name      string
	Frame     *Frame // Latest video frame; nil: name card
	Pinned    bool
	LastSpoke time.Time
	Order     int // Join order
}

// SortTiles orders tiles as the grid shows them: pinned first, then most
// recent speaker, then join order.
func SortTiles(tiles []Tile) {
	slices.SortStableFunc(tiles, func(a, b Tile) int {
		if a.Pinned != b.Pinned {
			if a.Pinned {
				return -1
			}
			return 1
		}
		if c := b.LastSpoke.Compare(a.LastSpoke); c != 0 {
			return c
		}
		return cmp.Compare(a.Order, b.Order)
	})
}

// DrawGrid draws tiles (already sorted) on dst, which is cleared first.
func DrawGrid(dst *Frame, tiles []Tile) {
	dst.Fill(16, 128, 128)
	for i, r := range Layout(len(tiles), dst.W, dst.H) {
		t := tiles[i]
		if t.Frame == nil {
			DrawCard(dst, r, t.Name)
			continue
		}
		ScaleInto(dst, r, t.Frame)
		DrawLabel(dst, r.X+4, r.Y+r.H-4, r.W-8, t.Name)
	}
}
