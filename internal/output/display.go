package output

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/daimoc/jitsi-media-tools/internal/ffmpeg"
)

// Display queue depths: about 0.1 s of video and 0.2 s of audio wait for a
// slow window before the oldest is dropped, so a slow window stays late by
// at most that much.
const (
	displayVideoQueue = 3
	displayAudioQueue = 10
)

// Audio chunk and video frame intervals the display is written at.
const (
	displayChunk = 20 * time.Millisecond
	displayFrame = time.Second / 30
)

// DisplayConfig describes the window.
type DisplayConfig struct {
	FFplay        string
	Title         string
	Width, Height int // Grid size
	Logger        *slog.Logger
	// Out, when set, receives the NUT stream instead of ffplay (tests); the
	// display closes it at Stop.
	Out *os.File
}

// Display shows the grid and plays the mixed audio (research R11): the
// session writes both as one NUT stream into ffplay, each frame and chunk
// stamped with the time it was written, so neither waits for the other.
type Display struct {
	play   *ffmpeg.Process
	out    *os.File
	nut    *nutWriter
	start  time.Time
	cancel context.CancelFunc
	wg     sync.WaitGroup
	done   chan struct{}

	mu        sync.Mutex
	pending   []displayPacket
	video     int // Video packets pending
	audio     int
	lastVideo time.Duration // Time of the last frame queued, -1 before
	lastAudio time.Duration
	ready     chan struct{}
	closed    bool
	stopOnce  sync.Once
}

type displayPacket struct {
	video bool
	at    time.Duration
	data  []byte
}

// PlayerArgs returns the arguments of ffplay reading the NUT stream.
func PlayerArgs(title string) []string {
	return []string{
		"-hide_banner", "-loglevel", "error",
		"-fflags", "nobuffer", "-flags", "low_delay", "-framedrop",
		"-probesize", "32", "-analyzeduration", "0",
		"-window_title", title, "-f", "nut", "-i", "pipe:0",
	}
}

// StartDisplay opens the window. Write grid frames and mix chunks as they
// are made; Done is closed when the window goes away.
func StartDisplay(ctx context.Context, c DisplayConfig) (*Display, error) {
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	ctx, cancel := context.WithCancel(ctx)
	d := &Display{
		cancel: cancel, done: make(chan struct{}), ready: make(chan struct{}, 1),
		lastVideo: -1, lastAudio: -1, out: c.Out,
	}
	if d.out == nil {
		r, w, err := os.Pipe()
		if err != nil {
			cancel()
			return nil, err
		}
		d.play, err = ffmpeg.Start(ctx, ffmpeg.Spec{Bin: c.FFplay, Args: PlayerArgs(c.Title), Name: "ffplay", StdinFrom: r, NoGrace: true, Logger: c.Logger})
		r.Close() // ffplay holds its own copy
		if err != nil {
			w.Close()
			cancel()
			return nil, err
		}
		d.out = w
	}
	d.nut = newNUTWriter(d.out, c.Width, c.Height)
	d.start = time.Now()
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		d.write(ctx)
	}()
	go func() {
		// The window closed (or writing ended): the display is over.
		if d.play != nil {
			<-d.play.Done()
		} else {
			d.wg.Wait()
		}
		close(d.done)
	}()
	return d, nil
}

// write sends queued packets to the player in the order they came, until
// ctx ends, the queue closes and is empty, or a write fails.
func (d *Display) write(ctx context.Context) {
	for {
		d.mu.Lock()
		if len(d.pending) == 0 {
			closed := d.closed
			d.mu.Unlock()
			if closed {
				return
			}
			select {
			case <-d.ready:
				continue
			case <-ctx.Done():
				return
			}
		}
		p := d.pending[0]
		d.pending = d.pending[1:]
		if p.video {
			d.video--
		} else {
			d.audio--
		}
		d.mu.Unlock()
		var err error
		if p.video {
			err = d.nut.video(p.at, p.data)
		} else {
			err = d.nut.audio(p.at, p.data)
		}
		if err != nil {
			return
		}
	}
}

// push queues a packet, dropping the oldest of its kind when too many wait.
func (d *Display) push(p displayPacket) {
	if p.video {
		d.video++
	} else {
		d.audio++
	}
	d.pending = append(d.pending, p)
	if d.video > displayVideoQueue || d.audio > displayAudioQueue {
		for i, q := range d.pending {
			if q.video == p.video {
				d.pending = append(d.pending[:i], d.pending[i+1:]...)
				break
			}
		}
		if p.video {
			d.video--
		} else {
			d.audio--
		}
	}
	select {
	case d.ready <- struct{}{}:
	default:
	}
}

// WriteVideo queues a grid frame (Width×Height yuv420p) made now; the
// display keeps b, so the caller must not reuse it. A frame closer than
// half a frame interval to the previous one (a clock catching up after a
// stall) is dropped.
func (d *Display) WriteVideo(b []byte) {
	at := time.Since(d.start)
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed || d.lastVideo >= 0 && at-d.lastVideo < displayFrame/2 {
		return
	}
	d.lastVideo = at
	d.push(displayPacket{video: true, at: at, data: b})
}

// WriteAudio queues a 20 ms mix chunk (s16le 48 kHz stereo) made now; the
// display keeps b. Chunks written back to back get consecutive times, as
// their samples play one after the other.
func (d *Display) WriteAudio(b []byte) {
	at := time.Since(d.start)
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return
	}
	if d.lastAudio >= 0 {
		at = max(at, d.lastAudio+displayChunk)
	}
	d.lastAudio = at
	d.push(displayPacket{at: at, data: b})
}

// Done is closed when the window has gone (closed by the user, or failed).
func (d *Display) Done() <-chan struct{} { return d.done }

// Err tells why the display ended early, if it failed.
func (d *Display) Err() error {
	if d.play != nil {
		select {
		case <-d.play.Done():
			if err := d.play.Err(); err != nil {
				return err
			}
			return errors.New("display window closed")
		default:
		}
	}
	return nil
}

// Stop closes the window and returns once ffplay has exited. Without a
// window (Out), what is queued is written first.
func (d *Display) Stop() {
	d.stopOnce.Do(func() {
		d.mu.Lock()
		d.closed = true
		d.mu.Unlock()
		select {
		case d.ready <- struct{}{}:
		default:
		}
		if d.play != nil {
			d.play.Stop() // A write blocked on the window fails
		}
		d.wg.Wait()
		d.cancel()
		d.out.Close()
	})
}
