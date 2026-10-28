package signaling

import (
	"context"
	"time"
)

// TrickleFunc delivers a local ICE candidate ("candidate:..." SDP attribute
// value, without the "a=" prefix) for the media section identified by mid to
// the remote side.
type TrickleFunc func(mid, candidate string)

// DataChannel is a data channel opened on the media session.
type DataChannel interface {
	// SendText sends a text message. It fails while the channel is not
	// open; the caller retries on its next OnOpen.
	SendText(msg string) error
}

// DataChannelHandler receives a data channel's events. Methods are called
// from the media stack's goroutines, one at a time per channel, and must not
// block.
type DataChannelHandler interface {
	OnOpen()               // Open, or open again
	OnMessage(text string) // Text message (binary messages are dropped)
	OnClose()
}

// SignalingHandler defines the callback interface for signaling events.
// It is implemented by the media side (execution.Peer) and driven by a
// PlatformAdapter, so that no platform-specific type crosses the boundary.
type SignalingHandler interface {
	// OnOffer applies a remote SDP offer and returns the SDP answer. Local
	// candidates gathered before the answer is returned are embedded in it;
	// later ones are passed to trickle.
	OnOffer(sdp string, trickle TrickleFunc) (string, error)
	// OnICECandidate applies a remote ICE candidate for the media section mid.
	OnICECandidate(mid, candidate string) error
	// OpenDataChannel opens a data channel on the negotiated session (after
	// OnOffer), e.g. the channel a platform's media server expects for its
	// control messages. Events go to h; the returned channel sends.
	OpenDataChannel(label, protocol string, h DataChannelHandler) (DataChannel, error)
	// LocalMediaKinds returns the media kinds ("audio", "video") the peer
	// sends, in a stable order. Adapters call it before joining so that they
	// can announce the sources (for example in presence). Empty for a
	// receive-only peer.
	LocalMediaKinds() []string
	// Renegotiate applies an updated remote SDP offer on an already
	// negotiated session (remote sources added or removed) and returns the
	// SDP answer. It does not trickle candidates or record negotiation
	// metrics. Adapters whose platform expects no reply may drop the answer.
	Renegotiate(sdp string) (string, error)
	// ReceiveLimit returns the maximum number of remote streams the peer
	// receives per media kind (0: no limit). Adapters subscribe to (or
	// offer) at most that many and replace an ended one by a waiting one.
	ReceiveLimit() int
	// RequestedVideoHeight returns the video height, in lines, the peer
	// wants to receive for each remote video stream. Adapters ask the media
	// server for it with their platform's mechanism; the server may send
	// less.
	RequestedVideoHeight() int
	// VideoLayerHeights returns the heights, low to high, of the simulcast
	// layers the scenario's peers send; nil without simulcast. Adapters
	// whose media server assumes other layer heights translate the
	// requested height to the server's.
	VideoLayerHeights() []int
	// OnSignalingLost reports that the signaling connection was lost after
	// Connect returned (server gone, kicked from the room, keepalive timeout).
	OnSignalingLost(err error)

	// The publish session, for platforms that receive the peer's media on a
	// session the peer offers, separate from the one the platform offers
	// for the media the peer receives (see PublishSessionAdapter). With it,
	// the session OnOffer answers carries no local track.

	// CreatePublishOffer creates the publish session's offer: one sendonly
	// section per local track. Candidates gathered before it returns are in
	// the SDP; later ones go to trickle. Fails if the peer was not built
	// with a publish session, or sends nothing.
	CreatePublishOffer(trickle TrickleFunc) (string, error)
	// ApplyPublishAnswer applies the remote answer to the publish offer.
	ApplyPublishAnswer(sdp string) error
	// OnPublishICECandidate applies a remote candidate for the publish
	// session. Candidates received before the answer are buffered.
	OnPublishICECandidate(mid, candidate string) error
	// LocalVideoSize returns the width and height of the video the peer
	// sends (0, 0 without video).
	LocalVideoSize() (width, height int)
}

// PublishSessionAdapter is implemented by adapters whose platform receives
// the peer's media on a session the peer offers, separate from the session
// the platform offers for the media the peer receives. The engine checks it
// once per scenario and builds its peers accordingly.
type PublishSessionAdapter interface {
	PublishSession() bool
}

// PlatformAdapter defines the interface for a platform-specific signaling client.
type PlatformAdapter interface {
	// Connect joins peerID to the platform and blocks until it is joined, the
	// join fails, or ctx is cancelled. On error no resources are left behind.
	Connect(ctx context.Context, peerID string, handler SignalingHandler) error
	// Disconnect tears down everything Connect created for peerID. It is safe
	// to call for unknown or already-disconnected peers.
	Disconnect(peerID string) error
}

// Occupant is another participant of the room, from its presence.
type Occupant struct {
	ID          string // Endpoint id (MUC nick)
	DisplayName string
	AudioMuted  bool
	VideoMuted  bool
	Left        bool // Unavailable presence: the occupant left
	// SourceMuted is the mute state of each source the occupant announces
	// (source name -> muted); nil when the presence has no SourceInfo. A
	// stopped screen share stays negotiated, muted here.
	SourceMuted map[string]bool
}

// RemoteSource is a stream another participant sends, as negotiated.
type RemoteSource struct {
	SSRC  uint32 // Primary SSRC
	Kind  string // "audio" or "video"
	Name  string // Source name, e.g. "abcd1234-v0" ("" if unnamed)
	Owner string // Endpoint id of the sender ("" if unknown)
	// Desktop marks a screen share (Jitsi videoType "desktop").
	Desktop bool
}

// RoomObserver may be implemented by a SignalingHandler to follow the room:
// participants, their sources and who is speaking. Calls come from
// signaling goroutines and must not block.
type RoomObserver interface {
	OnOccupant(o Occupant)
	// OnRemoteSources gives every remote source currently negotiated, after
	// each change.
	OnRemoteSources(sources []RemoteSource)
	// OnDominantSpeaker gives the current dominant speaker's endpoint id
	// and the previous speakers, most recent first.
	OnDominantSpeaker(id string, previous []string)
}

// ReceivePolicy is which video the media server should forward (research
// R8): the LastN most relevant sources (speakers), always including the
// Selected ones, at DefaultMaxHeight, or MaxHeights[source] when set.
type ReceivePolicy struct {
	LastN            int
	Selected         []string // Source names, always forwarded
	OnStage          []string // Source names shown large
	DefaultMaxHeight int
	MaxHeights       map[string]int // By source name
	// AssumedBandwidth, in bits/s, is offered to the bridge in place of its
	// bandwidth estimate; it uses it only where its operator allows (0: none).
	AssumedBandwidth int
}

// EndpointStats are a participant's connection statistics, as Jitsi
// clients report their own to the meeting (research R14).
type EndpointStats struct {
	UploadKbps, DownloadKbps         int
	AudioUploadKbps, VideoUploadKbps int
	UploadLoss, DownloadLoss         float64 // Percent of packets
	TotalLoss                        float64 // Percent, both directions
	Quality                          int     // Connection quality, 0 to 100
	RTT                              time.Duration
}

// HostWaiter may be implemented by a SignalingHandler to know when a guest
// waits for an authenticated host to start the conference.
type HostWaiter interface {
	OnWaitingForHost()
}
