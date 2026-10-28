// Package sessiontest fakes the server side of a session for tests: a
// signaling client whose Connect offers a session from an in-process Pion
// peer (the "bridge"), and a sender of ffmpeg-encoded media on that bridge.
package sessiontest

import (
	"context"
	"sync"
	"testing"

	"github.com/pion/webrtc/v3"

	"github.com/daimoc/jitsi-media-tools/internal/jitsi"
	"github.com/daimoc/jitsi-media-tools/internal/signaling"
)

// Signal stands in for the Jitsi adapter. Set the exported fields before
// the session connects.
type Signal struct {
	T          testing.TB
	Occupants  []signaling.Occupant
	Sources    []signaling.RemoteSource
	Setup      func(bridge *webrtc.PeerConnection) // Adds the bridge's tracks before the offer
	ConnectErr error

	mu           sync.Mutex
	handler      signaling.SignalingHandler
	bridge       *webrtc.PeerConnection
	policies     []*signaling.ReceivePolicy
	stats        []signaling.EndpointStats
	disconnected bool
	config       map[string]any
}

// RecordConfig keeps the session's signaling configuration (see Config).
func (f *Signal) RecordConfig(c map[string]any) {
	f.mu.Lock()
	f.config = c
	f.mu.Unlock()
}

func (f *Signal) Connect(ctx context.Context, peerID string, h signaling.SignalingHandler) error {
	if f.ConnectErr != nil {
		return f.ConnectErr
	}
	bridge, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		return err
	}
	f.T.Cleanup(func() { bridge.Close() })
	if f.Setup != nil {
		f.Setup(bridge)
	}
	for _, k := range []webrtc.RTPCodecType{webrtc.RTPCodecTypeAudio, webrtc.RTPCodecTypeVideo} {
		if _, err := bridge.AddTransceiverFromKind(k, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly}); err != nil {
			return err
		}
	}
	offer, err := bridge.CreateOffer(nil)
	if err != nil {
		return err
	}
	gathered := webrtc.GatheringCompletePromise(bridge)
	if err := bridge.SetLocalDescription(offer); err != nil {
		return err
	}
	<-gathered
	if o, ok := h.(signaling.RoomObserver); ok {
		for _, occ := range f.Occupants {
			o.OnOccupant(occ)
		}
		o.OnRemoteSources(f.Sources)
	}
	answer, err := h.OnOffer(bridge.LocalDescription().SDP, func(string, string) {})
	if err != nil {
		return err
	}
	if err := bridge.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: answer}); err != nil {
		return err
	}
	f.mu.Lock()
	f.handler, f.bridge = h, bridge
	f.mu.Unlock()
	return nil
}

func (f *Signal) Disconnect(string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.disconnected = true
	return nil
}

func (f *Signal) SetReceivePolicy(_ string, p *signaling.ReceivePolicy) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.policies = append(f.policies, p)
}

func (f *Signal) SendEndpointStats(_ string, st signaling.EndpointStats) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stats = append(f.stats, st)
}

// Stats are the connection statistics the session sent, in order.
func (f *Signal) Stats() []signaling.EndpointStats {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]signaling.EndpointStats(nil), f.stats...)
}

// LastPolicy is the last receive policy the session asked for.
func (f *Signal) LastPolicy() *signaling.ReceivePolicy {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.policies) == 0 {
		return nil
	}
	return f.policies[len(f.policies)-1]
}

// Config is the signaling configuration the session built.
func (f *Signal) Config() map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.config
}

// Disconnected reports whether the session disconnected.
func (f *Signal) Disconnected() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.disconnected
}

// Handler is the session's signaling handler, once connected.
func (f *Signal) Handler() signaling.SignalingHandler {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.handler
}

// Room is the handler's room observer, once connected; tests drive joins,
// leaves and speakers through it.
func (f *Signal) Room() signaling.RoomObserver {
	o, _ := f.Handler().(signaling.RoomObserver)
	return o
}

// FetchMeeting answers for any meeting URL as a server at meet.test would
// (room "notjitsi" is not a Jitsi server).
func FetchMeeting(_ context.Context, u string, _ bool) (jitsi.Meeting, error) {
	_, m, err := jitsi.ParseMeetingURL(u)
	if err != nil {
		return m, err
	}
	if m.Room == "notjitsi" {
		return m, jitsi.ErrNotJitsi
	}
	m.Domain, m.MUC, m.WebSocket = "meet.test", "muc.meet.test", "wss://meet.test/xmpp-websocket"
	return m, nil
}
