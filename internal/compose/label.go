package compose

import (
	"image"
	"image/color"

	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"
)

// labelScale enlarges basicfont's 7×13 glyphs.
const labelScale = 2

// labelHeight is the height of a label's box.
const labelHeight = 13*labelScale + 8

// DrawLabel writes text in white on a dark box with its bottom-left corner
// at (x, y), clipped to maxW pixels wide.
func DrawLabel(dst *Frame, x, y, maxW int, text string) {
	if text == "" || maxW <= 8 {
		return
	}
	face := basicfont.Face7x13
	d := font.Drawer{Face: face}
	tw := d.MeasureString(text).Ceil()
	mask := image.NewAlpha(image.Rect(0, 0, tw, 13))
	d.Dst, d.Src, d.Dot = mask, image.NewUniform(color.Alpha{255}), fixed.P(0, 10)
	d.DrawString(text)

	boxW := even(min(tw*labelScale+8, maxW))
	box := Rect{X: even(x), Y: even(y - labelHeight), W: boxW, H: labelHeight}.clip(dst.W, dst.H)
	// Semi-dark box: darken luma, neutral chroma.
	for j := box.Y; j < box.Y+box.H; j++ {
		row := dst.Y[j*dst.W:]
		for i := box.X; i < box.X+box.W; i++ {
			row[i] = 16 + (row[i]-16)/4
		}
	}
	for j := box.Y / 2; j < (box.Y+box.H)/2; j++ {
		for i := box.X / 2; i < (box.X+box.W)/2; i++ {
			dst.U[j*dst.W/2+i] = 128
			dst.V[j*dst.W/2+i] = 128
		}
	}
	ox, oy := box.X+4, box.Y+4
	for gy := 0; gy < 13; gy++ {
		for gx := 0; gx < tw; gx++ {
			if mask.AlphaAt(gx, gy).A < 128 {
				continue
			}
			for sy := 0; sy < labelScale; sy++ {
				for sx := 0; sx < labelScale; sx++ {
					px, py := ox+gx*labelScale+sx, oy+gy*labelScale+sy
					if px < box.X+box.W-2 && py < dst.H && px < dst.W {
						dst.Y[py*dst.W+px] = 235
					}
				}
			}
		}
	}
}

// DrawCard paints a name card: dark grey with the name centered, for a
// participant without video.
func DrawCard(dst *Frame, r Rect, name string) {
	dst.FillRect(r, 40, 128, 128)
	d := font.Drawer{Face: basicfont.Face7x13}
	tw := d.MeasureString(name).Ceil()*labelScale + 8
	x := r.X + max(0, (r.W-tw)/2)
	DrawLabel(dst, x, r.Y+r.H/2+labelHeight/2, r.W-(x-r.X), name)
}
