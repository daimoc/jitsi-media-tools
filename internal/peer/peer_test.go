package peer

import (
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
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

// TestPeer_StreamIDIsUnique checks that two peers never share a stream ID:
// Jicofo rejects a second participant whose MSID is already in the room.
func TestPeer_StreamIDIsUnique(t *testing.T) {
	a := newTestPeer(t, KindAudio, KindVideo)
	b := newTestPeer(t, KindAudio, KindVideo)
	for _, kind := range []string{KindAudio, KindVideo} {
		if sa, sb := a.LocalTrack(kind).StreamID(), b.LocalTrack(kind).StreamID(); sa == sb {
			t.Errorf("%s: both peers use stream ID %q", kind, sa)
		}
	}
	if a.LocalTrack(KindAudio).StreamID() != a.LocalTrack(KindVideo).StreamID() {
		t.Error("one peer's audio and video are in different streams")
	}
}

// rawTrack is a remote video track that writes packets with a chosen
// payload type, as the bridge forwards a sender that switches codec.
type rawTrack struct {
	mu     sync.Mutex
	ssrc   uint32
	w      webrtc.TrackLocalWriter
	codecs []webrtc.RTPCodecParameters
}

func (r *rawTrack) Bind(c webrtc.TrackLocalContext) (webrtc.RTPCodecParameters, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ssrc, r.w, r.codecs = uint32(c.SSRC()), c.WriteStream(), c.CodecParameters()
	return r.codecs[0], nil
}
func (r *rawTrack) Unbind(webrtc.TrackLocalContext) error { return nil }
func (r *rawTrack) ID() string                            { return "video" }
func (r *rawTrack) RID() string                           { return "" }
func (r *rawTrack) StreamID() string                      { return "remote" }
func (r *rawTrack) Kind() webrtc.RTPCodecType             { return webrtc.RTPCodecTypeVideo }

// write sends a packet of the codec mime, if negotiated.
func (r *rawTrack) write(mime string, seq uint16) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.w == nil {
		return false
	}
	for _, c := range r.codecs {
		if strings.EqualFold(c.MimeType, mime) {
			r.w.WriteRTP(&rtp.Header{Version: 2, SSRC: r.ssrc, PayloadType: uint8(c.PayloadType), SequenceNumber: seq, Timestamp: uint32(seq) * 3000, Marker: true}, []byte{0x10, 0, 0, 0})
			return true
		}
	}
	return false
}

// TestPeer_TrackSurvivesCodecSwitch checks that a track whose first packets
// are AV1 (a browser before it reads the codec list) still fires OnTrack,
// and reports the new codec once the sender switches to VP8.
func TestPeer_TrackSurvivesCodecSwitch(t *testing.T) {
	p := newTestPeer(t)
	gotTrack := make(chan *RemoteTrack, 1)
	p.OnTrack(func(rt *RemoteTrack) { gotTrack <- rt })
	remote, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { remote.Close() })
	raw := &rawTrack{}
	if _, err := remote.AddTrack(raw); err != nil {
		t.Fatal(err)
	}
	connect(t, remote, p)
	stop := make(chan struct{})
	defer close(stop)
	var switched atomic.Bool
	go func() {
		for seq := uint16(0); ; seq++ {
			select {
			case <-stop:
				return
			case <-time.After(10 * time.Millisecond):
			}
			mime := webrtc.MimeTypeAV1
			if switched.Load() {
				mime = webrtc.MimeTypeVP8
			}
			raw.write(mime, seq)
		}
	}()
	var rt *RemoteTrack
	select {
	case rt = <-gotTrack:
	case <-time.After(10 * time.Second):
		t.Fatal("no track for a stream starting with AV1")
	}
	if rt.Codec() != webrtc.MimeTypeAV1 {
		t.Errorf("codec %s, want AV1", rt.Codec())
	}
	switched.Store(true)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := rt.ReadRTP(); err != nil {
			t.Fatal(err)
		}
		if rt.Codec() == webrtc.MimeTypeVP8 {
			return
		}
	}
	t.Error("codec switch to VP8 not seen")
}
