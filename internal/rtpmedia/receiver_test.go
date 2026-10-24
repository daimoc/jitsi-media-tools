package rtpmedia

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"github.com/pion/rtp"
	"github.com/pion/rtp/codecs"
	"github.com/pion/webrtc/v3/pkg/media/ivfreader"
)

// packetize splits frames into RTP packets with p, one timestamp per frame.
func packetize(p rtp.Payloader, frames [][]byte, mtu uint16) []*rtp.Packet {
	var out []*rtp.Packet
	seq := uint16(65530) // Wraps
	for i, f := range frames {
		payloads := p.Payload(mtu, f)
		for j, pl := range payloads {
			out = append(out, &rtp.Packet{Header: rtp.Header{Version: 2, SequenceNumber: seq, Timestamp: 1<<30 + uint32(i)*3000, Marker: j == len(payloads)-1}, Payload: pl})
			seq++
		}
	}
	return out
}

func TestReceiver_VP8ToIVF(t *testing.T) {
	frames := [][]byte{append([]byte{0x10, 0, 0, 0x9d, 0x01, 0x2a}, bytes.Repeat([]byte{1}, 3000)...), bytes.Repeat([]byte{0x11}, 50), bytes.Repeat([]byte{0x11, 2}, 900), {0x11, 9}}
	var buf bytes.Buffer
	r, err := NewReceiver("video/VP8", &buf)
	if err != nil {
		t.Fatal(err)
	}
	keyframeRequests := 0
	r.NeedKeyframe = func() { keyframeRequests++ }
	for _, p := range packetize(&codecs.VP8Payloader{EnablePictureID: true}, frames, 1200) {
		if err := r.Push(p); err != nil {
			t.Fatal(err)
		}
	}
	ir, header, err := ivfreader.NewWith(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if header.FourCC != "VP80" || header.TimebaseDenominator != 90000 {
		t.Errorf("header %+v", header)
	}
	var got [][]byte
	var stamps []uint64
	for {
		f, fh, err := ir.ParseNextFrame()
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, f)
		stamps = append(stamps, fh.Timestamp)
	}
	// The sample builder releases a frame once the next one starts: all but
	// the last are out.
	if len(got) != len(frames)-1 {
		t.Fatalf("%d frames, want %d", len(got), len(frames)-1)
	}
	for i := range got {
		if !bytes.Equal(got[i], frames[i]) {
			t.Errorf("frame %d differs (%d vs %d bytes)", i, len(got[i]), len(frames[i]))
		}
		if stamps[i] != uint64(i)*3000 {
			t.Errorf("frame %d timestamp %d", i, stamps[i])
		}
	}
	if keyframeRequests != 1 {
		t.Errorf("%d keyframe requests, want 1 (at start)", keyframeRequests)
	}
}

func TestReceiver_LossAsksForKeyframe(t *testing.T) {
	frames := make([][]byte, 40) // 1.3 s: past the late frame limit
	for i := range frames {
		frames[i] = bytes.Repeat([]byte{0x11}, 2000) // Two packets each
	}
	r, _ := NewReceiver("video/VP8", io.Discard)
	requests := 0
	r.NeedKeyframe = func() { requests++ }
	for i, p := range packetize(&codecs.VP8Payloader{}, frames, 1200) {
		if i == 4 { // Second packet of frame 2
			continue
		}
		r.Push(p)
	}
	if requests < 2 {
		t.Errorf("%d keyframe requests, want one at start and one after the loss", requests)
	}
}

func TestReceiver_H264AnnexB(t *testing.T) {
	sps := []byte{0x67, 0x42, 0xc0, 0x1f}
	pps := []byte{0x68, 0xce, 0x3c, 0x80}
	idr := append([]byte{0x65}, bytes.Repeat([]byte{7}, 2500)...)
	var au1 []byte
	for _, nal := range [][]byte{sps, pps, idr} {
		au1 = append(append(au1, 0, 0, 0, 1), nal...)
	}
	au2 := append([]byte{0, 0, 0, 1, 0x41}, bytes.Repeat([]byte{3}, 10)...)
	var buf bytes.Buffer
	r, _ := NewReceiver("video/H264", &buf)
	for _, p := range packetize(&codecs.H264Payloader{}, [][]byte{au1, au2, au2}, 1200) {
		r.Push(p)
	}
	out := buf.Bytes()
	if !bytes.Contains(out, append([]byte{0, 0, 0, 1}, sps...)) || !bytes.Contains(out, idr[:100]) {
		t.Errorf("Annex B output lacks SPS or IDR (%d bytes)", len(out))
	}
}

func TestReceiver_OpusToOgg(t *testing.T) {
	var buf bytes.Buffer
	r, _ := NewReceiver("audio/opus", &buf)
	for i := 0; i < 4; i++ {
		r.Push(&rtp.Packet{Header: rtp.Header{SequenceNumber: uint16(i), Timestamp: uint32(i) * 960}, Payload: []byte{byte(i + 1), 0xaa}})
	}
	o := NewOggOpusReader(&buf)
	n := 0
	for {
		f, err := o.Next()
		if err != nil {
			break
		}
		if f.Data[0] != byte(n+1) {
			t.Errorf("packet %d starts with %d", n, f.Data[0])
		}
		n++
	}
	if n != 4 {
		t.Errorf("%d packets, want 4", n)
	}
}

func TestFormat(t *testing.T) {
	for mime, want := range map[string]string{"video/VP8": "ivf", "video/VP9": "ivf", "video/H264": "h264", "audio/opus": "ogg"} {
		if got, err := Format(mime); err != nil || got != want {
			t.Errorf("%s: %q %v", mime, got, err)
		}
	}
	if _, err := Format("video/AV1"); err == nil {
		t.Error("AV1 should be unsupported")
	}
}
