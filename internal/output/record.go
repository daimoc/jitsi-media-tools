package output

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/daimoc/jitsi-media-tools/internal/ffmpeg"
)

// RecordNeeds are the ffmpeg parts recordings use.
var RecordNeeds = []string{ffmpeg.NeedH264Encoder, ffmpeg.NeedAACEncoder}

// MixedName is the name part of the mixed recording's file.
const MixedName = "mixed"

// Recording queue depths: a file has no viewer waiting, so a second of
// video and audio may queue while the encoder catches up before the
// oldest is dropped.
const (
	recordVideoQueue = 30
	recordAudioQueue = 50
)

// RecordConfig describes a recording.
type RecordConfig struct {
	FFmpeg        string
	Name          string // For logs: the participant, or MixedName
	Path          string // RecordPath
	Width, Height int
	Logger        *slog.Logger
}

// Recording encodes a tile stream or the grid (constant-rate video and
// audio) as H.264 and AAC into a Matroska file (research R11). Clusters
// are flushed every second, so a file cut short by a killed tool still
// plays up to then; Stop finishes it properly.
type Recording struct {
	p      *ffmpeg.Process
	queue  *stampedQueue
	cancel context.CancelFunc
	wrote  chan struct{} // Closed when the writer has ended
	once   sync.Once
}

// RecordPath is the file of name's recording in dir:
// <room>-<YYYYMMDD-HHMMSS>-<name>.mkv, started being the session's start.
// Path separators and characters Windows refuses become "_".
func RecordPath(dir, room string, started time.Time, name string) string {
	return filepath.Join(dir, fmt.Sprintf("%s-%s-%s.mkv", fileName(room, "room"), started.Format("20060102-150405"), fileName(name, "participant")))
}

// fileName makes s safe as part of a file name, or returns empty when s
// has nothing left.
func fileName(s, empty string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || strings.ContainsRune(`/\:*?"<>|`, r) {
			return '_'
		}
		return r
	}, s)
	if s = strings.TrimSpace(s); s == "" {
		return empty
	}
	return s
}

// RecordArgs returns the arguments of ffmpeg reading a w×h NUT tile stream
// on stdin and recording it into path.
func RecordArgs(path string, w, h int) []string {
	kbps := fmt.Sprintf("%dk", feedKbps(w, h))
	return []string{
		"-hide_banner", "-loglevel", "error", "-y",
		"-probesize", "32", "-analyzeduration", "0", "-f", "nut", "-i", "pipe:0",
		"-c:v", "libx264", "-preset", "veryfast", "-g", "60", "-pix_fmt", "yuv420p",
		"-b:v", kbps, "-maxrate", kbps, "-bufsize", kbps,
		"-c:a", "aac", "-b:a", "128k",
		"-flush_packets", "1", "-cluster_time_limit", "1000", "-f", "matroska", path,
	}
}

// StartRecording starts recording; write the stream's frames and chunks as
// they are made, and Stop to finish the file. The recording outlives ctx's
// cancellation until Stop, so that a session ending finishes its files.
func StartRecording(ctx context.Context, c RecordConfig) (*Recording, error) {
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	// Fail now on a directory that cannot be written, not in ffmpeg later.
	if err := os.MkdirAll(filepath.Dir(c.Path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.Create(c.Path)
	if err != nil {
		return nil, err
	}
	f.Close()
	ctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	p, err := ffmpeg.Start(ctx, ffmpeg.Spec{Bin: c.FFmpeg, Args: RecordArgs(c.Path, c.Width, c.Height),
		Name: "recording " + c.Name, Stdin: true, Logger: c.Logger})
	if err != nil {
		cancel()
		return nil, err
	}
	r := &Recording{p: p, queue: newStampedQueue(recordVideoQueue, recordAudioQueue), cancel: cancel, wrote: make(chan struct{})}
	go func() {
		defer close(r.wrote)
		defer p.Stdin.Close() // ffmpeg then finishes the file
		nut := newNUTWriter(p.Stdin, c.Width, c.Height)
		for {
			pkt, ok := r.queue.pop(ctx)
			if !ok {
				return
			}
			if err := pkt.writeTo(nut); err != nil {
				c.Logger.Warn("Recording stopped", slog.String("file", c.Path), slog.String("error", err.Error()))
				return
			}
		}
	}()
	return r, nil
}

// WriteVideo queues a frame (Width×Height yuv420p) made at made; the recording
// keeps b.
func (r *Recording) WriteVideo(b []byte, made time.Time) { r.queue.pushVideo(b, made) }

// WriteAudio queues a 20 ms chunk (s16le 48 kHz stereo) made at made; the
// recording keeps b.
func (r *Recording) WriteAudio(b []byte, made time.Time) { r.queue.pushAudio(b, made) }

// Stop writes what is queued, finishes the file and returns once ffmpeg
// has exited.
func (r *Recording) Stop() {
	r.once.Do(func() {
		r.queue.close()
		select {
		case <-r.wrote:
		case <-time.After(2 * time.Second): // ffmpeg stuck: Stop closes its input
		}
		r.p.Stop()
		r.cancel()
	})
}
