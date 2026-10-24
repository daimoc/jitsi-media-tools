package session

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/daimoc/jitsi-media-tools/internal/ffmpeg"
	"github.com/daimoc/jitsi-media-tools/internal/peer"
	"github.com/daimoc/jitsi-media-tools/internal/rtpmedia"
)

// publishNeeds are the ffmpeg parts publishing uses.
var publishNeeds = []string{ffmpeg.NeedVP8Encoder, ffmpeg.NeedOpusEncoder, ffmpeg.NeedTCP}

// publisher runs the encoder of the publish source and sends its frames on
// the peer's tracks (research R5). Each kind has one long-lived frame
// channel fed by successive encoder runs, so that its pump sends one
// continuous stream with placeholders in the gaps (FR-010).
type publisher struct {
	m     *media
	pub   *Publish
	black []byte // Video placeholder (black VP8 keyframe)
	video chan rtpmedia.Frame
	audio chan rtpmedia.Frame
	once  sync.Once
}

// preparePublish checks the source and encodes the placeholder before
// joining: an unreadable file fails here (FR-022).
func (m *media) preparePublish(ctx context.Context, p *Publish) error {
	if p.Width == 0 || p.Height == 0 {
		p.Width, p.Height = 1280, 720
	}
	if err := p.Input.check(); err != nil {
		return err
	}
	if p.Input.Kind == "file" {
		st, err := ffmpeg.FileStreams(ctx, m.tools.ffmpeg, p.Input.Path)
		if err != nil {
			return fmt.Errorf("media file: %w", err)
		}
		if p.Audio && !st.Audio || p.Video && !st.Video {
			m.s.logger.Info("Publishing only the kinds the file has", slog.Bool("audio", p.Audio && st.Audio), slog.Bool("video", p.Video && st.Video))
		}
		p.Audio, p.Video = p.Audio && st.Audio, p.Video && st.Video
	}
	if !p.Audio && !p.Video {
		return fmt.Errorf("nothing to publish")
	}
	pb := &publisher{m: m, pub: p}
	if p.Video {
		black, err := ffmpeg.EncodeBlackFrame(ctx, m.tools.ffmpeg, p.Width, p.Height)
		if err != nil {
			return fmt.Errorf("video placeholder: %w", err)
		}
		pb.black = black
		pb.video = make(chan rtpmedia.Frame)
	}
	if p.Audio {
		pb.audio = make(chan rtpmedia.Frame)
	}
	m.pub = pb
	return nil
}

// start begins sending once media is connected; later calls do nothing.
func (pb *publisher) start() {
	pb.once.Do(func() {
		s := pb.m.s
		if pb.video != nil {
			pb.pump(peer.KindVideo, rtpmedia.NewVideoSender(s.peer.LocalTrack(peer.KindVideo)), pb.video, rtpmedia.VideoPlaceholder(pb.black))
		}
		if pb.audio != nil {
			pb.pump(peer.KindAudio, rtpmedia.NewAudioSender(s.peer.LocalTrack(peer.KindAudio)), pb.audio, rtpmedia.AudioPlaceholder())
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			if pb.pub.Input.Kind == "file" && !pb.pub.Input.Loop {
				pb.runOnce()
			} else {
				ffmpeg.Supervise(s.ctx, s.logger, "encoder", pb.startEncoder, func(err error) {
					if err != nil {
						s.logger.Warn("Encoder stopped; sending a placeholder until it restarts", slog.String("error", err.Error()))
					}
				})
			}
		}()
	})
}

func (pb *publisher) pump(kind string, snd *rtpmedia.Sender, in <-chan rtpmedia.Frame, ph *rtpmedia.Placeholder) {
	s := pb.m.s
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		if err := rtpmedia.Pump(s.ctx, snd, in, ph); err != nil && s.ctx.Err() == nil {
			s.logger.Warn("Sending stopped", slog.String("kind", kind), slog.String("error", err.Error()))
		}
	}()
}

// runOnce plays the file once, then ends the session (closed after a clean
// end, failed after an encoder error).
func (pb *publisher) runOnce() {
	s := pb.m.s
	p, err := pb.startEncoder(s.ctx)
	if err == nil {
		<-p.Done()
		err = p.Err()
	}
	if s.ctx.Err() != nil {
		return // Closing anyway
	}
	// Close and fail wait for this goroutine: call them from another one.
	if err != nil {
		go s.fail(fmt.Errorf("publishing %s: %w", pb.pub.Input.Path, err))
		return
	}
	s.logger.Info("End of the media file; leaving")
	go s.Close()
}

// startEncoder starts one encoder run and forwards its frames until its
// outputs end. The returned process is done once they have.
func (pb *publisher) startEncoder(ctx context.Context) (*ffmpeg.Process, error) {
	s := pb.m.s
	enc, err := ffmpeg.StartEncoder(ctx, ffmpeg.EncoderConfig{
		Bin: pb.m.tools.ffmpeg, Input: pb.pub.Input.Args, Logger: s.logger,
		Options: ffmpeg.EncodeOptions{Audio: pb.pub.Audio, Video: pb.pub.Video, Width: pb.pub.Width, Height: pb.pub.Height},
	})
	if err != nil {
		return nil, err
	}
	var fwd sync.WaitGroup
	forward := func(from <-chan rtpmedia.Frame, to chan<- rtpmedia.Frame) {
		defer fwd.Done()
		for f := range from {
			select {
			case to <- f:
			case <-ctx.Done():
				enc.Stop()
				return
			}
		}
	}
	if pb.video != nil {
		fwd.Add(1)
		go forward(enc.Video(), pb.video)
	}
	if pb.audio != nil {
		fwd.Add(1)
		go forward(enc.Audio(), pb.audio)
	}
	fwd.Wait() // Outputs ended: the process has exited or is exiting
	enc.Stop()
	return enc.Process(), enc.Err()
}
