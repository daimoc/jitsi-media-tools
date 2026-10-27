package session

import (
	"sync"

	"github.com/daimoc/jitsi-media-tools/internal/peer"
)

// Forwarder is an encoded source the session sends as it comes: a WHIP
// publisher (peer.Ingest).
type Forwarder interface {
	Forward(kind string, w peer.RTPWriter)
	RequestKeyframe()
}

// Forwarded is the source of a forward input. The source may be attached
// after the session joined (a gateway answers its publisher only then);
// packets flow once both the source and the session's media are there.
type Forwarded struct {
	mu     sync.Mutex
	src    Forwarder
	tracks map[string]peer.RTPWriter // By kind, once media connected
}

// ForwardInput sends f's packets unchanged: H.264 video and Opus audio.
func ForwardInput(f *Forwarded) Input { return Input{Kind: "forward", Forwarded: f} }

// Attach sets the source.
func (f *Forwarded) Attach(src Forwarder) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.src = src
	f.wire()
}

// connect gives the session's send tracks once media is connected.
func (f *Forwarded) connect(tracks map[string]peer.RTPWriter) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tracks = tracks
	f.wire()
}

// wire starts forwarding, from a keyframe, once both ends are known.
func (f *Forwarded) wire() {
	if f.src == nil || f.tracks == nil {
		return
	}
	for kind, w := range f.tracks {
		f.src.Forward(kind, w)
	}
	f.src.RequestKeyframe()
}

func (f *Forwarded) requestKeyframe() {
	f.mu.Lock()
	src := f.src
	f.mu.Unlock()
	if src != nil {
		src.RequestKeyframe()
	}
}

// encodes reports whether publishing runs ffmpeg.
func (c *Config) encodes() bool {
	return c.Publish != nil && c.Publish.Input.Kind != "forward"
}
