package compose

// ScaleInto draws src scaled to fit dst's rectangle r, keeping its aspect
// ratio (letterboxed with black), bilinear.
func ScaleInto(dst *Frame, r Rect, src *Frame) {
	if src.W == 0 || src.H == 0 || r.W <= 0 || r.H <= 0 {
		return
	}
	// Fit: the largest even rectangle with src's aspect inside r.
	w, h := r.W, r.W*src.H/src.W
	if h > r.H {
		h, w = r.H, r.H*src.W/src.H
	}
	w, h = even(max(w, 2)), even(max(h, 2))
	fit := Rect{X: even(r.X + (r.W-w)/2), Y: even(r.Y + (r.H-h)/2), W: w, H: h}
	if fit.W != r.W || fit.H != r.H {
		dst.FillRect(r, 16, 128, 128)
	}
	fit = fit.clip(dst.W, dst.H)
	scalePlane(dst.Y, dst.W, fit.X, fit.Y, fit.W, fit.H, src.Y, src.W, src.H)
	scalePlane(dst.U, dst.W/2, fit.X/2, fit.Y/2, fit.W/2, fit.H/2, src.U, src.W/2, src.H/2)
	scalePlane(dst.V, dst.W/2, fit.X/2, fit.Y/2, fit.W/2, fit.H/2, src.V, src.W/2, src.H/2)
}

// scalePlane scales one plane (bilinear, 16.16 fixed point) into a
// dw×dh area at (dx, dy) of a plane of stride dstride.
func scalePlane(dst []byte, dstride, dx, dy, dw, dh int, src []byte, sw, sh int) {
	if dw <= 0 || dh <= 0 || sw <= 0 || sh <= 0 {
		return
	}
	xstep := (sw << 16) / dw
	ystep := (sh << 16) / dh
	maxX, maxY := sw-1, sh-1
	sy := (ystep >> 1) - 1<<15
	for j := 0; j < dh; j, sy = j+1, sy+ystep {
		y0 := max(0, sy>>16)
		fy := sy & 0xffff
		if sy < 0 {
			fy = 0
		}
		y1 := min(y0+1, maxY)
		y0 = min(y0, maxY)
		row0, row1 := src[y0*sw:], src[y1*sw:]
		out := dst[(dy+j)*dstride+dx:]
		sx := (xstep >> 1) - 1<<15
		for i := 0; i < dw; i, sx = i+1, sx+xstep {
			x0 := max(0, sx>>16)
			fx := sx & 0xffff
			if sx < 0 {
				fx = 0
			}
			x1 := min(x0+1, maxX)
			x0 = min(x0, maxX)
			top := int(row0[x0])*(0x10000-fx) + int(row0[x1])*fx
			bot := int(row1[x0])*(0x10000-fx) + int(row1[x1])*fx
			out[i] = byte(((top>>16)*(0x10000-fy) + (bot>>16)*fy) >> 16)
		}
	}
}
