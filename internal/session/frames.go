package session

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"sync"

	"github.com/daimoc/jitsi-media-tools/internal/ffmpeg"
	"github.com/daimoc/jitsi-media-tools/internal/output"
)

// Frame queue depths: 2 frames (67 ms) of video and 2 chunks (40 ms) of
// audio wait for the encoder before the oldest is dropped. The queues fill
// while an encoder starts, and the sender paces what was queued at real
// time, so whatever they hold then stays as latency for the whole run
// (docs/latency.md).
const (
	framesVideoQueue = 2
	framesAudioQueue = 2
)

// ErrFramesClosed is returned by writes after Frames.Close.
var ErrFramesClosed = errors.New("frame source closed")

// Frames is media a program writes for the session to publish: yuv420p
// frames of W×H, meant at 30 fps, and 48 kHz stereo s16 samples, both in
// real time. They reach the encoder's raw inputs (FramesInput).
type Frames struct {
	W, H         int
	Audio, Video bool

	video, audio *output.Queue[[]byte]
	done         chan struct{}
	closeOnce    sync.Once
}

// NewFrames returns a frame source; w and h are rounded down to even.
func NewFrames(w, h int, audio, video bool) *Frames {
	return &Frames{
		W: w &^ 1, H: h &^ 1, Audio: audio, Video: video,
		video: output.NewQueue[[]byte](framesVideoQueue), audio: output.NewQueue[[]byte](framesAudioQueue),
		done: make(chan struct{}),
	}
}

func (f *Frames) closed() bool {
	select {
	case <-f.done:
		return true
	default:
		return false
	}
}

// WriteVideo queues a frame, given as its three planes; they are copied.
func (f *Frames) WriteVideo(w, h int, y, u, v []byte) error {
	switch {
	case f.closed():
		return ErrFramesClosed
	case !f.Video:
		return errors.New("frame source has no video")
	case w != f.W || h != f.H:
		return fmt.Errorf("frame is %dx%d, the source is %dx%d", w, h, f.W, f.H)
	case len(y) != w*h || len(u) != w*h/4 || len(v) != w*h/4:
		return fmt.Errorf("frame planes are %d, %d and %d bytes, want %d, %d and %d", len(y), len(u), len(v), w*h, w*h/4, w*h/4)
	}
	b := make([]byte, 0, w*h*3/2)
	f.video.Push(append(append(append(b, y...), u...), v...))
	return nil
}

// WriteAudio queues interleaved stereo samples; they are copied.
func (f *Frames) WriteAudio(samples []int16) error {
	switch {
	case f.closed():
		return ErrFramesClosed
	case !f.Audio:
		return errors.New("frame source has no audio")
	case len(samples)%2 != 0:
		return fmt.Errorf("%d samples: stereo needs an even count", len(samples))
	}
	f.audio.Push(pcmBytes(samples))
	return nil
}

// Close ends the source: the encoder finishes what is queued, then the
// session sends placeholders.
func (f *Frames) Close() {
	f.closeOnce.Do(func() {
		close(f.done)
		f.video.Close()
		f.audio.Close()
	})
}

// FramesInput publishes what a program writes into f.
func FramesInput(f *Frames) Input { return Input{Kind: "frames", Frames: f} }

// framesRun is one encoder run's connection to the Frames: its input
// arguments and the writers feeding them.
type framesRun struct {
	f     *Frames
	args  []string
	stdin *os.File // Read end, the encoder's stdin
	video *os.File // Write end
	lb    *ffmpeg.Loopback
}

// openFrames prepares an encoder run's inputs: raw video on stdin, audio
// as NUT on a loopback socket the encoder connects to.
func openFrames(f *Frames) (*framesRun, error) {
	r := &framesRun{f: f}
	if f.Video {
		var err error
		if r.stdin, r.video, err = os.Pipe(); err != nil {
			return nil, err
		}
		r.args = append(r.args, "-f", "rawvideo", "-pix_fmt", "yuv420p",
			"-s", strconv.Itoa(f.W)+"x"+strconv.Itoa(f.H), "-r", strconv.Itoa(ffmpeg.PublishFPS), "-i", "pipe:0")
	}
	if f.Audio {
		lb, err := ffmpeg.Listen()
		if err != nil {
			r.close()
			return nil, err
		}
		r.lb = lb
		r.args = append(r.args, "-f", "nut", "-i", lb.URL()) // See output.AudioNUT
	}
	return r, nil
}

// feed writes the queued media into the started encoder until ctx ends,
// the encoder stops reading, or the Frames close. It closes the inputs.
func (r *framesRun) feed(ctx context.Context, wg *sync.WaitGroup) {
	if r.stdin != nil {
		r.stdin.Close() // The encoder holds its own copy
		r.stdin = nil
	}
	send := func(q *output.Queue[[]byte], w io.Writer) {
		for {
			b, ok := q.Pop(ctx)
			if !ok {
				return
			}
			if _, err := w.Write(b); err != nil {
				return
			}
		}
	}
	if r.video != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer r.video.Close()
			send(r.f.video, r.video)
		}()
	}
	if r.lb != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			conn, err := r.lb.Accept(ctx)
			r.lb.Close()
			if err != nil {
				return
			}
			defer conn.Close()
			send(r.f.audio, output.NewAudioNUT(conn))
		}()
	}
}

// close releases a run that did not start.
func (r *framesRun) close() {
	for _, f := range []*os.File{r.stdin, r.video} {
		if f != nil {
			f.Close()
		}
	}
	if r.lb != nil {
		r.lb.Close()
	}
}
