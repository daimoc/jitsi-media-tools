package session

import (
	"context"

	"github.com/daimoc/jitsi-media-tools/internal/ffmpeg"
	"github.com/daimoc/jitsi-media-tools/internal/peer"
)

// tools are the media executables found by checkTools.
type tools struct {
	ffmpeg, ffplay string
	caps           *ffmpeg.Caps
}

// Publish is the session's publish source (data-model "PublishSource").
type Publish struct {
	Input         Input // What ffmpeg reads
	Audio, Video  bool  // Kinds published
	Width, Height int   // Published video size; default 1280x720
}

// media holds the session's publish and receive pipelines.
type media struct {
	s     *Session
	tools tools
	pub   *publisher // Nil when publishing nothing
	recv  *receiver  // Nil when nothing consumes received media
}

func newMedia(s *Session, t tools) *media {
	m := &media{s: s, tools: t}
	if s.cfg.receives() {
		m.recv = newReceiver(m)
	}
	return m
}

// joined starts the receive outputs once in the room.
func (m *media) joined() error {
	if m.recv != nil {
		return m.recv.start()
	}
	return nil
}

// prepare checks the publish source before joining.
func (m *media) prepare(ctx context.Context) error {
	if p := m.s.cfg.Publish; p != nil {
		return m.preparePublish(ctx, p)
	}
	return nil
}

func (m *media) publishKinds() []string {
	p := m.s.cfg.Publish
	if p == nil {
		return nil
	}
	var kinds []string
	if p.Audio {
		kinds = append(kinds, peer.KindAudio)
	}
	if p.Video {
		kinds = append(kinds, peer.KindVideo)
	}
	return kinds
}

// publishCodec is the sent video codec: H.264 forwarded as it came, or
// VP8 from the encoder ("").
func (m *media) publishCodec() string {
	if p := m.s.cfg.Publish; p != nil && p.Input.Kind == "forward" {
		return peer.H264Codec.MimeType
	}
	return ""
}

func (m *media) publishWidth() int {
	if p := m.s.cfg.Publish; p != nil {
		return p.Width
	}
	return 0
}

func (m *media) publishHeight() int {
	if p := m.s.cfg.Publish; p != nil {
		return p.Height
	}
	return 0
}

func (m *media) onTrack(t *peer.RemoteTrack) {
	s := m.s
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		if m.recv == nil {
			drain(t)
			return
		}
		m.recv.track(t)
	}()
}
func (m *media) onKeyframeRequest() {
	if p := m.s.cfg.Publish; p != nil && p.Input.Kind == "forward" {
		p.Input.Forwarded.requestKeyframe()
	}
}
func (m *media) onConnected() {
	if m.pub != nil {
		m.pub.start()
	}
}

// participantsChanged follows joins, leaves, renames, pins and screen shares.
func (m *media) participantsChanged() {
	if m.recv != nil {
		m.recv.sync()
	}
}

func (m *media) speakersChanged() { m.participantsChanged() }

func (m *media) selectionChanged() {
	if m.recv != nil {
		m.recv.setLimit(m.s.cfg.Selection.Limit)
		m.recv.sync()
	}
}

func (m *media) stop() {
	if m.recv != nil {
		m.recv.stop()
	}
}

// Address is something the session listens on.
type Address struct{ Label, URL string }

// Listening returns what the session listens on.
func (s *Session) Listening() []Address { return s.media.listening() }

func (m *media) listening() []Address { return nil }
