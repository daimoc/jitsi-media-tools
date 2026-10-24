package ffmpeg

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"sync"

	"github.com/daimoc/jitsi-media-tools/internal/rtpmedia"
)

// Encoding settings of published media (research R5).
const (
	PublishFPS        = 30
	keyframeInterval  = 2 * PublishFPS // Frames: a keyframe every 2 s
	videoBitrate      = "1500k"
	audioBitrate      = "64k"
	defaultWidth      = 1280
	defaultHeight     = 720
	encoderQueueDepth = 8 // Frames buffered between the reader and the sender
)

// EncodeOptions is what the encoder produces.
type EncodeOptions struct {
	Audio, Video  bool // Kinds encoded; at least one
	Width, Height int  // Video size; the picture is scaled to fit and padded. Default 1280x720
}

func (o EncodeOptions) size() (int, int) {
	if o.Width <= 0 || o.Height <= 0 {
		return defaultWidth, defaultHeight
	}
	return o.Width &^ 1, o.Height &^ 1 // yuv420p needs even sizes
}

// EncoderArgs returns the ffmpeg arguments that read input (ffmpeg input
// arguments, with -i) and write VP8 as IVF to stdout and Opus as Ogg to
// audioURL. Without video, Opus goes to stdout and audioURL is unused.
func EncoderArgs(input []string, o EncodeOptions, audioURL string) []string {
	args := append([]string{"-hide_banner", "-nostats", "-nostdin"}, input...)
	if o.Video {
		w, h := o.size()
		vf := fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=decrease:force_divisible_by=2,"+
			"pad=%d:%d:(ow-iw)/2:(oh-ih)/2,fps=%d,format=yuv420p", w, h, w, h, PublishFPS)
		args = append(args, "-map", "0:v:0", "-vf", vf,
			"-c:v", "libvpx", "-deadline", "realtime", "-cpu-used", "8", "-lag-in-frames", "0",
			"-error-resilient", "1", "-auto-alt-ref", "0",
			"-g", strconv.Itoa(keyframeInterval), "-keyint_min", strconv.Itoa(keyframeInterval),
			"-b:v", videoBitrate, "-maxrate", videoBitrate, "-bufsize", videoBitrate,
			"-flush_packets", "1", "-f", "ivf", "pipe:1")
	}
	if o.Audio {
		out := audioURL
		if !o.Video {
			out = "pipe:1"
		}
		args = append(args, "-map", audioMap(input),
			"-c:a", "libopus", "-ar", "48000", "-ac", "2", "-b:a", audioBitrate,
			"-frame_duration", "20", "-application", "voip",
			"-flush_packets", "1", "-page_duration", "20000", "-f", "ogg", out)
	}
	return args
}

// audioMap picks the first audio stream: of the second input when there
// are two (lavfi or devices give audio and video separately), else of the
// first.
func audioMap(input []string) string {
	n := 0
	for _, a := range input {
		if a == "-i" {
			n++
		}
	}
	if n > 1 {
		return "1:a:0"
	}
	return "0:a:0"
}

// EncoderConfig describes an encoder process.
type EncoderConfig struct {
	Bin     string
	Input   []string // ffmpeg input arguments, with -i
	Options EncodeOptions
	Logger  *slog.Logger
	// Stdin, when set, is the process's stdin (raw frames from a program).
	Stdin io.Reader
}

// Encoder is a running ffmpeg encoding one source to VP8 and Opus frames.
type Encoder struct {
	p            *Process
	video, audio chan rtpmedia.Frame
	readers      sync.WaitGroup

	mu  sync.Mutex
	err error // First reader error
}

// StartEncoder starts ffmpeg and the readers of its outputs. The frame
// channels close when ffmpeg ends its outputs; the encoder stops when ctx
// ends.
func StartEncoder(ctx context.Context, c EncoderConfig) (*Encoder, error) {
	o := c.Options
	if !o.Audio && !o.Video {
		return nil, errors.New("encoder: nothing to encode")
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	var lb *Loopback
	audioURL := ""
	if o.Audio && o.Video {
		var err error
		if lb, err = Listen(); err != nil {
			return nil, err
		}
		audioURL = lb.URL()
	}
	p, err := Start(ctx, Spec{
		Bin: c.Bin, Args: EncoderArgs(c.Input, o, audioURL), Name: "encoder",
		Stdout: true, StdinFrom: c.Stdin, NoGrace: true, Logger: c.Logger,
	})
	if err != nil {
		if lb != nil {
			lb.Close()
		}
		return nil, err
	}
	e := &Encoder{p: p}
	if o.Video {
		e.video = make(chan rtpmedia.Frame, encoderQueueDepth)
		e.read(func() (frameSource, error) {
			return rtpmedia.NewIVFReader(p.Stdout)
		}, e.video)
	}
	if o.Audio {
		e.audio = make(chan rtpmedia.Frame, encoderQueueDepth)
		if lb == nil {
			e.read(func() (frameSource, error) { return rtpmedia.NewOggOpusReader(p.Stdout), nil }, e.audio)
		} else {
			e.read(func() (frameSource, error) {
				// Accept until ffmpeg exits: then no connection will come.
				actx, cancel := context.WithCancel(ctx)
				go func() {
					select {
					case <-p.Done():
					case <-actx.Done():
					}
					cancel()
				}()
				conn, err := lb.Accept(actx)
				cancel()
				lb.Close()
				if err != nil {
					return nil, err
				}
				e.closeOnExit(conn)
				return rtpmedia.NewOggOpusReader(conn), nil
			}, e.audio)
		}
	}
	return e, nil
}

type frameSource interface {
	Next() (rtpmedia.Frame, error)
}

// read runs a reader goroutine that sends frames to ch and closes it at the
// end of the stream.
func (e *Encoder) read(open func() (frameSource, error), ch chan rtpmedia.Frame) {
	e.readers.Add(1)
	go func() {
		defer e.readers.Done()
		defer close(ch)
		src, err := open()
		if err != nil {
			e.setErr(err)
			return
		}
		for {
			f, err := src.Next()
			if err != nil {
				if !errors.Is(err, io.EOF) {
					e.setErr(err)
				}
				return
			}
			select {
			case ch <- f:
			case <-e.p.Done():
				return
			}
		}
	}()
}

// closeOnExit closes c when ffmpeg exits, so its reader ends.
func (e *Encoder) closeOnExit(c io.Closer) {
	go func() {
		<-e.p.Done()
		c.Close()
	}()
}

func (e *Encoder) setErr(err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.err == nil {
		e.err = err
	}
}

// Video returns the VP8 frames; nil without video.
func (e *Encoder) Video() <-chan rtpmedia.Frame {
	if e.video == nil {
		return nil
	}
	return e.video
}

// Audio returns the Opus packets; nil without audio.
func (e *Encoder) Audio() <-chan rtpmedia.Frame {
	if e.audio == nil {
		return nil
	}
	return e.audio
}

// Process returns the ffmpeg process.
func (e *Encoder) Process() *Process { return e.p }

// Done is closed when ffmpeg has exited.
func (e *Encoder) Done() <-chan struct{} { return e.p.Done() }

// Err returns why the encoder ended (nil for the clean end of its input);
// valid after Done.
func (e *Encoder) Err() error {
	if err := e.p.Err(); err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.err
}

// Stop ends ffmpeg and waits for the readers.
func (e *Encoder) Stop() {
	e.p.Stop()
	e.readers.Wait()
}

// EncodeBlackFrame encodes one black VP8 keyframe of the given size, the
// video placeholder sent while a source has no data (FR-010).
func EncodeBlackFrame(ctx context.Context, bin string, width, height int) ([]byte, error) {
	w, h := EncodeOptions{Width: width, Height: height}.size()
	var out bytes.Buffer
	p, err := Start(ctx, Spec{
		Bin: bin, Name: "placeholder", StdoutTo: &out,
		Args: []string{"-hide_banner", "-nostats", "-nostdin",
			"-f", "lavfi", "-i", fmt.Sprintf("color=black:size=%dx%d:rate=1", w, h), "-frames:v", "1",
			"-c:v", "libvpx", "-deadline", "realtime", "-b:v", "100k", "-f", "ivf", "pipe:1"},
	})
	if err != nil {
		return nil, err
	}
	<-p.Done()
	if err := p.Err(); err != nil {
		return nil, err
	}
	r, err := rtpmedia.NewIVFReader(&out)
	if err != nil {
		return nil, fmt.Errorf("placeholder: %w", err)
	}
	f, err := r.Next()
	if err != nil {
		return nil, fmt.Errorf("placeholder: %w", err)
	}
	return f.Data, nil
}
