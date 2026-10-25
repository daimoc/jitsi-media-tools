package peer

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/logging"
	"github.com/pion/sdp/v3"
	"github.com/pion/transport/v2/packetio"
	"github.com/pion/webrtc/v3"
)

// Buffer sizes of Pion's default SRTP/SRTCP read streams.
const (
	rtpBufferLimit  = 1000 * 1000
	rtcpBufferLimit = 100 * 1000
)

// errNotOffered is returned by reads on a stream whose SSRC the applied
// offer does not list.
var errNotOffered = errors.New("ssrc not offered")

// settingEngineHook, when set (tests only), adjusts the settings of every
// new peer, e.g. to put it on a virtual network.
var settingEngineHook func(*webrtc.SettingEngine)

// Codecs the peer sends.
var (
	VP8Codec  = webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8, ClockRate: 90000, RTCPFeedback: videoFeedback}
	OpusCodec = webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2, SDPFmtpLine: "minptime=10;useinbandfec=1"}
)

var videoFeedback = []webrtc.RTCPFeedback{{Type: "goog-remb"}, {Type: "ccm", Parameter: "fir"}, {Type: "nack"}, {Type: "nack", Parameter: "pli"}}

// registerCodecs registers what the peer can receive and send: Opus, and
// VP8, VP9 and H.264 video (research R7). AV1 is registered but not
// decoded: a browser may send it before it reads the codec list in our
// presence, and Pion drops for good a track whose first packet has an
// unknown payload type.
func registerCodecs(m *webrtc.MediaEngine) error {
	video := []webrtc.RTPCodecParameters{
		{RTPCodecCapability: VP8Codec, PayloadType: 96},
		{RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP9, ClockRate: 90000, SDPFmtpLine: "profile-id=0", RTCPFeedback: videoFeedback}, PayloadType: 98},
		{RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000, SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f", RTCPFeedback: videoFeedback}, PayloadType: 102},
		{RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000, SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42001f", RTCPFeedback: videoFeedback}, PayloadType: 127},
		{RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000, SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=640032", RTCPFeedback: videoFeedback}, PayloadType: 112},
		{RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeAV1, ClockRate: 90000, RTCPFeedback: videoFeedback}, PayloadType: 45},
	}
	for _, c := range video {
		if err := m.RegisterCodec(c, webrtc.RTPCodecTypeVideo); err != nil {
			return err
		}
	}
	return m.RegisterCodec(webrtc.RTPCodecParameters{RTPCodecCapability: OpusCodec, PayloadType: 111}, webrtc.RTPCodecTypeAudio)
}

// newPeerConnection creates the peer's PeerConnection with its codecs and
// Pion's default interceptors, plus these settings:
//   - incoming streams whose SSRC is neither in the applied offer nor one of
//     the peer's own (RTCP about them) are dropped as they arrive, instead of
//     filling a 1 MB buffer each;
//   - Pion's logs go to the peer's structured logger;
//   - the transport sends RFC 7675 consent checks while it is busy, as
//     browsers do (consent.go).
func newPeerConnection(p *Peer) (*webrtc.PeerConnection, error) {
	m := &webrtc.MediaEngine{}
	if err := registerCodecs(m); err != nil {
		return nil, err
	}
	// Pion's default interceptors, except its TWCC generator: the peer
	// acknowledges every arriving packet itself (transportFeedback), with the
	// same header extension and RTCP feedback negotiated.
	registry := &interceptor.Registry{}
	if err := webrtc.ConfigureNack(m, registry); err != nil {
		return nil, err
	}
	if err := webrtc.ConfigureRTCPReports(registry); err != nil {
		return nil, err
	}
	for _, kind := range []webrtc.RTPCodecType{webrtc.RTPCodecTypeVideo, webrtc.RTPCodecTypeAudio} {
		m.RegisterFeedback(webrtc.RTCPFeedback{Type: webrtc.TypeRTCPFBTransportCC}, kind)
		if err := m.RegisterHeaderExtension(webrtc.RTPHeaderExtensionCapability{URI: sdp.TransportCCURI}, kind); err != nil {
			return nil, err
		}
	}

	var se webrtc.SettingEngine
	se.LoggerFactory = slogLoggerFactory{p.logger}
	c, err := newConsent(p.logger)
	if err != nil {
		return nil, err
	}
	se.SetNet(c.net) // A hook may replace it (virtual network): no consent checks then
	se.BufferFactory = func(packetType packetio.BufferPacketType, ssrc uint32) io.ReadWriteCloser {
		limit := rtpBufferLimit
		if packetType == packetio.RTCPBufferPacket {
			limit = rtcpBufferLimit
		}
		buf := packetio.NewBuffer()
		buf.SetLimitSize(limit)
		return &gatedBuffer{peer: p, ssrc: ssrc, rtp: packetType == packetio.RTPBufferPacket, buf: buf}
	}
	if settingEngineHook != nil {
		settingEngineHook(&se)
	}
	api := webrtc.NewAPI(webrtc.WithMediaEngine(m), webrtc.WithInterceptorRegistry(registry), webrtc.WithSettingEngine(se))
	return c.start(api.NewPeerConnection(webrtc.Configuration{}))
}

// gatedBuffer is an SRTP/SRTCP read buffer that drops what it is given while
// its SSRC is not allowed, and fails reads at once (so that Pion's probe of
// an unknown stream ends). Once allowed, it is a normal buffer. RTP packets
// are recorded for transport feedback before the gate.
type gatedBuffer struct {
	peer *Peer
	ssrc uint32
	rtp  bool // RTP (not RTCP) buffer
	buf  *packetio.Buffer
}

func (b *gatedBuffer) Write(pkt []byte) (int, error) {
	if b.rtp {
		b.peer.feedback.record(pkt, time.Now()) // Every arriving packet, kept or not
	}
	if !b.peer.allowedSSRC(b.ssrc) {
		return len(pkt), nil
	}
	return b.buf.Write(pkt)
}

func (b *gatedBuffer) Read(pkt []byte) (int, error) {
	if !b.peer.allowedSSRC(b.ssrc) {
		return 0, errNotOffered
	}
	return b.buf.Read(pkt)
}

func (b *gatedBuffer) Close() error {
	return b.buf.Close()
}

// slogLoggerFactory routes Pion's logs to a structured logger: errors as
// warnings, warnings as debug, the rest dropped. The error Pion logs for
// every stream it was not offered is expected and goes to debug.
type slogLoggerFactory struct {
	logger *slog.Logger
}

func (f slogLoggerFactory) NewLogger(scope string) logging.LeveledLogger {
	return slogLogger{f.logger.With(slog.String("pion", scope))}
}

type slogLogger struct {
	logger *slog.Logger
}

func (l slogLogger) Trace(string)                  {}
func (l slogLogger) Tracef(string, ...interface{}) {}
func (l slogLogger) Debug(string)                  {}
func (l slogLogger) Debugf(string, ...interface{}) {}
func (l slogLogger) Info(string)                   {}
func (l slogLogger) Infof(string, ...interface{})  {}
func (l slogLogger) Warn(msg string)               { l.logger.Debug(msg) }
func (l slogLogger) Warnf(format string, args ...interface{}) {
	l.logger.Debug(fmt.Sprintf(format, args...))
}
func (l slogLogger) Error(msg string) { l.error(msg) }
func (l slogLogger) Errorf(format string, args ...interface{}) {
	l.error(fmt.Sprintf(format, args...))
}

func (l slogLogger) error(msg string) {
	if strings.HasPrefix(msg, "Incoming unhandled RTP ssrc") {
		l.logger.Debug(msg)
		return
	}
	l.logger.Warn(msg)
}
