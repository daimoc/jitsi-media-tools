package peer

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/ice/v2"
	"github.com/pion/interceptor"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"github.com/pion/sdp/v3"
	"github.com/pion/webrtc/v3"
)

// ErrUnsupportedOffer is returned by NewIngest for an offer that has no
// media the ingest can forward.
var ErrUnsupportedOffer = errors.New("unsupported offer")

// errIdle ends an ingest whose publisher stopped sending.
var errIdle = errors.New("no media from the publisher")

// Ingest defaults.
const (
	defaultIngestIdle   = 10 * time.Second
	ingestGatherTimeout = 2 * time.Second
	minKeyframeInterval = 500 * time.Millisecond // Between PLIs sent to the publisher
)

// UDPMux carries the media of several peers on one UDP port.
type UDPMux = ice.UDPMux

// NewUDPMux serves conn to the peers given it.
func NewUDPMux(conn net.PacketConn, logger *slog.Logger) UDPMux {
	return webrtc.NewICEUDPMux(slogLoggerFactory{logger}.NewLogger("udpmux"), conn)
}

// RTPWriter takes forwarded packets: a Peer's LocalTrack.
type RTPWriter interface {
	WriteRTP(p *rtp.Packet) error
}

// IngestConfig sets up an Ingest.
type IngestConfig struct {
	Logger      *slog.Logger
	UDPMux      UDPMux        // nil: a port of its own
	PublicIPs   []string      // Given as host candidates instead of the local addresses
	IdleTimeout time.Duration // 0: 10 s
}

// Ingest receives a WHIP publisher's H.264 and Opus and forwards the
// packets unchanged, but for their header extensions, which belong to this
// connection (research R14).
type Ingest struct {
	pc     *webrtc.PeerConnection
	logger *slog.Logger
	kinds  []string

	mu   sync.Mutex
	dest map[string]RTPWriter // By kind

	videoSSRC atomic.Uint32
	last      atomic.Int64 // Unix nanoseconds of the last packet, or of the start
	lastPLI   atomic.Int64

	done      chan struct{}
	err       error // Set before done closes
	closeOnce sync.Once
}

// NewIngest answers a publisher's offer. It fails with ErrUnsupportedOffer
// when a video section has no H.264 in packetization mode 1, or an audio
// section no Opus.
func NewIngest(cfg IngestConfig, offer string) (*Ingest, string, error) {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.IdleTimeout <= 0 {
		cfg.IdleTimeout = defaultIngestIdle
	}
	m := &webrtc.MediaEngine{}
	kinds, err := registerOffered(m, offer)
	if err != nil {
		return nil, "", err
	}
	registry := &interceptor.Registry{}
	if err := webrtc.RegisterDefaultInterceptors(m, registry); err != nil {
		return nil, "", err
	}
	var se webrtc.SettingEngine
	se.LoggerFactory = slogLoggerFactory{cfg.Logger}
	if cfg.UDPMux != nil {
		se.SetICEUDPMux(cfg.UDPMux)
		se.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4})
	}
	if len(cfg.PublicIPs) > 0 {
		se.SetNAT1To1IPs(cfg.PublicIPs, webrtc.ICECandidateTypeHost)
	}
	if settingEngineHook != nil {
		settingEngineHook(&se)
	}
	api := webrtc.NewAPI(webrtc.WithMediaEngine(m), webrtc.WithInterceptorRegistry(registry), webrtc.WithSettingEngine(se))
	pc, err := api.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		return nil, "", err
	}
	in := &Ingest{pc: pc, logger: cfg.Logger, kinds: kinds, dest: map[string]RTPWriter{}, done: make(chan struct{})}
	in.last.Store(time.Now().UnixNano())
	pc.OnTrack(func(t *webrtc.TrackRemote, _ *webrtc.RTPReceiver) { go in.forward(t) })
	pc.OnConnectionStateChange(func(s webrtc.PeerConnectionState) {
		switch s {
		case webrtc.PeerConnectionStateFailed:
			in.end(errors.New("the connection to the publisher failed"))
		case webrtc.PeerConnectionStateClosed:
			in.end(errors.New("the publisher closed the connection"))
		}
	})
	answer, err := in.answer(offer)
	if err != nil {
		pc.Close()
		return nil, "", err
	}
	go in.watchIdle(cfg.IdleTimeout)
	return in, answer, nil
}

// registerOffered registers the offer's H.264 (packetization mode 1) and
// Opus formats as they are, whatever their profile, and returns the kinds
// the offer sends.
func registerOffered(m *webrtc.MediaEngine, offer string) ([]string, error) {
	var parsed sdp.SessionDescription
	if err := parsed.Unmarshal([]byte(offer)); err != nil {
		return nil, fmt.Errorf("%w: cannot parse it: %v", ErrUnsupportedOffer, err)
	}
	var kinds []string
	seen := map[string]bool{}
	for _, md := range parsed.MediaDescriptions {
		kind := md.MediaName.Media
		if kind != KindAudio && kind != KindVideo || md.MediaName.Port.Value == 0 || seen[kind] {
			continue
		}
		if _, ok := md.Attribute("recvonly"); ok {
			continue
		}
		if _, ok := md.Attribute("inactive"); ok {
			continue
		}
		fmtp := map[string]string{}
		for _, a := range md.Attributes {
			if a.Key == "fmtp" {
				pt, line, _ := strings.Cut(a.Value, " ")
				fmtp[pt] = line
			}
		}
		n := 0
		for _, a := range md.Attributes {
			if a.Key != "rtpmap" {
				continue
			}
			pt, name, _ := strings.Cut(a.Value, " ")
			id, err := strconv.ParseUint(pt, 10, 8)
			if err != nil {
				continue
			}
			var c webrtc.RTPCodecCapability
			switch {
			case kind == KindVideo && strings.EqualFold(name, "H264/90000") && strings.Contains(fmtp[pt], "packetization-mode=1"):
				c = webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000, SDPFmtpLine: fmtp[pt], RTCPFeedback: videoFeedback}
			case kind == KindAudio && strings.EqualFold(name, "opus/48000/2"):
				c = webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2, SDPFmtpLine: fmtp[pt]}
			default:
				continue
			}
			typ := webrtc.RTPCodecTypeVideo
			if kind == KindAudio {
				typ = webrtc.RTPCodecTypeAudio
			}
			if err := m.RegisterCodec(webrtc.RTPCodecParameters{RTPCodecCapability: c, PayloadType: webrtc.PayloadType(id)}, typ); err != nil {
				return nil, err
			}
			n++
		}
		if n == 0 {
			if kind == KindVideo {
				return nil, fmt.Errorf("%w: the video is not H.264 (packetization mode 1); set the encoder to H.264", ErrUnsupportedOffer)
			}
			return nil, fmt.Errorf("%w: the audio is not Opus", ErrUnsupportedOffer)
		}
		seen[kind] = true
		kinds = append(kinds, kind)
	}
	if len(kinds) == 0 {
		return nil, fmt.Errorf("%w: it sends no audio or video", ErrUnsupportedOffer)
	}
	return kinds, nil
}

// answer applies the offer and returns the answer with all candidates:
// WHIP has no trickle here.
func (in *Ingest) answer(offer string) (string, error) {
	if err := in.pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: offer}); err != nil {
		return "", fmt.Errorf("%w: %v", ErrUnsupportedOffer, err)
	}
	answer, err := in.pc.CreateAnswer(nil)
	if err != nil {
		return "", err
	}
	gathered := webrtc.GatheringCompletePromise(in.pc)
	if err := in.pc.SetLocalDescription(answer); err != nil {
		return "", err
	}
	select {
	case <-gathered:
	case <-time.After(ingestGatherTimeout):
	}
	return in.pc.LocalDescription().SDP, nil
}

// Kinds returns the media kinds the publisher sends.
func (in *Ingest) Kinds() []string { return in.kinds }

// Forward sends the packets of kind received from now on to w.
func (in *Ingest) Forward(kind string, w RTPWriter) {
	in.mu.Lock()
	in.dest[kind] = w
	in.mu.Unlock()
}

func (in *Ingest) forward(t *webrtc.TrackRemote) {
	kind := t.Kind().String()
	if kind == KindVideo {
		in.videoSSRC.Store(uint32(t.SSRC()))
	}
	in.logger.Debug("Receiving from the publisher", slog.String("kind", kind), slog.String("codec", t.Codec().MimeType))
	for {
		pkt, _, err := t.ReadRTP()
		if err != nil {
			return
		}
		in.last.Store(time.Now().UnixNano())
		if len(pkt.Payload) == 0 {
			continue // Padding, for bandwidth probing
		}
		pkt.Header.Extension = false
		pkt.Header.Extensions = nil
		in.mu.Lock()
		w := in.dest[kind]
		in.mu.Unlock()
		if w != nil {
			_ = w.WriteRTP(pkt)
		}
	}
}

// RequestKeyframe asks the publisher for a keyframe, at most every 500 ms.
func (in *Ingest) RequestKeyframe() {
	ssrc := in.videoSSRC.Load()
	if ssrc == 0 {
		return
	}
	now := time.Now().UnixNano()
	if last := in.lastPLI.Load(); now-last < int64(minKeyframeInterval) || !in.lastPLI.CompareAndSwap(last, now) {
		return
	}
	_ = in.pc.WriteRTCP([]rtcp.Packet{&rtcp.PictureLossIndication{MediaSSRC: ssrc}})
}

func (in *Ingest) watchIdle(timeout time.Duration) {
	tick := time.NewTicker(timeout / 4)
	defer tick.Stop()
	for {
		select {
		case <-in.done:
			return
		case <-tick.C:
			if time.Since(time.Unix(0, in.last.Load())) > timeout {
				in.end(errIdle)
				return
			}
		}
	}
}

// end closes the ingest once, recording why.
func (in *Ingest) end(err error) {
	in.closeOnce.Do(func() {
		in.err = err
		close(in.done)
		in.pc.Close()
	})
}

// Done is closed when the ingest has ended: Close, the connection failing
// or closing, or no media for the idle timeout.
func (in *Ingest) Done() <-chan struct{} { return in.done }

// Err says why the ingest ended; nil after Close or before it ended.
func (in *Ingest) Err() error {
	select {
	case <-in.done:
		return in.err
	default:
		return nil
	}
}

// Close ends the ingest.
func (in *Ingest) Close() error {
	in.end(nil)
	return nil
}
