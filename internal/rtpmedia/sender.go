package rtpmedia

import (
	"context"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/rtp/codecs"
)

// rtpPayloadMTU bounds the RTP payload of a packet, leaving room for the RTP
// header, header extensions and SRTP authentication within a 1280-byte path.
const rtpPayloadMTU = 1200

// initialRTPTimestamp is the RTP timestamp of a sender's first frame. It is
// not 0 (RFC 3550 5.1: the initial value should not be predictable) so that
// a server which rebases forwarded timestamps from 0 never yields
// timestamps of recently sent frames. It is below 2^31 because Jitsi's
// bridge keeps a video stream's timestamps only when they are newer than 0
// in RTP's modular order, and rebases them otherwise.
const initialRTPTimestamp uint32 = 1 << 30

// Pacing bounds: a frame later than maxLate (a gap: source restarted or
// stalled) or due more than maxEarly ahead (timestamps jumped) re-anchors
// the schedule on the wall clock.
const (
	maxLate  = 250 * time.Millisecond
	maxEarly = time.Second
)

// TrackWriter receives the packets of a Sender. webrtc.TrackLocalStaticRTP
// implements it, rewriting the SSRC and payload type.
type TrackWriter interface {
	WriteRTP(p *rtp.Packet) error
}

// clock abstracts time for tests.
type clock interface {
	Now() time.Time
	// SleepUntil waits until t; it reports false if ctx ended first.
	SleepUntil(ctx context.Context, t time.Time) bool
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

func (realClock) SleepUntil(ctx context.Context, t time.Time) bool {
	d := time.Until(t)
	if d <= 0 {
		return ctx.Err() == nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// Sender paces frames of one media kind and writes them to a track as RTP:
// each frame is due its Duration after the previous one, with an RTP
// timestamp its Samples after the previous one's. Frames from successive
// producers (an encoder, then a placeholder, then a restarted encoder) keep
// one continuous stream: after a gap the schedule and the timestamps follow
// the wall clock. A Sender is used by one goroutine.
type Sender struct {
	w       TrackWriter
	payload rtp.Payloader
	rate    uint32
	clock   clock

	started bool
	seq     uint16
	ts      uint32
	due     time.Time
}

// NewVideoSender sends VP8 to w.
func NewVideoSender(w TrackWriter) *Sender {
	return &Sender{w: w, payload: &codecs.VP8Payloader{EnablePictureID: true}, rate: VideoClockRate, clock: realClock{}}
}

// NewAudioSender sends Opus to w.
func NewAudioSender(w TrackWriter) *Sender {
	return &Sender{w: w, payload: &codecs.OpusPayloader{}, rate: OpusClockRate, clock: realClock{}}
}

// Send waits until f is due and writes it. It returns ctx's error if ctx
// ends first; write errors (connection closing) are ignored.
func (s *Sender) Send(ctx context.Context, f Frame) error {
	now := s.clock.Now()
	switch {
	case !s.started:
		s.started, s.ts, s.due = true, initialRTPTimestamp, now
	default:
		due := s.due.Add(f.Duration)
		if now.Sub(due) > maxLate || due.Sub(now) > maxEarly {
			// Re-anchor: the timestamp advances by the real time elapsed.
			elapsed := now.Sub(s.due)
			s.ts += uint32(int64(elapsed) * int64(s.rate) / int64(time.Second))
			s.due = now
		} else {
			s.ts += f.Samples
			s.due = due
		}
	}
	if !s.clock.SleepUntil(ctx, s.due) {
		return ctx.Err()
	}
	payloads := s.payload.Payload(rtpPayloadMTU, f.Data)
	for i, p := range payloads {
		pkt := &rtp.Packet{
			Header: rtp.Header{
				Version:        2,
				Marker:         i == len(payloads)-1,
				SequenceNumber: s.seq,
				Timestamp:      s.ts,
			},
			Payload: p,
		}
		s.seq++
		if err := s.w.WriteRTP(pkt); err != nil {
			return nil // The rest of this frame would fail too
		}
	}
	return nil
}
