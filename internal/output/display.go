package output

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"sync"

	"github.com/daimoc/jitsi-media-tools/internal/ffmpeg"
)

// Display queue depths: 1 frame of video and 40 ms of audio wait for a
// slow window before the oldest is dropped, so a slow window stays late by
// at most that much. A deeper queue keeps a standing backlog once it has
// filled (docs/latency.md).
const (
	displayVideoQueue = 1
	displayAudioQueue = 2
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
	queue  *stampedQueue
	cancel context.CancelFunc
	wg     sync.WaitGroup
	done   chan struct{}

	stopOnce sync.Once
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
	d := &Display{cancel: cancel, done: make(chan struct{}), out: c.Out}
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
	d.queue = newStampedQueue(displayVideoQueue, displayAudioQueue)
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
		p, ok := d.queue.pop(ctx)
		if !ok || p.writeTo(d.nut) != nil {
			return
		}
	}
}

// WriteVideo queues a grid frame (Width×Height yuv420p) made now; the
// display keeps b, so the caller must not reuse it.
func (d *Display) WriteVideo(b []byte) { d.queue.pushVideo(b) }

// WriteAudio queues a 20 ms mix chunk (s16le 48 kHz stereo) made now; the
// display keeps b.
func (d *Display) WriteAudio(b []byte) { d.queue.pushAudio(b) }

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
		d.queue.close()
		if d.play != nil {
			d.play.Stop() // A write blocked on the window fails
		}
		d.wg.Wait()
		d.cancel()
		d.out.Close()
	})
}
