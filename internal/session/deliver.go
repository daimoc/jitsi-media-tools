package session

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/daimoc/jitsi-media-tools/internal/compose"
	"github.com/daimoc/jitsi-media-tools/internal/output"
)

// Callbacks of received media. Buffers are valid during the call only; a
// callback that is slow loses items (drop-oldest), and never delays the
// session or the other callbacks.
type (
	// VideoFunc gets a participant's decoded frames (id "" for the grid).
	VideoFunc func(id string, f *compose.Frame, at time.Time)
	// AudioFunc gets 20 ms chunks of 48 kHz stereo (compose.ChunkSamples),
	// a participant's (id) or the room's mix (id "").
	AudioFunc func(id string, samples []int16, at time.Time)
)

// deliverQueue is how many items wait for a busy callback.
const deliverQueue = 2

// deliverer runs one callback on its own goroutine. Items are copied into
// buffers the deliverer owns, and reused once the callback has returned.
type deliverer[T any] struct {
	q      *output.Queue[*T]
	free   chan *T
	cancel context.CancelFunc
}

func startDeliverer[T any](ctx context.Context, f func(*T)) *deliverer[T] {
	ctx, cancel := context.WithCancel(ctx)
	d := &deliverer[T]{q: output.NewQueue[*T](deliverQueue), free: make(chan *T, deliverQueue+2), cancel: cancel}
	go func() {
		for {
			v, ok := d.q.Pop(ctx)
			if !ok {
				return
			}
			f(v)
			select {
			case d.free <- v:
			default:
			}
		}
	}()
	return d
}

// buffer returns a free item to fill and pass to send.
func (d *deliverer[T]) buffer() *T {
	select {
	case v := <-d.free:
		return v
	default:
		return new(T)
	}
}

func (d *deliverer[T]) send(v *T) { d.q.Push(v) }

// stop ends the goroutine once the running call, if any, returns.
func (d *deliverer[T]) stop() {
	d.cancel()
	d.q.Close()
}

type videoItem struct {
	id string
	f  compose.Frame
	at time.Time
}

type audioItem struct {
	id string
	s  []int16
	at time.Time
}

// sinks are the session's media callbacks; they change at any time.
type sinks struct {
	ctx         context.Context
	video, grid atomic.Pointer[deliverer[videoItem]]
	audio, mix  atomic.Pointer[deliverer[audioItem]]
}

func setVideoSink(ctx context.Context, p *atomic.Pointer[deliverer[videoItem]], f VideoFunc) {
	var d *deliverer[videoItem]
	if f != nil {
		d = startDeliverer(ctx, func(v *videoItem) { f(v.id, &v.f, v.at) })
	}
	if old := p.Swap(d); old != nil {
		old.stop()
	}
}

func setAudioSink(ctx context.Context, p *atomic.Pointer[deliverer[audioItem]], f AudioFunc) {
	var d *deliverer[audioItem]
	if f != nil {
		d = startDeliverer(ctx, func(v *audioItem) { f(v.id, v.s, v.at) })
	}
	if old := p.Swap(d); old != nil {
		old.stop()
	}
}

// deliverVideo copies f to the callback behind p, if any.
func deliverVideo(p *atomic.Pointer[deliverer[videoItem]], id string, f *compose.Frame, at time.Time) {
	d := p.Load()
	if d == nil {
		return
	}
	v := d.buffer()
	v.id, v.at = id, at
	if v.f.W != f.W || v.f.H != f.H {
		v.f = compose.Frame{W: f.W, H: f.H, Y: make([]byte, len(f.Y)), U: make([]byte, len(f.U)), V: make([]byte, len(f.V))}
	}
	copy(v.f.Y, f.Y)
	copy(v.f.U, f.U)
	copy(v.f.V, f.V)
	d.send(v)
}

// deliverAudio copies s to the callback behind p, if any.
func deliverAudio(p *atomic.Pointer[deliverer[audioItem]], id string, s []int16, at time.Time) {
	d := p.Load()
	if d == nil {
		return
	}
	v := d.buffer()
	v.id, v.at = id, at
	v.s = append(v.s[:0], s...)
	d.send(v)
}

func (k *sinks) stop() {
	for _, p := range []*atomic.Pointer[deliverer[videoItem]]{&k.video, &k.grid} {
		if d := p.Swap(nil); d != nil {
			d.stop()
		}
	}
	for _, p := range []*atomic.Pointer[deliverer[audioItem]]{&k.audio, &k.mix} {
		if d := p.Swap(nil); d != nil {
			d.stop()
		}
	}
}

// SetOnVideo sets the callback of each participant's decoded video (nil
// stops it). Video is decoded only while something consumes it.
func (s *Session) SetOnVideo(f VideoFunc) {
	s.setSink(func(k *sinks) { setVideoSink(k.ctx, &k.video, f) })
}

// SetOnGrid sets the callback of the composed grid, at 30 fps (id "").
func (s *Session) SetOnGrid(f VideoFunc) {
	s.setSink(func(k *sinks) { setVideoSink(k.ctx, &k.grid, f) })
}

// SetOnAudio sets the callback of each participant's audio.
func (s *Session) SetOnAudio(f AudioFunc) {
	s.setSink(func(k *sinks) { setAudioSink(k.ctx, &k.audio, f) })
}

// SetOnMix sets the callback of the room's mixed audio (id "").
func (s *Session) SetOnMix(f AudioFunc) { s.setSink(func(k *sinks) { setAudioSink(k.ctx, &k.mix, f) }) }

func (s *Session) setSink(set func(*sinks)) {
	r := s.media.recv
	if r == nil || s.ctx.Err() != nil {
		return // Not receiving (Config.Receive unset), or closed
	}
	set(&r.sinks)
}
