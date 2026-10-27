package peer

import (
	"bytes"
	"errors"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v3"
)

// publisherPC is a WHIP publisher: H.264 High profile (as OBS's x264
// sends) and Opus, with Pion's default interceptors, which add header
// extensions to the packets.
func publisherPC(t *testing.T, video webrtc.RTPCodecCapability, audio bool) (*webrtc.PeerConnection, *webrtc.TrackLocalStaticRTP, *webrtc.RTPSender) {
	t.Helper()
	m := &webrtc.MediaEngine{}
	if err := m.RegisterCodec(webrtc.RTPCodecParameters{RTPCodecCapability: video, PayloadType: 106}, webrtc.RTPCodecTypeVideo); err != nil {
		t.Fatal(err)
	}
	if err := m.RegisterCodec(webrtc.RTPCodecParameters{RTPCodecCapability: OpusCodec, PayloadType: 111}, webrtc.RTPCodecTypeAudio); err != nil {
		t.Fatal(err)
	}
	ir := &interceptor.Registry{}
	if err := webrtc.RegisterDefaultInterceptors(m, ir); err != nil {
		t.Fatal(err)
	}
	api := webrtc.NewAPI(webrtc.WithMediaEngine(m), webrtc.WithInterceptorRegistry(ir))
	pc, err := api.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pc.Close() })
	vt, err := webrtc.NewTrackLocalStaticRTP(video, "video", "obs")
	if err != nil {
		t.Fatal(err)
	}
	vs, err := pc.AddTransceiverFromTrack(vt, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionSendonly})
	if err != nil {
		t.Fatal(err)
	}
	if audio {
		at, err := webrtc.NewTrackLocalStaticRTP(OpusCodec, "audio", "obs")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pc.AddTransceiverFromTrack(at, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionSendonly}); err != nil {
			t.Fatal(err)
		}
	}
	return pc, vt, vs.Sender()
}

var h264High = webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000,
	SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=64001f"}

// offerOf returns pc's offer with all candidates.
func offerOf(t *testing.T, pc *webrtc.PeerConnection) string {
	t.Helper()
	offer, err := pc.CreateOffer(nil)
	if err != nil {
		t.Fatal(err)
	}
	gathered := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(offer); err != nil {
		t.Fatal(err)
	}
	<-gathered
	return pc.LocalDescription().SDP
}

// packetLog is a TrackWriter that keeps what it is given.
type packetLog struct {
	mu   sync.Mutex
	pkts []*rtp.Packet
	got  chan struct{}
}

func newPacketLog() *packetLog { return &packetLog{got: make(chan struct{}, 100)} }

func (l *packetLog) WriteRTP(p *rtp.Packet) error {
	l.mu.Lock()
	l.pkts = append(l.pkts, p.Clone())
	l.mu.Unlock()
	select {
	case l.got <- struct{}{}:
	default:
	}
	return nil
}

func (l *packetLog) packets() []*rtp.Packet {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]*rtp.Packet(nil), l.pkts...)
}

func newTestIngest(t *testing.T, offer string) (*Ingest, string) {
	t.Helper()
	ing, answer, err := NewIngest(IngestConfig{Logger: quietLogger, IdleTimeout: 2 * time.Second}, offer)
	if err != nil {
		t.Fatalf("NewIngest: %v", err)
	}
	t.Cleanup(func() { ing.Close() })
	return ing, answer
}

func TestIngest_ForwardsUnchanged(t *testing.T) {
	pub, vt, _ := publisherPC(t, h264High, true)
	ing, answer := newTestIngest(t, offerOf(t, pub))
	if got := ing.Kinds(); len(got) != 2 {
		t.Fatalf("Kinds() = %v, want audio and video", got)
	}
	if err := pub.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: answer}); err != nil {
		t.Fatal(err)
	}
	video := newPacketLog()
	ing.Forward(KindVideo, video)

	payload := []byte{0x65, 0x88, 0x84, 0x00, 0x33} // An IDR NAL unit
	deadline := time.Now().Add(5 * time.Second)
	for seq := uint16(7); len(video.packets()) < 3 && time.Now().Before(deadline); seq++ { // SRTP drops replayed numbers
		vt.WriteRTP(&rtp.Packet{Header: rtp.Header{Version: 2, Marker: true, Timestamp: 3000, SequenceNumber: seq}, Payload: payload})
		time.Sleep(20 * time.Millisecond)
	}
	pkts := video.packets()
	if len(pkts) < 3 {
		t.Fatalf("%d packets forwarded, want some", len(pkts))
	}
	for _, p := range pkts {
		if !bytes.Equal(p.Payload, payload) || p.Timestamp != 3000 || !p.Marker {
			t.Fatalf("forwarded %+v, want the payload, timestamp and marker sent", p)
		}
		if p.Extension || len(p.Extensions) != 0 {
			t.Fatalf("forwarded packet keeps the publisher's header extensions: %+v", p.Header)
		}
	}
}

func TestIngest_DropsBeforeForward(t *testing.T) {
	pub, vt, _ := publisherPC(t, h264High, false)
	ing, answer := newTestIngest(t, offerOf(t, pub))
	if err := pub.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: answer}); err != nil {
		t.Fatal(err)
	}
	if got := ing.Kinds(); len(got) != 1 || got[0] != KindVideo {
		t.Fatalf("Kinds() = %v, want video", got)
	}
	seq := uint16(0)
	for i := 0; i < 20; i++ { // Nowhere to go yet
		seq++
		vt.WriteRTP(&rtp.Packet{Header: rtp.Header{Version: 2, Timestamp: 1, SequenceNumber: seq}, Payload: []byte{1}})
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond) // Let them all arrive
	video := newPacketLog()
	ing.Forward(KindVideo, video)
	for i := 0; i < 50 && len(video.packets()) == 0; i++ {
		seq++
		vt.WriteRTP(&rtp.Packet{Header: rtp.Header{Version: 2, Timestamp: 2, SequenceNumber: seq}, Payload: []byte{2}})
		time.Sleep(20 * time.Millisecond)
	}
	for _, p := range video.packets() {
		if p.Timestamp != 2 {
			t.Fatalf("a packet sent before Forward was kept: %+v", p.Header)
		}
	}
	if len(video.packets()) == 0 {
		t.Fatal("nothing forwarded")
	}
}

func TestIngest_RequestKeyframe(t *testing.T) {
	pub, vt, sender := publisherPC(t, h264High, false)
	ing, answer := newTestIngest(t, offerOf(t, pub))
	if err := pub.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: answer}); err != nil {
		t.Fatal(err)
	}
	video := newPacketLog()
	ing.Forward(KindVideo, video)
	for i := 0; i < 100 && len(video.packets()) == 0; i++ { // The ingest knows the SSRC once a packet came
		vt.WriteRTP(&rtp.Packet{Header: rtp.Header{Version: 2, SequenceNumber: uint16(i)}, Payload: []byte{1}})
		time.Sleep(20 * time.Millisecond)
	}
	pli := make(chan struct{}, 1)
	go func() {
		for {
			pkts, _, err := sender.ReadRTCP()
			if err != nil {
				return
			}
			for _, p := range pkts {
				if _, ok := p.(*rtcp.PictureLossIndication); ok {
					select {
					case pli <- struct{}{}:
					default:
					}
				}
			}
		}
	}()
	ing.RequestKeyframe()
	select {
	case <-pli:
	case <-time.After(3 * time.Second):
		t.Fatal("no PLI reached the publisher")
	}
}

func TestIngest_RefusesOtherVideoCodecs(t *testing.T) {
	av1 := webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeAV1, ClockRate: 90000}
	pub, _, _ := publisherPC(t, av1, true)
	_, _, err := NewIngest(IngestConfig{Logger: quietLogger}, offerOf(t, pub))
	if !errors.Is(err, ErrUnsupportedOffer) {
		t.Fatalf("NewIngest with AV1 video: %v, want ErrUnsupportedOffer", err)
	}
}

// An ingest whose publisher never sends (here never connects) ends.
func TestIngest_EndsWhenIdle(t *testing.T) {
	pub, _, _ := publisherPC(t, h264High, false)
	ing, _, err := NewIngest(IngestConfig{Logger: quietLogger, IdleTimeout: 300 * time.Millisecond}, offerOf(t, pub))
	if err != nil {
		t.Fatal(err)
	}
	defer ing.Close()
	select {
	case <-ing.Done():
		if ing.Err() == nil {
			t.Error("Err() is nil after an idle end")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("an idle ingest did not end")
	}
}

func TestPeer_SendsH264(t *testing.T) {
	p, err := New(Config{Logger: quietLogger, Kinds: []string{KindVideo}, VideoCodec: webrtc.MimeTypeH264})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	remote, err := webrtc.NewPeerConnection(webrtc.Configuration{}) // Offers VP8, VP9 and H.264, as Jicofo
	if err != nil {
		t.Fatal(err)
	}
	defer remote.Close()
	if _, err := remote.AddTransceiverFromKind(webrtc.RTPCodecTypeVideo, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly}); err != nil {
		t.Fatal(err)
	}
	codec := make(chan string, 1)
	remote.OnTrack(func(tr *webrtc.TrackRemote, _ *webrtc.RTPReceiver) { codec <- tr.Codec().MimeType })
	connect(t, remote, p)
	for i := 0; i < 250; i++ {
		select {
		case c := <-codec:
			if c != webrtc.MimeTypeH264 {
				t.Fatalf("remote receives %s, want H.264", c)
			}
			return
		default:
		}
		p.LocalTrack(KindVideo).WriteRTP(&rtp.Packet{Header: rtp.Header{Version: 2, SequenceNumber: uint16(i)}, Payload: []byte{0x65, 1}})
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("no video reached the remote side")
}

// Ingests share one UDP port, and give the public address in their answer.
func TestIngest_UDPMuxAndPublicIP(t *testing.T) {
	free, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	port := strconv.Itoa(free.LocalAddr().(*net.UDPAddr).Port)
	free.Close()
	mux, err := NewUDPMux(free.LocalAddr().(*net.UDPAddr).Port, quietLogger)
	if err != nil {
		t.Fatal(err)
	}
	defer mux.Close()
	for i := 0; i < 2; i++ {
		pub, _, _ := publisherPC(t, h264High, true)
		ing, answer, err := NewIngest(IngestConfig{Logger: quietLogger, UDPMux: mux, PublicIPs: []string{"203.0.113.5"}}, offerOf(t, pub))
		if err != nil {
			t.Fatal(err)
		}
		defer ing.Close()
		var cands []string
		for _, l := range strings.Split(answer, "\n") {
			if strings.HasPrefix(l, "a=candidate:") {
				cands = append(cands, strings.TrimSpace(l))
			}
		}
		if len(cands) == 0 {
			t.Fatalf("answer %d has no candidates", i)
		}
		for _, c := range cands {
			if !strings.Contains(c, " 203.0.113.5 "+port+" ") {
				t.Errorf("answer %d: candidate %q, want 203.0.113.5:%s", i, c, port)
			}
		}
	}
}

func TestH264Keyframe(t *testing.T) {
	for _, c := range []struct {
		name string
		p    []byte
		want bool
	}{
		{"IDR", []byte{0x65, 0x88}, true},
		{"SPS", []byte{0x67, 0x64}, true},
		{"P slice", []byte{0x41, 0x9a}, false},
		{"STAP-A with SPS", []byte{0x78, 0x00, 0x02, 0x67, 0x64, 0x00, 0x02, 0x68, 0xee}, true},
		{"STAP-A without", []byte{0x78, 0x00, 0x02, 0x06, 0x05, 0x00, 0x02, 0x41, 0x9a}, false},
		{"FU-A IDR start", []byte{0x7c, 0x85, 0x88}, true},
		{"FU-A IDR middle", []byte{0x7c, 0x05, 0x88}, false},
		{"FU-A P start", []byte{0x7c, 0x81, 0x9a}, false},
		{"short", []byte{0x65}, false},
	} {
		if got := h264Keyframe(c.p); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

// The ingest asks again for a keyframe until one comes, as a publisher may
// miss or ignore the first request.
func TestIngest_RepeatsKeyframeRequestsUntilOne(t *testing.T) {
	pub, vt, sender := publisherPC(t, h264High, false)
	ing, answer := newTestIngest(t, offerOf(t, pub))
	if err := pub.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: answer}); err != nil {
		t.Fatal(err)
	}
	var plis atomic.Int32
	go func() {
		for {
			pkts, _, err := sender.ReadRTCP()
			if err != nil {
				return
			}
			for _, p := range pkts {
				if _, ok := p.(*rtcp.PictureLossIndication); ok {
					plis.Add(1)
				}
			}
		}
	}()
	ing.Forward(KindVideo, newPacketLog())
	seq := uint16(0)
	send := func(payload []byte, d time.Duration) {
		for end := time.Now().Add(d); time.Now().Before(end); seq++ {
			vt.WriteRTP(&rtp.Packet{Header: rtp.Header{Version: 2, SequenceNumber: seq}, Payload: payload})
			time.Sleep(20 * time.Millisecond)
		}
	}
	send([]byte{0x41, 0x9a}, 1600*time.Millisecond) // P frames only
	if n := plis.Load(); n < 3 {
		t.Fatalf("%d keyframe requests in 1.6 s without a keyframe, want one every 500 ms", n)
	}
	send([]byte{0x65, 0x88}, 100*time.Millisecond) // A keyframe
	n := plis.Load()
	send([]byte{0x41, 0x9a}, 1200*time.Millisecond)
	if more := plis.Load() - n; more > 0 {
		t.Errorf("%d keyframe requests after the keyframe, want none", more)
	}
}
