package session

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v3"

	"github.com/daimoc/jitsi-media-tools/internal/peer"
	"github.com/daimoc/jitsi-media-tools/internal/signaling"
	"github.com/daimoc/jitsi-media-tools/internal/whip"
)

// whipPublisher is a WHIP publisher like OBS: H.264 High profile and Opus.
type whipPublisher struct {
	pc           *webrtc.PeerConnection
	video, audio *webrtc.TrackLocalStaticRTP
	videoSender  *webrtc.RTPSender
	plis         atomic.Int32
}

func newWHIPPublisher(t *testing.T, videoCodec webrtc.RTPCodecCapability) *whipPublisher {
	t.Helper()
	m := &webrtc.MediaEngine{}
	if err := m.RegisterCodec(webrtc.RTPCodecParameters{RTPCodecCapability: videoCodec, PayloadType: 106}, webrtc.RTPCodecTypeVideo); err != nil {
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
	p := &whipPublisher{pc: pc}
	if p.video, err = webrtc.NewTrackLocalStaticRTP(videoCodec, "video", "obs"); err != nil {
		t.Fatal(err)
	}
	if p.audio, err = webrtc.NewTrackLocalStaticRTP(peer.OpusCodec, "audio", "obs"); err != nil {
		t.Fatal(err)
	}
	vt, err := pc.AddTransceiverFromTrack(p.video, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionSendonly})
	if err != nil {
		t.Fatal(err)
	}
	p.videoSender = vt.Sender()
	if _, err := pc.AddTransceiverFromTrack(p.audio, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionSendonly}); err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			pkts, _, err := p.videoSender.ReadRTCP()
			if err != nil {
				return
			}
			for _, k := range pkts {
				if _, ok := k.(*rtcp.PictureLossIndication); ok {
					p.plis.Add(1)
				}
			}
		}
	}()
	return p
}

func (p *whipPublisher) offer(t *testing.T) string {
	t.Helper()
	o, err := p.pc.CreateOffer(nil)
	if err != nil {
		t.Fatal(err)
	}
	gathered := webrtc.GatheringCompletePromise(p.pc)
	if err := p.pc.SetLocalDescription(o); err != nil {
		t.Fatal(err)
	}
	<-gathered
	return p.pc.LocalDescription().SDP
}

// send writes packets on both tracks until stop closes.
func (p *whipPublisher) send(stop <-chan struct{}) {
	for seq := uint16(0); ; seq++ {
		select {
		case <-stop:
			return
		case <-time.After(20 * time.Millisecond):
		}
		p.video.WriteRTP(&rtp.Packet{Header: rtp.Header{Version: 2, Marker: true, SequenceNumber: seq, Timestamp: uint32(seq) * 1800}, Payload: []byte{0x65, 0x88, 1}})
		p.audio.WriteRTP(&rtp.Packet{Header: rtp.Header{Version: 2, SequenceNumber: seq, Timestamp: uint32(seq) * 960}, Payload: []byte{0xfc, 1}})
	}
}

var h264High = webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000,
	SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=64001f"}

func newTestGateway(idle time.Duration) *Gateway {
	return NewGateway(GatewayConfig{Session: Config{Logger: quietLogger}, IdleTimeout: idle})
}

func TestGateway_ForwardsToTheMeeting(t *testing.T) {
	var audio atomic.Int32
	videoCodec := make(chan string, 1)
	f := &fakeSignal{Setup: func(b *webrtc.PeerConnection) {
		b.OnTrack(func(tr *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
			if tr.Kind() == webrtc.RTPCodecTypeVideo {
				videoCodec <- tr.Codec().MimeType
				// Ask for a keyframe, as a viewer joining would.
				b.WriteRTCP([]rtcp.Packet{&rtcp.PictureLossIndication{MediaSSRC: uint32(tr.SSRC())}})
			}
			for {
				if _, _, err := tr.ReadRTP(); err != nil {
					return
				}
				if tr.Kind() == webrtc.RTPCodecTypeAudio {
					audio.Add(1)
				}
			}
		})
	}}
	useFake(t, f)
	g := newTestGateway(0)
	pub := newWHIPPublisher(t, h264High)
	st, err := g.Publish(context.Background(), whip.Request{MeetingURL: "https://meet.test/Room", Name: "OBS", Offer: pub.offer(t)})
	if err != nil {
		t.Fatal(err)
	}
	defer st.End()
	if err := pub.pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: st.Answer}); err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	defer close(stop)
	go pub.send(stop)

	select {
	case mime := <-videoCodec:
		if mime != webrtc.MimeTypeH264 {
			t.Errorf("the meeting receives %s video, want H.264", mime)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no video reached the meeting")
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if audio.Load() > 10 && pub.plis.Load() > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if a := audio.Load(); a <= 10 {
		t.Errorf("%d audio packets reached the meeting", a)
	}
	if pub.plis.Load() == 0 {
		t.Error("the meeting's keyframe request did not reach the publisher")
	}
	if p := f.LastPolicy(); p == nil || p.LastN != 0 {
		t.Errorf("receive policy %+v, want LastN 0", p)
	}

	st.End()
	select {
	case <-st.Done:
	case <-time.After(5 * time.Second):
		t.Fatal("stream not done after End")
	}
	if !f.Disconnected() {
		t.Error("the participant did not leave the meeting")
	}
	if err := st.Err(); err != nil {
		t.Errorf("Err() = %v after End, want nil", err)
	}
}

func TestGateway_RefusesOtherCodecsWithoutJoining(t *testing.T) {
	f := &fakeSignal{}
	useFake(t, f)
	g := newTestGateway(0)
	pub := newWHIPPublisher(t, webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeAV1, ClockRate: 90000})
	_, err := g.Publish(context.Background(), whip.Request{MeetingURL: "https://meet.test/Room", Name: "OBS", Offer: pub.offer(t)})
	var we *whip.Error
	if !errors.As(err, &we) || we.Status != http.StatusBadRequest {
		t.Fatalf("Publish with AV1: %v, want a 400 whip.Error", err)
	}
	if f.Handler() != nil {
		t.Error("joined the meeting")
	}
}

func TestGateway_EndsWhenThePublisherStops(t *testing.T) {
	useFake(t, &fakeSignal{})
	g := newTestGateway(500 * time.Millisecond)
	pub := newWHIPPublisher(t, h264High)
	st, err := g.Publish(context.Background(), whip.Request{MeetingURL: "https://meet.test/Room", Name: "OBS", Offer: pub.offer(t)})
	if err != nil {
		t.Fatal(err)
	}
	defer st.End()
	// The publisher never connects, so no media comes.
	select {
	case <-st.Done:
		if st.Err() == nil {
			t.Error("Err() is nil after an idle end")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stream not ended without media")
	}
}

func TestGateway_JoinFailure(t *testing.T) {
	useFake(t, &fakeSignal{ConnectErr: errFake})
	g := newTestGateway(0)
	pub := newWHIPPublisher(t, h264High)
	if _, err := g.Publish(context.Background(), whip.Request{MeetingURL: "https://meet.test/Room", Name: "OBS", Offer: pub.offer(t)}); !errors.Is(err, errFake) {
		t.Fatalf("Publish: %v, want the join error", err)
	}
}

func TestQuality(t *testing.T) {
	for _, c := range []struct {
		loss float64
		want int
	}{{0, 100}, {2, 100}, {2.5, 70}, {4, 70}, {5, 50}, {7, 30}, {10, 10}, {12, 10}, {12.1, 0}, {50, 0}} {
		if got := quality(c.loss); got != c.want {
			t.Errorf("quality(%v) = %d, want %d", c.loss, got, c.want)
		}
	}
}

func TestStreamStats_Window(t *testing.T) {
	var w statsWindow
	t0 := time.Unix(100, 0)
	w.next(peer.IngestStats{}, 0, 0, t0)
	got := w.next(peer.IngestStats{
		Video: peer.KindStats{Packets: 900, Bytes: 1_250_000, Lost: 100},
		Audio: peer.KindStats{Packets: 500, Bytes: 40_000},
	}, 10, 0, t0.Add(5*time.Second))
	if got.VideoKbps != 2000 || got.AudioKbps != 64 {
		t.Errorf("bitrates %d and %d kbps, want 2000 and 64", got.VideoKbps, got.AudioKbps)
	}
	// 100 lost of 1500 expected from the publisher; the bridge reports 10 %
	// of video and 0 % of audio lost, weighted by packets: 6.43 %, which
	// already includes the publisher's gaps.
	if got.PublisherLoss < 6.66 || got.PublisherLoss > 6.67 {
		t.Errorf("publisher loss %v%%, want 6.67", got.PublisherLoss)
	}
	if got.MeetingLoss != got.PublisherLoss {
		t.Errorf("meeting loss %v%%, want the larger of the publisher's (%v) and the bridge's (6.43)", got.MeetingLoss, got.PublisherLoss)
	}
	if bigger := w.next(peer.IngestStats{
		Video: peer.KindStats{Packets: 1800, Bytes: 2_500_000, Lost: 100},
		Audio: peer.KindStats{Packets: 1000, Bytes: 80_000},
	}, 20, 0, t0.Add(10*time.Second)); bigger.MeetingLoss < 12.85 || bigger.MeetingLoss > 12.86 {
		t.Errorf("meeting loss %v%%, want the bridge's 12.86 (20 %% of 900 video of 1400)", bigger.MeetingLoss)
	}
	if got.Quality != quality(got.MeetingLoss) {
		t.Errorf("quality %d for %v%% loss", got.Quality, got.MeetingLoss)
	}
	// Counts that did not move give zeros, not a division by zero.
	if z := w.next(w.prev, 0, 0, t0.Add(15*time.Second)); z.VideoKbps != 0 || z.PublisherLoss != 0 || z.Quality != 100 {
		t.Errorf("idle window %+v", z)
	}
}

func TestGateway_ReportsStatsToTheMeeting(t *testing.T) {
	f := &fakeSignal{}
	useFake(t, f)
	g := NewGateway(GatewayConfig{Session: Config{Logger: quietLogger}, StatsInterval: 300 * time.Millisecond})
	pub := newWHIPPublisher(t, h264High)
	st, err := g.Publish(context.Background(), whip.Request{MeetingURL: "https://meet.test/Room", Name: "OBS", Offer: pub.offer(t)})
	if err != nil {
		t.Fatal(err)
	}
	defer st.End()
	if err := pub.pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: st.Answer}); err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	defer close(stop)
	go pub.send(stop)
	var last signaling.EndpointStats
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if s := f.Stats(); len(s) > 0 && s[len(s)-1].VideoUploadKbps > 0 {
			last = s[len(s)-1]
			break
		}
	}
	if last.VideoUploadKbps == 0 || last.AudioUploadKbps == 0 || last.UploadKbps != last.VideoUploadKbps+last.AudioUploadKbps {
		t.Fatalf("stats sent %+v, want video and audio bitrates", last)
	}
	if last.Quality != 100 || last.UploadLoss != 0 {
		t.Errorf("stats %+v, want no loss and quality 100 on loopback", last)
	}
	if s := st.Stats(); s.VideoKbps == 0 || s.Quality != 100 {
		t.Errorf("Stream.Stats() = %+v", s)
	}
}
