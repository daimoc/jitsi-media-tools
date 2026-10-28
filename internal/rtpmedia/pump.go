package rtpmedia

import (
	"context"
	"time"
)

// OpusSilence is a 20 ms Opus packet of silence (CELT fullband, one frame),
// the audio placeholder.
var OpusSilence = []byte{0xf8, 0xff, 0xfe}

// Placeholder is what a Pump sends while its source has no data (FR-010).
type Placeholder struct {
	Frame Frame         // Sent every Frame.Duration
	After time.Duration // Source silence before the first placeholder
}

// AudioPlaceholder sends silence after 100 ms without audio.
func AudioPlaceholder() *Placeholder {
	return &Placeholder{Frame: Frame{Data: OpusSilence, Duration: OpusFrame, Samples: OpusSamples}, After: 100 * time.Millisecond}
}

// VideoPlaceholder sends keyframe (a black picture) 5 times a second after
// 500 ms without video.
func VideoPlaceholder(keyframe []byte) *Placeholder {
	const d = 200 * time.Millisecond
	return &Placeholder{Frame: Frame{Data: keyframe, Duration: d, Samples: uint32(d * VideoClockRate / time.Second), Keyframe: true},
		After: 500 * time.Millisecond}
}

// Pump sends the frames of in through s until in closes (nil) or ctx ends
// (its error). Frames are paced by their durations, except that a frame
// with others queued behind it goes out at once. While in has no frame for ph.After, it sends ph.Frame
// instead, until in has data again. A nil ph sends nothing in the gaps.
func Pump(ctx context.Context, s *Sender, in <-chan Frame, ph *Placeholder) error {
	var timer *time.Timer
	var gap <-chan time.Time
	wait := func(d time.Duration) {
		if timer == nil {
			timer = time.NewTimer(d)
		} else {
			timer.Reset(d)
		}
		gap = timer.C
	}
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	if ph == nil {
		ph = &Placeholder{} // Never used: wait does nothing without one
		wait = func(time.Duration) {}
	}
	wait(ph.After)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case f, ok := <-in:
			if !ok {
				return nil
			}
			if timer != nil && !timer.Stop() {
				select { // Drain a fired timer before Reset
				case <-timer.C:
				default:
				}
			}
			send := s.Send
			if len(in) > 0 {
				// More frames wait behind this one: the source is behind
				// (a live source's start-up burst), so catch up rather than
				// keep the backlog as latency.
				send = s.SendNow
			}
			if err := send(ctx, f); err != nil {
				return err
			}
			wait(ph.After)
		case <-gap:
			if err := s.Send(ctx, ph.Frame); err != nil {
				return err
			}
			// Send waited until the placeholder was due: the next one is due
			// one frame later.
			wait(ph.Frame.Duration)
		}
	}
}
