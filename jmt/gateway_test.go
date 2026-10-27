package jmt

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v3"

	"github.com/daimoc/jitsi-media-tools/internal/peer"
)

func startGateway(t *testing.T) (*Gateway, string) {
	t.Helper()
	g, err := NewGateway(GatewayOptions{Listen: "127.0.0.1:0", Secret: "s3cret", UDPPort: -1, Logger: quiet})
	if err != nil {
		t.Fatal(err)
	}
	served := make(chan error, 1)
	go func() { served <- g.Serve(context.Background()) }()
	t.Cleanup(func() {
		g.Close()
		if err := <-served; err != nil {
			t.Errorf("Serve: %v", err)
		}
	})
	return g, "http://" + g.Addr()
}

// obsLike offers H.264 and Opus, as OBS does.
func obsLike(t *testing.T) (*webrtc.PeerConnection, *webrtc.TrackLocalStaticRTP, string) {
	t.Helper()
	h264 := webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000, SDPFmtpLine: "packetization-mode=1;profile-level-id=42e01f"}
	m := &webrtc.MediaEngine{}
	if err := m.RegisterCodec(webrtc.RTPCodecParameters{RTPCodecCapability: h264, PayloadType: 96}, webrtc.RTPCodecTypeVideo); err != nil {
		t.Fatal(err)
	}
	if err := m.RegisterCodec(webrtc.RTPCodecParameters{RTPCodecCapability: peer.OpusCodec, PayloadType: 111}, webrtc.RTPCodecTypeAudio); err != nil {
		t.Fatal(err)
	}
	ir := &interceptor.Registry{}
	if err := webrtc.RegisterDefaultInterceptors(m, ir); err != nil {
		t.Fatal(err)
	}
	pc, err := webrtc.NewAPI(webrtc.WithMediaEngine(m), webrtc.WithInterceptorRegistry(ir)).NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pc.Close() })
	video, err := webrtc.NewTrackLocalStaticRTP(h264, "video", "obs")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pc.AddTransceiverFromTrack(video, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionSendonly}); err != nil {
		t.Fatal(err)
	}
	offer, err := pc.CreateOffer(nil)
	if err != nil {
		t.Fatal(err)
	}
	gathered := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(offer); err != nil {
		t.Fatal(err)
	}
	<-gathered
	return pc, video, pc.LocalDescription().SDP
}

func request(t *testing.T, method, url, token, body string) *http.Response {
	t.Helper()
	r, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Content-Type", "application/sdp")
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func nextGatewayEvent(t *testing.T, g *Gateway) GatewayEvent {
	t.Helper()
	select {
	case e := <-g.Events():
		return e
	case <-time.After(10 * time.Second):
		t.Fatal("no gateway event within 10 s")
		return GatewayEvent{}
	}
}

func TestGateway_RequiresASecret(t *testing.T) {
	if _, err := NewGateway(GatewayOptions{Listen: "127.0.0.1:0", UDPPort: -1}); err == nil {
		t.Error("NewGateway without a secret succeeded")
	}
	if _, err := NewGateway(GatewayOptions{Listen: "127.0.0.1:0", UDPPort: -1, Secret: "s", TLSCert: "cert.pem"}); err == nil {
		t.Error("NewGateway with a certificate and no key succeeded")
	}
}

func TestGateway_StreamIntoAMeeting(t *testing.T) {
	f := fakeRoom(t, false)
	g, base := startGateway(t)
	pc, video, offer := obsLike(t)

	if resp := request(t, http.MethodPost, base+"/whip/meet.test/Room?name=OBS", "wrong", offer); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong token: %d, want 401", resp.StatusCode)
	}
	resp := request(t, http.MethodPost, base+"/whip/meet.test/Room?name=OBS", "s3cret", offer)
	answer, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST: %d %s", resp.StatusCode, answer)
	}
	if e := nextGatewayEvent(t, g); e.Kind != StreamStarted || e.Stream.DisplayName != "OBS" || e.Stream.MeetingURL != "https://meet.test/Room" {
		t.Fatalf("event %+v, want OBS started", e)
	}
	if st := g.Streams(); len(st) != 1 || st[0].DisplayName != "OBS" {
		t.Fatalf("Streams() = %+v", st)
	}
	if f.Handler() == nil {
		t.Fatal("the gateway did not join the meeting")
	}
	if err := pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: string(answer)}); err != nil {
		t.Fatal(err)
	}
	connected := make(chan struct{})
	pc.OnConnectionStateChange(func(s webrtc.PeerConnectionState) {
		if s == webrtc.PeerConnectionStateConnected {
			close(connected)
		}
	})
	go func() {
		for seq := uint16(0); pc.ConnectionState() != webrtc.PeerConnectionStateClosed; seq++ {
			video.WriteRTP(&rtp.Packet{Header: rtp.Header{Version: 2, SequenceNumber: seq}, Payload: []byte{0x65, 1}})
			time.Sleep(20 * time.Millisecond)
		}
	}()
	select {
	case <-connected:
	case <-time.After(10 * time.Second):
		t.Fatal("the publisher did not connect to the gateway")
	}

	loc := resp.Header.Get("Location")
	if resp := request(t, http.MethodDelete, base+loc, "s3cret", ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("DELETE: %d", resp.StatusCode)
	}
	if e := nextGatewayEvent(t, g); e.Kind != StreamEnded || e.Err != nil {
		t.Fatalf("event %+v, want ended", e)
	}
	if !f.Disconnected() {
		t.Error("the participant did not leave the meeting")
	}
}

func TestGateway_RefusedStreamIsAnEvent(t *testing.T) {
	fakeRoom(t, false)
	g, base := startGateway(t)
	resp := request(t, http.MethodPost, base+"/whip/meet.test/Room?name=OBS", "s3cret", "v=0\r\nnot an offer\r\n")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad offer: %d, want 400", resp.StatusCode)
	}
	if e := nextGatewayEvent(t, g); e.Kind != StreamFailed || e.Err == nil {
		t.Fatalf("event %+v, want failed", e)
	}
}
