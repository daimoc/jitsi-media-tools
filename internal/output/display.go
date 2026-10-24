package output

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"strconv"
	"sync"

	"github.com/daimoc/jitsi-media-tools/internal/ffmpeg"
)

// Display queue depths: about 0.3 s of video and 0.4 s of audio wait for a
// slow window before the oldest is dropped.
const (
	displayVideoQueue = 10
	displayAudioQueue = 20
)

// DisplayConfig describes the window.
type DisplayConfig struct {
	FFmpeg, FFplay string
	Title          string
	Width, Height  int // Grid size
	Logger         *slog.Logger
	// Out, when set, receives the muxed stream instead of ffplay (tests).
	Out *os.File
}

// Display shows the grid and plays the mixed audio (research R11): one
// ffmpeg muxes the raw grid (stdin) and the raw mix (loopback socket) into
// NUT, piped into ffplay.
type Display struct {
	mux, play *ffmpeg.Process
	video     *Queue[[]byte]
	audio     *Queue[[]byte]
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	done      chan struct{}
	stopOnce  sync.Once
}

// DisplayArgs returns the arguments of the muxing ffmpeg.
func DisplayArgs(w, h int, audioURL string) []string {
	return []string{
		"-hide_banner", "-nostats", "-nostdin",
		"-probesize", "32", "-analyzeduration", "0",
		"-f", "rawvideo", "-pix_fmt", "yuv420p", "-s", strconv.Itoa(w) + "x" + strconv.Itoa(h), "-r", "30", "-i", "pipe:0",
		"-f", "s16le", "-ar", strconv.Itoa(ffmpeg.DecodedSampleRate), "-ac", "2", "-i", audioURL,
		"-map", "0:v", "-map", "1:a", "-c:v", "rawvideo", "-c:a", "pcm_s16le",
		"-flush_packets", "1", "-f", "nut", "pipe:1",
	}
}

// PlayerArgs returns the arguments of ffplay reading the NUT stream.
func PlayerArgs(title string) []string {
	return []string{
		"-hide_banner", "-loglevel", "error",
		"-fflags", "nobuffer", "-flags", "low_delay", "-framedrop",
		"-window_title", title, "-i", "pipe:0",
	}
}

// StartDisplay opens the window. Write grid frames and mix chunks at their
// constant rates; Done is closed when the window goes away.
func StartDisplay(ctx context.Context, c DisplayConfig) (*Display, error) {
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	ctx, cancel := context.WithCancel(ctx)
	d := &Display{
		video: NewQueue[[]byte](displayVideoQueue), audio: NewQueue[[]byte](displayAudioQueue),
		cancel: cancel, done: make(chan struct{}),
	}
	fail := func(err error) (*Display, error) {
		d.Stop()
		return nil, err
	}
	// The NUT stream goes from the muxer to the player through an OS pipe.
	out := c.Out
	if out == nil {
		r, w, err := os.Pipe()
		if err != nil {
			return fail(err)
		}
		d.play, err = ffmpeg.Start(ctx, ffmpeg.Spec{Bin: c.FFplay, Args: PlayerArgs(c.Title), Name: "ffplay", StdinFrom: r, NoGrace: true, Logger: c.Logger})
		r.Close()
		if err != nil {
			w.Close()
			return fail(err)
		}
		out = w
		defer w.Close() // The muxer holds its own copy
	}
	lb, err := ffmpeg.Listen()
	if err != nil {
		return fail(err)
	}
	d.mux, err = ffmpeg.Start(ctx, ffmpeg.Spec{
		Bin: c.FFmpeg, Args: DisplayArgs(c.Width, c.Height, lb.URL()), Name: "display",
		Stdin: true, StdoutTo: out, NoGrace: true, Logger: c.Logger,
	})
	if err != nil {
		lb.Close()
		return fail(err)
	}
	d.wg.Add(2)
	go func() {
		defer d.wg.Done()
		d.send(ctx, d.video, d.mux.Stdin)
	}()
	go func() {
		defer d.wg.Done()
		conn, err := lb.Accept(ctx)
		lb.Close()
		if err != nil {
			return
		}
		defer conn.Close()
		d.send(ctx, d.audio, conn)
	}()
	go func() {
		// The window closed (or the muxer died): the display is over.
		if d.play != nil {
			select {
			case <-d.play.Done():
			case <-d.mux.Done():
			}
		} else {
			<-d.mux.Done()
		}
		close(d.done)
	}()
	return d, nil
}

// send writes queued buffers to w until ctx ends or a write fails.
func (d *Display) send(ctx context.Context, q *Queue[[]byte], w io.Writer) {
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

// WriteVideo queues a grid frame (Width×Height yuv420p); the display keeps
// b, so the caller must not reuse it.
func (d *Display) WriteVideo(b []byte) { d.video.Push(b) }

// WriteAudio queues a 20 ms mix chunk (s16le 48 kHz stereo); the display
// keeps b.
func (d *Display) WriteAudio(b []byte) { d.audio.Push(b) }

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
	if d.mux != nil {
		select {
		case <-d.mux.Done():
			return d.mux.Err()
		default:
		}
	}
	return nil
}

// Stop closes the window and returns once its processes have exited.
func (d *Display) Stop() {
	d.stopOnce.Do(func() {
		d.cancel()
		d.video.Close()
		d.audio.Close()
		if d.mux != nil {
			d.mux.Stop()
		}
		if d.play != nil {
			d.play.Stop()
		}
		d.wg.Wait()
	})
}
