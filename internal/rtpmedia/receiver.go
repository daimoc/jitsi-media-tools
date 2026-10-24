package rtpmedia

import (
	"encoding/binary"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/rtp/codecs"
	"github.com/pion/webrtc/v3/pkg/media/oggwriter"
	"github.com/pion/webrtc/v3/pkg/media/samplebuilder"
)

// maxLatePackets is how many packets the sample builder waits for a missing
// one before giving up on its frame (about 0.5 s of video).
const maxLatePackets = 256

// maxLateTime is how long a frame with a missing packet is waited for.
const maxLateTime = 500 * time.Millisecond

// Receiver turns a received RTP stream into the byte stream ffmpeg decodes:
// IVF for VP8 and VP9, Annex B for H.264, Ogg for Opus (research R7).
type Receiver struct {
	push func(*rtp.Packet) error
	out  io.Writer
	// NeedKeyframe, when set, is called at the first frame and whenever
	// frames were lost, so the sender is asked for a keyframe.
	NeedKeyframe func()
	started      bool
}

// Format returns the ffmpeg input format (-f) of a codec's stream.
func Format(mimeType string) (string, error) {
	switch strings.ToLower(mimeType) {
	case "video/vp8", "video/vp9":
		return "ivf", nil
	case "video/h264":
		return "h264", nil
	case "audio/opus":
		return "ogg", nil
	}
	return "", fmt.Errorf("unsupported codec %s", mimeType)
}

// NewReceiver writes the stream of codec mimeType to out.
func NewReceiver(mimeType string, out io.Writer) (*Receiver, error) {
	r := &Receiver{out: out}
	switch strings.ToLower(mimeType) {
	case "video/vp8":
		r.video(&codecs.VP8Packet{}, "VP80", out)
	case "video/vp9":
		r.video(&codecs.VP9Packet{}, "VP90", out)
	case "video/h264":
		r.video(&codecs.H264Packet{}, "", out)
	case "audio/opus":
		w, err := oggwriter.NewWith(out, OpusClockRate, 2)
		if err != nil {
			return nil, err
		}
		r.push = w.WriteRTP
	default:
		return nil, fmt.Errorf("unsupported codec %s", mimeType)
	}
	return r, nil
}

// video sets up frame reassembly; fourCC "" means Annex B (H.264, whose
// depacketizer already emits start codes).
func (r *Receiver) video(d rtp.Depacketizer, fourCC string, out io.Writer) {
	sb := samplebuilder.New(maxLatePackets, d, VideoClockRate, samplebuilder.WithMaxTimeDelay(maxLateTime))
	var (
		first  uint32
		header bool
	)
	r.push = func(p *rtp.Packet) error {
		sb.Push(p)
		for {
			s, ts := sb.PopWithTimestamp()
			if s == nil {
				return nil
			}
			if (!r.started || s.PrevDroppedPackets > 0) && r.NeedKeyframe != nil {
				r.NeedKeyframe()
			}
			r.started = true
			if fourCC == "" {
				if _, err := out.Write(s.Data); err != nil {
					return err
				}
				continue
			}
			if !header {
				if err := writeIVFHeader(out, fourCC); err != nil {
					return err
				}
				header, first = true, ts
			}
			var fh [12]byte
			binary.LittleEndian.PutUint32(fh[0:], uint32(len(s.Data)))
			binary.LittleEndian.PutUint64(fh[4:], uint64(ts-first))
			if _, err := out.Write(fh[:]); err != nil {
				return err
			}
			if _, err := out.Write(s.Data); err != nil {
				return err
			}
		}
	}
}

// writeIVFHeader writes an IVF file header with a 1/90000 timebase. The
// size fields are nominal: decoders read the real size from the stream.
func writeIVFHeader(w io.Writer, fourCC string) error {
	var h [32]byte
	copy(h[0:], "DKIF")
	binary.LittleEndian.PutUint16(h[4:], 0)  // Version
	binary.LittleEndian.PutUint16(h[6:], 32) // Header size
	copy(h[8:], fourCC)
	binary.LittleEndian.PutUint16(h[12:], 640)
	binary.LittleEndian.PutUint16(h[14:], 360)
	binary.LittleEndian.PutUint32(h[16:], VideoClockRate) // Timebase denominator
	binary.LittleEndian.PutUint32(h[20:], 1)              // Numerator
	binary.LittleEndian.PutUint32(h[24:], 0)              // Frame count (unknown)
	_, err := w.Write(h[:])
	return err
}

// Push adds a received packet.
func (r *Receiver) Push(p *rtp.Packet) error {
	if len(p.Payload) == 0 {
		return nil // Padding-only probe
	}
	return r.push(p)
}
