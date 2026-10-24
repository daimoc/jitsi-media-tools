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
}

func newMedia(s *Session, t tools) *media {
	return &media{s: s, tools: t}
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

func (m *media) onTrack(t *peer.RemoteTrack) {}
func (m *media) onKeyframeRequest()          {}
func (m *media) onConnected() {
	if m.pub != nil {
		m.pub.start()
	}
}

func (m *media) participantLeft(p Participant) {}
func (m *media) speakersChanged()              {}
func (m *media) selectionChanged()             {}
func (m *media) stop()                         {}

// Address is something the session listens on.
type Address struct{ Label, URL string }

// Listening returns what the session listens on.
func (s *Session) Listening() []Address { return s.media.listening() }

func (m *media) listening() []Address { return nil }
