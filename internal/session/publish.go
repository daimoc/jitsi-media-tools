package session

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/daimoc/jitsi-media-tools/internal/ffmpeg"
	"github.com/daimoc/jitsi-media-tools/internal/peer"
	"github.com/daimoc/jitsi-media-tools/internal/rtpmedia"
)

// publishNeeds are the ffmpeg parts publishing uses.
var publishNeeds = []string{ffmpeg.NeedVP8Encoder, ffmpeg.NeedOpusEncoder, ffmpeg.NeedTCP}

// pumpQueue is the depth of the channels into the pumps: frames queued
// there show a pump that its source is behind, so it catches up.
const pumpQueue = 8

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
	if p.Input.Kind == "forward" {
		m.pub = &publisher{m: m, pub: p}
		return nil
	}
	if f := p.Input.Frames; p.Input.Kind == "frames" {
		p.Audio, p.Video = f.Audio, f.Video
		if f.Video {
			p.Width, p.Height = f.W, f.H
		}
	}
	if p.Input.Kind == "devices" {
		if err := m.openDevices(ctx, p); err != nil {
			return err
		}
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
		pb.video = make(chan rtpmedia.Frame, pumpQueue)
	}
	if p.Audio {
		pb.audio = make(chan rtpmedia.Frame, pumpQueue)
	}
	m.pub = pb
	return nil
}

// listDevices lists the capture devices; tests replace it.
var listDevices = ffmpeg.ListDevices

// openDevices looks up the named devices and builds the input capturing
// them: an unknown name fails before joining, listing the devices there
// are (FR-008).
func (m *media) openDevices(ctx context.Context, p *Publish) error {
	devs, err := listDevices(ctx, m.tools.ffmpeg)
	if err != nil {
		return fmt.Errorf("listing devices: %w", err)
	}
	var video, audio *ffmpeg.Device
	for _, want := range []struct {
		kind, name string
		dev        **ffmpeg.Device
	}{{"video", p.Input.Video, &video}, {"audio", p.Input.Audio, &audio}} {
		if want.name == "" {
			continue
		}
		d, err := ffmpeg.FindDevice(devs, want.kind, want.name)
		if err != nil {
			return err
		}
		*want.dev = &d
	}
	args, _, _ := ffmpeg.DeviceInput(ctx, m.tools.ffmpeg, video, audio, p.Width, p.Height)
	if err := m.tools.caps.Missing(ffmpeg.InputFormats(args)...); err != nil {
		return err
	}
	p.Input.Args = args
	p.Audio, p.Video = audio != nil, video != nil
	return nil
}

// start begins sending once media is connected; later calls do nothing.
func (pb *publisher) start() {
	pb.once.Do(func() {
		s := pb.m.s
		if pb.pub.Input.Kind == "forward" {
			tracks := map[string]peer.RTPWriter{}
			for _, kind := range pb.m.publishKinds() {
				tracks[kind] = s.peer.LocalTrack(kind)
			}
			pb.pub.Input.Forwarded.connect(tracks)
			return
		}
		if pb.video != nil {
			pb.pump(peer.KindVideo, rtpmedia.NewVideoSender(s.peer.LocalTrack(peer.KindVideo)), pb.video, rtpmedia.VideoPlaceholder(pb.black))
		}
		if pb.audio != nil {
			pb.pump(peer.KindAudio, rtpmedia.NewAudioSender(s.peer.LocalTrack(peer.KindAudio)), pb.audio, rtpmedia.AudioPlaceholder())
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			switch {
			case pb.pub.Input.Kind == "file" && !pb.pub.Input.Loop:
				pb.runOnce()
			case pb.pub.Input.Kind == "frames":
				pb.runFrames()
			default:
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

// runFrames encodes what the program writes, restarting the encoder after
// a failure, until the session or the frame source closes. Placeholders go
// out meanwhile and afterwards.
func (pb *publisher) runFrames() {
	s, f := pb.m.s, pb.pub.Input.Frames
	for {
		p, err := pb.startEncoder(s.ctx)
		if err == nil {
			<-p.Done()
			err = p.Err()
		}
		if s.ctx.Err() != nil || f.closed() {
			return
		}
		if err != nil {
			s.logger.Warn("Encoder stopped; sending a placeholder until it restarts", slog.String("error", err.Error()))
		}
		select {
		case <-s.ctx.Done():
			return
		case <-f.done:
			return
		case <-time.After(restartDelay):
		}
	}
}

// startEncoder starts one encoder run and forwards its frames until its
// outputs end. The returned process is done once they have.
func (pb *publisher) startEncoder(ctx context.Context) (*ffmpeg.Process, error) {
	s := pb.m.s
	cfg := ffmpeg.EncoderConfig{
		Bin: pb.m.tools.ffmpeg, Input: pb.pub.Input.Args, Logger: s.logger,
		Options: ffmpeg.EncodeOptions{Audio: pb.pub.Audio, Video: pb.pub.Video, Width: pb.pub.Width, Height: pb.pub.Height},
	}
	var frames *framesRun
	if pb.pub.Input.Kind == "frames" {
		var err error
		if frames, err = openFrames(pb.pub.Input.Frames); err != nil {
			return nil, err
		}
		cfg.Input = frames.args
		cfg.Options.ConstantRate = true // Raw frames at -r PublishFPS
		if frames.stdin != nil {
			cfg.Stdin = frames.stdin
		}
	}
	enc, err := ffmpeg.StartEncoder(ctx, cfg)
	if err != nil {
		if frames != nil {
			frames.close()
		}
		return nil, err
	}
	if frames != nil {
		// The feeders stop with this run, so that the next one gets the
		// frames that follow.
		runCtx, cancel := context.WithCancel(ctx)
		go func() {
			<-enc.Done()
			cancel()
		}()
		frames.feed(runCtx, &s.wg)
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
