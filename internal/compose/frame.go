// Package compose builds what the session shows and plays from decoded
// media: the tile grid, name cards and the room's audio mix, as raw yuv420p
// frames and 48 kHz stereo PCM (research R10). It is arithmetic on pixels
// and samples only: no codecs.
package compose

// Frame is a yuv420p picture: Y is W×H bytes, U and V (W/2)×(H/2).
type Frame struct {
	W, H    int
	Y, U, V []byte
}

// NewFrame returns a black frame; w and h must be even.
func NewFrame(w, h int) *Frame {
	f := &Frame{W: w, H: h, Y: make([]byte, w*h), U: make([]byte, w*h/4), V: make([]byte, w*h/4)}
	f.Fill(16, 128, 128)
	return f
}

// FrameFrom wraps a packed yuv420p buffer of w×h (len w*h*3/2) without
// copying.
func FrameFrom(w, h int, buf []byte) *Frame {
	ys, cs := w*h, w*h/4
	return &Frame{W: w, H: h, Y: buf[:ys], U: buf[ys : ys+cs], V: buf[ys+cs : ys+2*cs]}
}

// Size returns the packed size of a w×h yuv420p frame.
func Size(w, h int) int { return w * h * 3 / 2 }

// Fill paints the whole frame one color.
func (f *Frame) Fill(y, u, v byte) {
	fill(f.Y, y)
	fill(f.U, u)
	fill(f.V, v)
}

// FillRect paints a rectangle (even coordinates) one color.
func (f *Frame) FillRect(r Rect, y, u, v byte) {
	r = r.clip(f.W, f.H)
	for j := r.Y; j < r.Y+r.H; j++ {
		fill(f.Y[j*f.W+r.X:j*f.W+r.X+r.W], y)
	}
	cw := f.W / 2
	for j := r.Y / 2; j < (r.Y+r.H)/2; j++ {
		fill(f.U[j*cw+r.X/2:j*cw+(r.X+r.W)/2], u)
		fill(f.V[j*cw+r.X/2:j*cw+(r.X+r.W)/2], v)
	}
}

// Bytes appends the frame packed (Y, U, V) to dst.
func (f *Frame) Bytes(dst []byte) []byte {
	dst = append(dst, f.Y...)
	dst = append(dst, f.U...)
	return append(dst, f.V...)
}

// Clone returns a copy.
func (f *Frame) Clone() *Frame {
	return &Frame{W: f.W, H: f.H, Y: append([]byte(nil), f.Y...), U: append([]byte(nil), f.U...), V: append([]byte(nil), f.V...)}
}

func fill(b []byte, v byte) {
	for i := range b {
		b[i] = v
	}
}

// Rect is a rectangle in pixels.
type Rect struct{ X, Y, W, H int }

func (r Rect) clip(w, h int) Rect {
	if r.X < 0 {
		r.W += r.X
		r.X = 0
	}
	if r.Y < 0 {
		r.H += r.Y
		r.Y = 0
	}
	r.W = max(0, min(r.W, w-r.X))
	r.H = max(0, min(r.H, h-r.Y))
	return r
}

// even rounds down to an even number.
func even(v int) int { return v &^ 1 }
