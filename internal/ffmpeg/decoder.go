package ffmpeg

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strconv"
)

// Decoded audio: 20 ms chunks of 48 kHz stereo s16le.
const (
	DecodedSampleRate = 48000
	DecodedChunkBytes = 960 * 2 * 2
)

// decoderQueueDepth is how many decoded frames wait for the consumer before
// the oldest is dropped: a slow consumer loses frames, ffmpeg never stalls.
const decoderQueueDepth = 4

// DecoderConfig describes one received stream to decode.
type DecoderConfig struct {
	Bin    string
	Format string // ffmpeg input format of the stream: ivf, h264 or ogg
	Height int    // Video only: output height; the width is Height*16/9
	Logger *slog.Logger
}

// Decoder is an ffmpeg turning one received stream (written with Write)
// into raw frames of a fixed size (research R7): yuv420p video scaled and
// padded to 16:9, or 20 ms chunks of s16le 48 kHz stereo audio.
type Decoder struct {
	p      *Process
	w, h   int
	frames chan []byte
}

// lowDelayInput starts decoding at once: minimal probing of the stream.
// Not "-fflags nobuffer": with it ffmpeg drops every frame after a change
// of the stream's picture size, which the bridge's layer switches cause.
var lowDelayInput = []string{"-flags", "low_delay", "-probesize", "32", "-analyzeduration", "0"}

// VideoArgs returns the arguments of a video decoder writing w×h yuv420p.
func VideoArgs(format string, w, h int) []string {
	size := strconv.Itoa(w) + ":" + strconv.Itoa(h)
	args := []string{"-hide_banner", "-nostats", "-nostdin"}
	args = append(args, lowDelayInput...)
	return append(args,
		"-f", format, "-i", "pipe:0", "-an",
		"-vf", "scale="+size+":force_original_aspect_ratio=decrease:force_divisible_by=2,"+
			"pad="+size+":(ow-iw)/2:(oh-ih)/2,format=yuv420p",
		"-fps_mode", "passthrough", "-pix_fmt", "yuv420p", "-f", "rawvideo", "pipe:1",
	)
}

// AudioArgs returns the arguments of an audio decoder writing s16le.
func AudioArgs(format string) []string {
	args := []string{"-hide_banner", "-nostats", "-nostdin"}
	args = append(args, lowDelayInput...)
	return append(args, "-f", format, "-i", "pipe:0", "-vn",
		"-f", "s16le", "-ar", strconv.Itoa(DecodedSampleRate), "-ac", "2", "pipe:1")
}

// StartVideoDecoder starts a video decoder; frames are Width×Height yuv420p
// (Width*Height*3/2 bytes).
func StartVideoDecoder(ctx context.Context, c DecoderConfig) (*Decoder, error) {
	if c.Height <= 0 || c.Height%2 != 0 {
		return nil, fmt.Errorf("decoder: bad height %d", c.Height)
	}
	w := c.Height * 16 / 9 &^ 1
	return startDecoder(ctx, c, "video decoder", VideoArgs(c.Format, w, c.Height), w, c.Height, w*c.Height*3/2)
}

// StartAudioDecoder starts an audio decoder; frames are 20 ms chunks
// (DecodedChunkBytes).
func StartAudioDecoder(ctx context.Context, c DecoderConfig) (*Decoder, error) {
	return startDecoder(ctx, c, "audio decoder", AudioArgs(c.Format), 0, 0, DecodedChunkBytes)
}

func startDecoder(ctx context.Context, c DecoderConfig, name string, args []string, w, h, size int) (*Decoder, error) {
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	p, err := Start(ctx, Spec{Bin: c.Bin, Args: args, Name: name, Stdin: true, Stdout: true, NoGrace: true, Logger: c.Logger})
	if err != nil {
		return nil, err
	}
	d := &Decoder{p: p, w: w, h: h, frames: make(chan []byte, decoderQueueDepth)}
	go d.read(size)
	return d, nil
}

// read cuts stdout into frames until it ends.
func (d *Decoder) read(size int) {
	defer close(d.frames)
	for {
		buf := make([]byte, size)
		if _, err := io.ReadFull(d.p.Stdout, buf); err != nil {
			return
		}
		for {
			select {
			case d.frames <- buf:
			default:
				select { // Full: drop the oldest frame and retry
				case <-d.frames:
				default:
				}
				continue
			}
			break
		}
	}
}

// Write sends stream bytes to the decoder.
func (d *Decoder) Write(b []byte) (int, error) {
	select {
	case <-d.p.Done():
		return 0, errors.New("decoder exited")
	default:
	}
	return d.p.Stdin.Write(b)
}

// CloseInput ends the stream: the decoder outputs what is left, then exits.
func (d *Decoder) CloseInput() error { return d.p.Stdin.Close() }

// Frames returns the decoded frames; it is closed when the decoder exits.
func (d *Decoder) Frames() <-chan []byte { return d.frames }

// Width and Height are the size of decoded video frames (0 for audio).
func (d *Decoder) Width() int  { return d.w }
func (d *Decoder) Height() int { return d.h }

// Done is closed when the process has exited; Err then tells why.
func (d *Decoder) Done() <-chan struct{} { return d.p.Done() }
func (d *Decoder) Err() error            { return d.p.Err() }

// Stop kills the decoder and returns once it has exited.
func (d *Decoder) Stop() { d.p.Stop() }
