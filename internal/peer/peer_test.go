package peer

import (
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v3"
)

var quietLogger = slog.New(slog.NewTextHandler(io.Discard, nil))

func newTestPeer(t *testing.T, kinds ...string) *Peer {
	t.Helper()
	p, err := New(Config{Logger: quietLogger, Kinds: kinds})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { p.Close() })
	return p
}

// connect has remote offer to p (with full gathering, no trickle) and
// applies p's answer.
func connect(t *testing.T, remote *webrtc.PeerConnection, p *Peer) {
	t.Helper()
	offer, err := remote.CreateOffer(nil)
	if err != nil {
		t.Fatal(err)
	}
	gathered := webrtc.GatheringCompletePromise(remote)
	if err := remote.SetLocalDescription(offer); err != nil {
		t.Fatal(err)
	}
	<-gathered
	answer, err := p.OnOffer(remote.LocalDescription().SDP, func(string, string) {})
	if err != nil {
		t.Fatalf("OnOffer: %v", err)
	}
	if err := remote.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: answer}); err != nil {
		t.Fatal(err)
	}
}

// TestPeer_SendsAndReceives checks both directions: a remote VP8 track
// reaches OnTrack, and the peer's own audio track reaches the remote side.
func TestPeer_SendsAndReceives(t *testing.T) {
	p := newTestPeer(t, KindAudio)
	connectedCh := make(chan struct{}, 1)
	p.OnTransportState(func(s TransportState, err error) {
		if s == TransportConnected {
			connectedCh <- struct{}{}
		}
	})
	gotTrack := make(chan *RemoteTrack, 1)
	p.OnTrack(func(rt *RemoteTrack) { gotTrack <- rt })

	remote, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { remote.Close() })
	video, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8, ClockRate: 90000}, "video", "remote")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := remote.AddTrack(video); err != nil {
		t.Fatal(err)
	}
	if _, err := remote.AddTransceiverFromKind(webrtc.RTPCodecTypeAudio, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly}); err != nil {
		t.Fatal(err)
	}
	remoteAudio := make(chan struct{}, 1)
	remote.OnTrack(func(tr *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		if _, _, err := tr.ReadRTP(); err == nil {
			remoteAudio <- struct{}{}
		}
	})
	connect(t, remote, p)

	select {
	case <-connectedCh:
	case <-time.After(10 * time.Second):
		t.Fatal("transport not connected")
	}
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		for seq := uint16(0); ; seq++ {
			select {
			case <-stop:
				return
			case <-time.After(20 * time.Millisecond):
			}
			video.WriteRTP(&rtp.Packet{Header: rtp.Header{Version: 2, SequenceNumber: seq, Timestamp: uint32(seq) * 3000, Marker: true}, Payload: []byte{0x10, 0, 0, 0}})
			p.LocalTrack(KindAudio).WriteRTP(&rtp.Packet{Header: rtp.Header{Version: 2, SequenceNumber: seq, Timestamp: uint32(seq) * 960}, Payload: []byte{0xf8, 0xff, 0xfe}})
		}
	}()

	select {
	case rt := <-gotTrack:
		if rt.Kind != KindVideo || rt.MimeType != webrtc.MimeTypeVP8 {
			t.Errorf("got %s %s track, want video VP8", rt.Kind, rt.MimeType)
		}
		if _, err := rt.ReadRTP(); err != nil {
			t.Errorf("ReadRTP: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no remote track")
	}
	select {
	case <-remoteAudio:
	case <-time.After(10 * time.Second):
		t.Fatal("remote side received no audio")
	}
}
