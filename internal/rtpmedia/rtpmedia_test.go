package rtpmedia

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v3/pkg/media/ivfwriter"
	"github.com/pion/webrtc/v3/pkg/media/oggwriter"
)

func TestIVFReader_FramesAndDurations(t *testing.T) {
	var buf bytes.Buffer
	w, err := ivfwriter.NewWith(&buf, ivfwriter.WithCodec("video/VP8"))
	if err != nil {
		t.Fatal(err)
	}
	// ivfwriter writes RTP: one VP8 packet per frame (keyframe, then delta).
	frames := [][]byte{{0x10, 0x00, 0x00, 0x9d, 0x01, 0x2a, 1, 2}, {0x11, 3, 4}, {0x11, 5}}
	for i, f := range frames {
		pkt := &rtp.Packet{Header: rtp.Header{Marker: true, SequenceNumber: uint16(i), Timestamp: uint32(i) * 3000}, Payload: f}
		if err := w.WriteRTP(pkt); err != nil {
			t.Fatal(err)
		}
	}
	w.Close()

	r, err := NewIVFReader(&buf)
	if err != nil {
		t.Fatal(err)
	}
	var got []Frame
	for {
		f, err := r.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, f)
	}
	if len(got) != 3 {
		t.Fatalf("got %d frames, want 3", len(got))
	}
	if !got[0].Keyframe || got[1].Keyframe {
		t.Errorf("keyframe flags %v %v, want true false", got[0].Keyframe, got[1].Keyframe)
	}
	for i, f := range got {
		if f.Duration < 30*time.Millisecond || f.Duration > 36*time.Millisecond {
			t.Errorf("frame %d duration %v, want ~33ms", i, f.Duration)
		}
	}
}

func TestOggOpusReader_SplitsPagesIntoPackets(t *testing.T) {
	var buf bytes.Buffer
	w, err := oggwriter.NewWith(&buf, 48000, 2)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		pkt := &rtp.Packet{Header: rtp.Header{SequenceNumber: uint16(i), Timestamp: uint32(i) * 960}, Payload: bytes.Repeat([]byte{byte(i + 1)}, 300)}
		if err := w.WriteRTP(pkt); err != nil {
			t.Fatal(err)
		}
	}
	w.Close()
	// A hand-made page with two packets (lacing 3, 2).
	page := append([]byte("OggS\x00\x00"), make([]byte, 20)...)
	page = append(page, 2, 3, 2, 'a', 'b', 'c', 'd', 'e')
	buf.Write(page)

	r := NewOggOpusReader(&buf)
	var sizes []int
	for {
		f, err := r.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if f.Samples != OpusSamples {
			t.Errorf("samples %d, want %d", f.Samples, OpusSamples)
		}
		sizes = append(sizes, len(f.Data))
	}
	want := []int{300, 300, 300, 300, 300, 3, 2}
	if len(sizes) != len(want) {
		t.Fatalf("packet sizes %v, want %v", sizes, want)
	}
	for i := range want {
		if sizes[i] != want[i] {
			t.Fatalf("packet sizes %v, want %v", sizes, want)
		}
	}
}

// fakeClock advances only when slept on.
type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time { return c.now }
func (c *fakeClock) SleepUntil(_ context.Context, t time.Time) bool {
	if t.After(c.now) {
		c.now = t
	}
	return true
}

type recorder struct{ pkts []*rtp.Packet }

func (r *recorder) WriteRTP(p *rtp.Packet) error { r.pkts = append(r.pkts, p); return nil }

func TestSender_TimestampsAndReanchoring(t *testing.T) {
	rec := &recorder{}
	clk := &fakeClock{now: time.Unix(1000, 0)}
	s := NewAudioSender(rec)
	s.clock = clk
	ctx := context.Background()
	f := Frame{Data: []byte{1, 2, 3}, Duration: OpusFrame, Samples: OpusSamples}
	for i := 0; i < 3; i++ {
		s.Send(ctx, f)
	}
	if rec.pkts[0].Timestamp != initialRTPTimestamp || rec.pkts[2].Timestamp != initialRTPTimestamp+2*OpusSamples {
		t.Fatalf("timestamps %d %d, want %d %d", rec.pkts[0].Timestamp, rec.pkts[2].Timestamp, initialRTPTimestamp, initialRTPTimestamp+2*OpusSamples)
	}
	// A 2 s gap (source restarted): the timestamp follows the wall clock.
	clk.now = clk.now.Add(2 * time.Second)
	s.Send(ctx, f)
	got := rec.pkts[3].Timestamp - rec.pkts[2].Timestamp
	if want := uint32(2 * OpusClockRate); got < want-10 || got > want+10 {
		t.Errorf("timestamp step after a 2 s gap %d, want ~%d", got, want)
	}
	for i, p := range rec.pkts {
		if p.SequenceNumber != uint16(i) {
			t.Errorf("packet %d has sequence number %d", i, p.SequenceNumber)
		}
	}
}

