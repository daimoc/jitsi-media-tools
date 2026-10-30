package peer

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"slices"
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

// NewUDPMux listens on port of every IPv4 interface, loopback included,
// for the peers given it (0: a free port per interface). For peers given
// a public address it is one socket on every interface: Pion keeps a
// single candidate for that address, tied to the first socket, and would
// drop what reaches the others, such as a container's eth0.
func NewUDPMux(port int, public bool, logger *slog.Logger) (UDPMux, error) {
	if public {
		conn, err := net.ListenUDP("udp4", &net.UDPAddr{Port: port})
		if err != nil {
			return nil, err
		}
		return ice.NewUDPMuxDefault(ice.UDPMuxParams{UDPConn: conn,
			Logger: slogLoggerFactory{logger}.NewLogger("udpmux")}), nil
	}
	return ice.NewMultiUDPMuxFromPort(port,
		ice.UDPMuxFromPortWithNetworks(ice.NetworkTypeUDP4),
		ice.UDPMuxFromPortWithLoopback(),
		ice.UDPMuxFromPortWithLogger(slogLoggerFactory{logger}.NewLogger("udpmux")))
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
	pc         *webrtc.PeerConnection
	logger     *slog.Logger
	kinds      []string
	videoCodec string // webrtc.MimeTypeH264 or webrtc.MimeTypeAV1

	mu   sync.Mutex
	dest map[string]RTPWriter // By kind

	videoSSRC atomic.Uint32
	last      atomic.Int64 // Unix nanoseconds of the last packet, or of the start
	lastPLI   atomic.Int64
	counts    map[string]*seqCounter // By kind; fixed after NewIngest

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
	kinds, videoCodec, err := registerOffered(m, offer)
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
	in := &Ingest{pc: pc, logger: cfg.Logger, kinds: kinds, videoCodec: videoCodec, dest: map[string]RTPWriter{}, done: make(chan struct{}),
		counts: map[string]*seqCounter{KindAudio: {}, KindVideo: {}}}
	in.last.Store(time.Now().UnixNano())
	pc.OnTrack(func(t *webrtc.TrackRemote, _ *webrtc.RTPReceiver) { go in.forward(t) })
	pc.OnConnectionStateChange(func(s webrtc.PeerConnectionState) {
		in.logger.Debug("Publisher connection", slog.String("state", s.String()))
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

// CheckOffer returns the media kinds an Ingest would forward from offer and
// its video codec (webrtc.MimeTypeH264 or webrtc.MimeTypeAV1, "" without
// video), or ErrUnsupportedOffer, without creating anything.
func CheckOffer(offer string) ([]string, string, error) {
	return registerOffered(&webrtc.MediaEngine{}, offer)
}

// registerOffered registers the offer's H.264 (packetization mode 1) and
// Opus formats as they are, whatever their profile, and returns the kinds
// the offer sends and its video codec. AV1 is registered only when the
// offer has no H.264, which more viewers can decode: the bridge forwards
// the codec as it comes, and a viewer without an AV1 decoder sees nothing.
func registerOffered(m *webrtc.MediaEngine, offer string) ([]string, string, error) {
	var parsed sdp.SessionDescription
	if err := parsed.Unmarshal([]byte(offer)); err != nil {
		return nil, "", fmt.Errorf("%w: cannot parse it: %v", ErrUnsupportedOffer, err)
	}
	videoCodec := ""
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
		var offered []webrtc.RTPCodecParameters
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
			case kind == KindVideo && strings.EqualFold(name, "AV1/90000"):
				c = webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeAV1, ClockRate: 90000, SDPFmtpLine: fmtp[pt], RTCPFeedback: videoFeedback}
			case kind == KindAudio && strings.EqualFold(name, "opus/48000/2"):
				c = webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2, SDPFmtpLine: fmtp[pt]}
			default:
				continue
			}
			offered = append(offered, webrtc.RTPCodecParameters{RTPCodecCapability: c, PayloadType: webrtc.PayloadType(id)})
		}
		if kind == KindVideo {
			for _, want := range []string{webrtc.MimeTypeH264, webrtc.MimeTypeAV1} {
				if slices.ContainsFunc(offered, func(c webrtc.RTPCodecParameters) bool { return c.MimeType == want }) {
					videoCodec = want
					offered = slices.DeleteFunc(offered, func(c webrtc.RTPCodecParameters) bool { return c.MimeType != want })
					break
				}
			}
		}
		if len(offered) == 0 {
			if kind == KindVideo {
				return nil, "", fmt.Errorf("%w: the video is neither H.264 (packetization mode 1) nor AV1; set the encoder to H.264 or AV1", ErrUnsupportedOffer)
			}
			return nil, "", fmt.Errorf("%w: the audio is not Opus", ErrUnsupportedOffer)
		}
		typ := webrtc.RTPCodecTypeVideo
		if kind == KindAudio {
			typ = webrtc.RTPCodecTypeAudio
		}
		for _, c := range offered {
			if err := m.RegisterCodec(c, typ); err != nil {
				return nil, "", err
			}
		}
		seen[kind] = true
		kinds = append(kinds, kind)
	}
	if len(kinds) == 0 {
		return nil, "", fmt.Errorf("%w: it sends no audio or video", ErrUnsupportedOffer)
	}
	return kinds, videoCodec, nil
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

// VideoCodec returns the publisher's video codec: webrtc.MimeTypeH264 or
// webrtc.MimeTypeAV1, "" without video.
func (in *Ingest) VideoCodec() string { return in.videoCodec }

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
	var since time.Time // When video forwarding started
	keyed := false      // A keyframe was forwarded since
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
		in.counts[kind].add(pkt.SequenceNumber, pkt.MarshalSize())
		in.mu.Lock()
		w := in.dest[kind]
		in.mu.Unlock()
		if w == nil {
			continue
		}
		if kind == KindVideo && !keyed {
			// The meeting shows video from a keyframe on: ask until one
			// comes (RequestKeyframe allows one request every 500 ms).
			if since.IsZero() {
				since = time.Now()
			}
			if keyed = in.keyframe(pkt.Payload); keyed {
				in.keyframeCame(time.Since(since))
			} else {
				in.RequestKeyframe()
			}
		}
		_ = w.WriteRTP(pkt)
	}
}

// IngestStats counts what the publisher sent, per kind, since the start.
type IngestStats struct{ Audio, Video KindStats }

// KindStats counts one kind's packets: received, their bytes (RTP, as
// forwarded), and lost on the way (sequence numbers never received).
type KindStats struct{ Packets, Bytes, Lost uint64 }

// Stats returns the counts so far.
func (in *Ingest) Stats() IngestStats {
	return IngestStats{Audio: in.counts[KindAudio].stats(), Video: in.counts[KindVideo].stats()}
}

// seqCounter counts a stream's packets and the sequence numbers missing
// between the first and the highest received (RFC 3550 A.3).
type seqCounter struct {
	mu             sync.Mutex
	started        bool
	first, highest uint64 // Extended sequence numbers
	packets, bytes uint64
}

func (c *seqCounter) add(seq uint16, size int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.packets++
	c.bytes += uint64(size)
	if !c.started {
		c.started, c.first, c.highest = true, uint64(seq), uint64(seq)
		return
	}
	if d := int16(seq - uint16(c.highest)); d > 0 { // Newer, across a wrap too
		c.highest += uint64(d)
	}
}

func (c *seqCounter) stats() KindStats {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := KindStats{Packets: c.packets, Bytes: c.bytes}
	if expected := c.highest - c.first + 1; c.started && expected > c.packets {
		s.Lost = expected - c.packets // Duplicates (retransmissions) may hide some
	}
	return s
}

// slowKeyframe is a wait for the first keyframe that shows the publisher
// ignores keyframe requests.
const slowKeyframe = 2 * time.Second

func (in *Ingest) keyframeCame(wait time.Duration) {
	if wait < slowKeyframe {
		in.logger.Info("Video forwarded from the publisher's first keyframe", slog.Duration("waited", wait.Round(time.Millisecond)))
		return
	}
	in.logger.Warn("The meeting waited for the publisher's next keyframe: it ignores keyframe requests; "+
		"set its keyframe interval to 1 s (OBS: Settings, Output, Keyframe Interval)", slog.Duration("waited", wait.Round(time.Millisecond)))
}

// keyframe reports whether a video RTP payload starts a keyframe.
func (in *Ingest) keyframe(p []byte) bool {
	if in.videoCodec == webrtc.MimeTypeAV1 {
		return av1Keyframe(p)
	}
	return h264Keyframe(p)
}

// av1Keyframe reports whether an AV1 RTP payload (AV1 RTP specification,
// 4.4) starts a coded video sequence: its N bit is set, or its first OBU
// element, not a continuation, is a sequence header.
func av1Keyframe(p []byte) bool {
	if len(p) < 2 {
		return false
	}
	if p[0]&0x08 != 0 { // N
		return true
	}
	if p[0]&0x80 != 0 { // Z: the first element continues an OBU
		return false
	}
	obu := p[1:]
	if p[0]&0x30 != 0x10 { // W is not 1: the first element has a LEB128 length
		n := 0
		for n < len(obu) && obu[n]&0x80 != 0 {
			n++
		}
		if n+1 >= len(obu) {
			return false
		}
		obu = obu[n+1:]
	}
	return (obu[0]>>3)&0x0f == 1 // OBU_SEQUENCE_HEADER
}

// h264Keyframe reports whether an H.264 RTP payload (RFC 6184) starts an
// IDR picture or carries a sequence parameter set.
func h264Keyframe(p []byte) bool {
	if len(p) < 2 {
		return false
	}
	switch p[0] & 0x1f {
	case 5, 7:
		return true
	case 24: // STAP-A: aggregated NAL units, each after a 2-byte size
		for i := 1; i+2 < len(p); {
			n := int(p[i])<<8 | int(p[i+1])
			if t := p[i+2] & 0x1f; t == 5 || t == 7 {
				return true
			}
			i += 2 + n
		}
	case 28: // FU-A: the start of a fragmented NAL unit
		return p[1]&0x80 != 0 && p[1]&0x1f == 5
	}
	return false
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
	in.logger.Debug("Keyframe requested from the publisher")
	_ = in.pc.WriteRTCP([]rtcp.Packet{&rtcp.PictureLossIndication{MediaSSRC: ssrc}})
}

func (in *Ingest) watchIdle(timeout time.Duration) {
	tick := time.NewTicker(min(timeout/4, 250*time.Millisecond))
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
