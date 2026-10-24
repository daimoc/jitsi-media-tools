package signaling

import "context"

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
