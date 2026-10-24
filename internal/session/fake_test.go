package session

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"

	"github.com/pion/webrtc/v3"

	"github.com/daimoc/jitsi-media-tools/internal/jitsi"
	"github.com/daimoc/jitsi-media-tools/internal/signaling"
)

var quietLogger = slog.New(slog.NewTextHandler(io.Discard, nil))

// fakeSignal stands in for the Jitsi adapter: Connect offers a session from
// an in-process Pion peer (the "bridge") and reports the room's occupants.
type fakeSignal struct {
	t          *testing.T
	occupants  []signaling.Occupant
	sources    []signaling.RemoteSource
	setup      func(bridge *webrtc.PeerConnection) // Adds the bridge's tracks before the offer
	connectErr error

	mu           sync.Mutex
	handler      signaling.SignalingHandler
	bridge       *webrtc.PeerConnection
	policies     []*signaling.ReceivePolicy
	disconnected bool
	config       map[string]any
}

func (f *fakeSignal) Connect(ctx context.Context, peerID string, h signaling.SignalingHandler) error {
	if f.connectErr != nil {
		return f.connectErr
	}
	bridge, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		return err
	}
	f.t.Cleanup(func() { bridge.Close() })
	if f.setup != nil {
		f.setup(bridge)
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
		for _, occ := range f.occupants {
			o.OnOccupant(occ)
		}
		o.OnRemoteSources(f.sources)
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

func (f *fakeSignal) Disconnect(string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.disconnected = true
	return nil
}

func (f *fakeSignal) SetReceivePolicy(_ string, p *signaling.ReceivePolicy) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.policies = append(f.policies, p)
}

func (f *fakeSignal) lastPolicy() *signaling.ReceivePolicy {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.policies) == 0 {
		return nil
	}
	return f.policies[len(f.policies)-1]
}

// useFake installs f as the signaling of sessions started by the test, with
// a config.js answer for any meeting URL.
func useFake(t *testing.T, f *fakeSignal) {
	t.Helper()
	f.t = t
	oldSignal, oldFetch := newSignal, fetchMeeting
	newSignal = func(c map[string]any, _ *slog.Logger) (signalClient, error) {
		f.mu.Lock()
		f.config = c
		f.mu.Unlock()
		return f, nil
	}
	fetchMeeting = func(_ context.Context, u string, _ bool) (jitsi.Meeting, error) {
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
	t.Cleanup(func() { newSignal, fetchMeeting = oldSignal, oldFetch })
}

var errFake = errors.New("fake failure")
