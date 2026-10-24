package rtpmedia

import (
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/pion/webrtc/v3/pkg/media/ivfreader"
)

// defaultFrameDuration is a video frame's duration when the stream gives no
// next timestamp yet (30 fps).
const defaultFrameDuration = time.Second / 30

// IVFReader reads VP8 frames from an IVF stream as ffmpeg writes it, one at
// a time. A frame's duration is known when the next one arrives, so each
// frame is returned with the duration of the gap before it was preceded,
// which is the previous frame's: the first one gets 1/30 s.
type IVFReader struct {
	r        *ivfreader.IVFReader
	num, den uint64
	last     uint64
	started  bool
	Width    int
	Height   int
}

// NewIVFReader reads the IVF header from r; it must announce VP8.
func NewIVFReader(r io.Reader) (*IVFReader, error) {
	ir, header, err := ivfreader.NewWith(r)
	if err != nil {
		return nil, fmt.Errorf("ivf: %w", err)
	}
	if header.FourCC != "VP80" {
		return nil, fmt.Errorf("ivf: expected VP8, got %q", header.FourCC)
	}
	if header.TimebaseDenominator == 0 {
		return nil, errors.New("ivf: zero timebase")
	}
	return &IVFReader{r: ir, num: uint64(header.TimebaseNumerator), den: uint64(header.TimebaseDenominator),
		Width: int(header.Width), Height: int(header.Height)}, nil
}

// Next returns the next frame; io.EOF at the end of the stream. The frame's
// Duration and Samples are the time since the previous frame (the time this
// frame is due after it).
func (r *IVFReader) Next() (Frame, error) {
	data, fh, err := r.r.ParseNextFrame()
	if err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) {
			err = io.EOF
		}
		return Frame{}, err
	}
	d := defaultFrameDuration
	if r.started && fh.Timestamp > r.last {
		units := fh.Timestamp - r.last
		d = time.Duration(units * r.num * uint64(time.Second) / r.den)
	}
	r.last, r.started = fh.Timestamp, true
	return Frame{
		Data:     data,
		Duration: d,
		Samples:  uint32(d * VideoClockRate / time.Second),
		Keyframe: isVP8Keyframe(data),
	}, nil
}
