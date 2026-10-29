package output

import (
	"context"
	"log/slog"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/daimoc/jitsi-media-tools/internal/ffmpeg"
)

// Feed queue depths: as the display's, a feed whose ffmpeg waits for a
// caller or falls behind loses the oldest frames instead of delaying.
const (
	feedVideoQueue = displayVideoQueue
	feedAudioQueue = displayAudioQueue
)

// A gap longer than feedStall between queued packets is time the feed
// waited for its caller. A stream starts at feedLead, so that the other
// kind's packets made just before its first one stay positive.
const (
	feedStall = 500 * time.Millisecond
	feedLead  = 100 * time.Millisecond
)

// FeedConfig describes an SRT feed for OBS.
type FeedConfig struct {
	FFmpeg        string
	Name          string // For logs: the participant, or "grid"
	ListenURL     string // srt://host:port?mode=listener… (Ports.ListenURL)
	Width, Height int
	Logger        *slog.Logger
}

// Feed serves a tile stream (constant-rate video and audio) as H.264 and
// AAC in MPEG-TS over an SRT listener that OBS Media Sources call (research
// R11). ffmpeg waits for a caller before encoding, so a feed nobody watches
// costs no encoding; when the caller leaves, ffmpeg ends and is restarted
// for the next one.
type Feed struct {
	c      FeedConfig
	queue  *stampedQueue
	cancel context.CancelFunc
	done   chan struct{}
	once   sync.Once
}

// feedKbps is the video bitrate of a w×h feed.
func feedKbps(w, h int) int {
	if h >= 720 {
		return 3000
	}
	return 1000
}

// FeedArgs returns the arguments of ffmpeg reading a w×h NUT tile stream
// on stdin and serving it at listenURL.
func FeedArgs(listenURL string, w, h int) []string {
	kbps := strconv.Itoa(feedKbps(w, h)) + "k"
	return []string{
		"-hide_banner", "-loglevel", "error",
		"-probesize", "32", "-analyzeduration", "0", "-f", "nut", "-i", "pipe:0",
		"-c:v", "libx264", "-preset", "veryfast", "-tune", "zerolatency", "-g", "60", "-pix_fmt", "yuv420p",
		"-b:v", kbps, "-maxrate", kbps, "-bufsize", kbps,
		"-c:a", "aac", "-b:a", "128k",
		"-flush_packets", "1", "-f", "mpegts", listenURL,
	}
}

// StartFeed starts serving; write the tile stream's frames and chunks as
// they are made.
func StartFeed(ctx context.Context, c FeedConfig) (*Feed, error) {
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	ctx, cancel := context.WithCancel(ctx)
	f := &Feed{c: c, queue: newStampedQueue(feedVideoQueue, feedAudioQueue), cancel: cancel, done: make(chan struct{})}
	logger := c.Logger.With(slog.String("feed", c.Name))
	go func() {
		defer close(f.done)
		ffmpeg.Supervise(ctx, logger, "feed", f.run, func(err error) {
			logger.Debug("Feed caller gone; listening again", slog.Any("reason", err))
		})
	}()
	return f, nil
}

// run starts one ffmpeg and writes the queue into it until it ends.
func (f *Feed) run(ctx context.Context) (*ffmpeg.Process, error) {
	r, w, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	p, err := ffmpeg.Start(ctx, ffmpeg.Spec{Bin: f.c.FFmpeg, Args: FeedArgs(f.c.ListenURL, f.c.Width, f.c.Height),
		Name: "feed " + f.c.Name, StdinFrom: r, NoGrace: true, Logger: f.c.Logger})
	r.Close() // ffmpeg holds its own copy
	if err != nil {
		w.Close()
		return nil, err
	}
	runCtx, cancel := context.WithCancel(ctx)
	go func() {
		<-p.Done()
		cancel()
	}()
	go func() {
		defer w.Close()
		nut := newNUTWriter(w, f.c.Width, f.c.Height)
		// Each ffmpeg's stream starts near 0, and the time the queue waited
		// for a caller is cut out: a caller who comes late gets the stream
		// as it is now, not one old frame and then a jump that players show
		// as a frozen picture.
		var shift, last time.Duration
		for first := true; ; first = false {
			pkt, ok := f.queue.pop(runCtx)
			if !ok {
				return
			}
			switch {
			case first:
				shift, last = pkt.at-feedLead, pkt.at
			case pkt.at-last > feedStall:
				shift += pkt.at - last - streamFrame
			}
			last = max(last, pkt.at)
			pkt.at -= shift
			if pkt.writeTo(nut) != nil {
				return // A write to an ended ffmpeg fails
			}
		}
	}()
	return p, nil
}

// WriteVideo queues a tile frame (Width×Height yuv420p) made at made; the feed
// keeps b.
func (f *Feed) WriteVideo(b []byte, made time.Time) { f.queue.pushVideo(b, made) }

// WriteAudio queues a 20 ms chunk (s16le 48 kHz stereo) made at made; the feed
// keeps b.
func (f *Feed) WriteAudio(b []byte, made time.Time) { f.queue.pushAudio(b, made) }

// Stop ends the feed; it returns once its ffmpeg has exited.
func (f *Feed) Stop() {
	f.once.Do(func() {
		f.queue.close()
		f.cancel()
		<-f.done
	})
}
