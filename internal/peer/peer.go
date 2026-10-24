// Package peer is the session's WebRTC side: the PeerConnection that answers
// the bridge's offer, sends the local tracks and receives the remote ones.
// It implements signaling.SignalingHandler and knows nothing of Jitsi.
package peer

import (
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"github.com/pion/sdp/v3"
	"github.com/pion/webrtc/v3"

	"github.com/daimoc/jitsi-media-tools/internal/signaling"
)

// Media kinds.
const (
	KindAudio = "audio"
	KindVideo = "video"
)

// defaultGatherTimeout bounds how long OnOffer waits for ICE gathering before
// answering; candidates found later are trickled.
const defaultGatherTimeout = time.Second

// TransportState is the state of the media transport (ICE + DTLS).
type TransportState int

const (
	TransportNew TransportState = iota
	TransportConnected
	TransportFailed // Also reported when signaling is lost
)

// Config is what a peer sends and asks for.
type Config struct {
	Logger *slog.Logger
	// Kinds are the media kinds sent ("audio", "video"); none for a
	// receive-only peer. Fixed for the peer's life.
	Kinds []string
	// VideoHeight is the video height asked for each received video when the
	// signaling side has no better policy; 0 means 180.
	VideoHeight int
	// VideoWidth and VideoHeightSent are the size of the sent video.
	VideoWidth, VideoHeightSent int
}

// RemoteTrack is a received remote stream.
type RemoteTrack struct {
	Kind     string // "audio" or "video"
	MimeType string // e.g. "video/VP8"
	SSRC     uint32
	track    *webrtc.TrackRemote
}

// ReadRTP reads the next packet; it fails once the track or the connection
// ends.
func (t *RemoteTrack) ReadRTP() (*rtp.Packet, error) {
	pkt, _, err := t.track.ReadRTP()
	return pkt, err
}

// Peer encapsulates a WebRTC peer connection. It implements
// signaling.SignalingHandler.
type Peer struct {
	pc     *webrtc.PeerConnection
	logger *slog.Logger
	cfg    Config

	gatherTimeout time.Duration
	localTracks   map[string]*webrtc.TrackLocalStaticRTP // By kind
	senders       map[string]*webrtc.RTPSender           // By kind

	cbMu      sync.Mutex
	onTrack   func(*RemoteTrack)
	onState   func(TransportState, error)
	onPLI     func()
	connected bool // Guarded by cbMu: TransportConnected reported

	candMu        sync.Mutex
	remoteSet     bool                      // Guarded by candMu
	pendingRemote []webrtc.ICECandidateInit // Remote candidates received before the offer, guarded by candMu

	negMu sync.Mutex // Serializes OnOffer and Renegotiate

	feedback *transportFeedback // Transport-wide congestion control feedback

	// SSRCs whose incoming packets are kept: the applied offer's and the
	// peer's own (RTCP about them). Read on every incoming packet.
	allowed    atomic.Pointer[map[uint32]bool]
	localSSRCs []uint32 // Set before the first offer

	trickleMu sync.Mutex
	answered  bool                  // Guarded by trickleMu: answer SDP handed to signaling
	trickle   signaling.TrickleFunc // Guarded by trickleMu

	closeOnce sync.Once
}

var _ signaling.SignalingHandler = (*Peer)(nil)

// New creates a peer with one send track per kind of cfg.Kinds: VP8 video
// and Opus audio.
func New(cfg Config) (*Peer, error) {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.VideoHeight <= 0 {
		cfg.VideoHeight = 180
	}
	p := &Peer{
		cfg:           cfg,
		logger:        cfg.Logger,
		gatherTimeout: defaultGatherTimeout,
		feedback:      newTransportFeedback(),
	}
	p.allowed.Store(&map[uint32]bool{})
	pc, err := newPeerConnection(p)
	if err != nil {
		return nil, fmt.Errorf("failed to create peer connection: %w", err)
	}
	p.pc = pc
	if err := p.addLocalTracks(); err != nil {
		pc.Close()
		return nil, err
	}
	pc.OnConnectionStateChange(p.onConnectionStateChange)
	pc.OnICECandidate(p.onLocalCandidate)
	pc.OnTrack(p.handleTrack)
	return p, nil
}

// addLocalTracks adds one track per sent kind before any offer, so that the
// answer reuses their transceivers for the offered sections (sendrecv).
func (p *Peer) addLocalTracks() error {
	p.localTracks = map[string]*webrtc.TrackLocalStaticRTP{}
	p.senders = map[string]*webrtc.RTPSender{}
	// One stream per peer, unique in the room: Jicofo refuses an MSID that
	// another participant already uses.
	streamID := "jitsi-media-tools-" + uuid.NewString()
	for _, kind := range p.cfg.Kinds {
		codec := OpusCodec
		if kind == KindVideo {
			codec = VP8Codec
		}
		track, err := webrtc.NewTrackLocalStaticRTP(codec, kind, streamID)
		if err != nil {
			return fmt.Errorf("failed to create %s track: %w", kind, err)
		}
		sender, err := p.pc.AddTrack(track)
		if err != nil {
			return fmt.Errorf("failed to add %s track: %w", kind, err)
		}
		p.localTracks[kind] = track
		p.senders[kind] = sender
		for _, enc := range sender.GetParameters().Encodings {
			p.localSSRCs = append(p.localSSRCs, uint32(enc.SSRC))
		}
		go p.readRTCP(kind, sender)
	}
	return nil
}

// LocalTrack returns the sending track of kind, or nil.
func (p *Peer) LocalTrack(kind string) *webrtc.TrackLocalStaticRTP {
	return p.localTracks[kind]
}

// OnTrack registers the function called for each remote stream received.
// Set it before the first offer.
func (p *Peer) OnTrack(f func(*RemoteTrack)) {
	p.cbMu.Lock()
	p.onTrack = f
	p.cbMu.Unlock()
}

// OnTransportState registers the function called when the transport
// connects or fails (err says why).
func (p *Peer) OnTransportState(f func(TransportState, error)) {
	p.cbMu.Lock()
	p.onState = f
	p.cbMu.Unlock()
}

// OnKeyframeRequest registers the function called when the remote side asks
// for a keyframe of the sent video (PLI or FIR).
func (p *Peer) OnKeyframeRequest(f func()) {
	p.cbMu.Lock()
	p.onPLI = f
	p.cbMu.Unlock()
}

// readRTCP reads the RTCP the remote side sends about a local track (Pion's
// interceptors, e.g. the NACK responder, only see packets that are read).
func (p *Peer) readRTCP(kind string, sender *webrtc.RTPSender) {
	for {
		packets, _, err := sender.ReadRTCP()
		if err != nil {
			return // Connection closed
		}
		if kind != KindVideo {
			continue
		}
		for _, pkt := range packets {
			switch pkt.(type) {
			case *rtcp.PictureLossIndication, *rtcp.FullIntraRequest:
				p.cbMu.Lock()
				f := p.onPLI
				p.cbMu.Unlock()
				if f != nil {
					f()
				}
			}
		}
	}
}

func (p *Peer) handleTrack(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
	t := &RemoteTrack{
		Kind:     track.Kind().String(),
		MimeType: track.Codec().MimeType,
		SSRC:     uint32(track.SSRC()),
		track:    track,
	}
	p.logger.Debug("Receiving remote stream", slog.String("kind", t.Kind), slog.String("codec", t.MimeType), slog.Uint64("ssrc", uint64(t.SSRC)))
	p.cbMu.Lock()
	f := p.onTrack
	p.cbMu.Unlock()
	if f != nil {
		f(t)
	}
}

// RequestKeyframe sends a picture loss indication about a received stream.
func (p *Peer) RequestKeyframe(ssrc uint32) {
	_ = p.pc.WriteRTCP([]rtcp.Packet{&rtcp.PictureLossIndication{MediaSSRC: ssrc}})
}

// recordOffered allows the SSRCs of an offer about to be applied (packets of
// other SSRCs are dropped) and sets up transport feedback.
func (p *Peer) recordOffered(offer string) {
	parsed := &sdp.SessionDescription{}
	if err := parsed.Unmarshal([]byte(offer)); err != nil {
		return
	}
	allowed := map[uint32]bool{}
	for _, ssrc := range p.localSSRCs {
		allowed[ssrc] = true
	}
	for _, md := range parsed.MediaDescriptions {
		if _, ok := md.Attribute("inactive"); ok {
			continue
		}
		if _, ok := md.Attribute("recvonly"); ok {
			continue
		}
		for _, a := range md.Attributes {
			if a.Key != "ssrc" {
				continue
			}
			id, _, _ := strings.Cut(a.Value, " ")
			if ssrc, err := strconv.ParseUint(id, 10, 32); err == nil {
				allowed[uint32(ssrc)] = true
			}
		}
	}
	p.allowed.Store(&allowed)
	p.feedback.setExtension(parsed)
	if p.feedback.extID.Load() != 0 && p.pc != nil {
		p.feedback.run(p.pc.WriteRTCP)
	}
}

// allowedSSRC reports whether incoming packets of ssrc are kept.
func (p *Peer) allowedSSRC(ssrc uint32) bool {
	return (*p.allowed.Load())[ssrc]
}

// logUnusedTracks reports sent kinds the offer has no section for.
func (p *Peer) logUnusedTracks() {
	for kind, sender := range p.senders {
		for _, tr := range p.pc.GetTransceivers() {
			if tr.Sender() == sender && tr.Mid() == "" {
				p.logger.Warn("Offer has no section for a sent media kind; not sending it", slog.String("kind", kind))
			}
		}
	}
}

// Close releases the peer connection.
func (p *Peer) Close() error {
	var err error
	p.closeOnce.Do(func() {
		p.feedback.close()
		err = p.pc.Close()
	})
	return err
}

// OnOffer handles an incoming SDP offer and returns the SDP answer.
func (p *Peer) OnOffer(offer string, trickle signaling.TrickleFunc) (string, error) {
	p.negMu.Lock()
	defer p.negMu.Unlock()
	p.recordOffered(offer)
	if err := p.pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: offer}); err != nil {
		return "", fmt.Errorf("failed to set remote description: %w", err)
	}

	p.candMu.Lock()
	p.remoteSet = true
	pending := p.pendingRemote
	p.pendingRemote = nil
	p.candMu.Unlock()
	for _, c := range pending {
		if err := p.pc.AddICECandidate(c); err != nil {
			p.logger.Warn("Failed to apply buffered remote candidate", slog.String("error", err.Error()))
		}
	}

	answer, err := p.pc.CreateAnswer(nil)
	if err != nil {
		return "", fmt.Errorf("failed to create answer: %w", err)
	}
	p.logUnusedTracks()

	gathered := webrtc.GatheringCompletePromise(p.pc)
	if err := p.pc.SetLocalDescription(answer); err != nil {
		return "", fmt.Errorf("failed to set local description: %w", err)
	}
	select {
	case <-gathered:
	case <-time.After(p.gatherTimeout):
		p.logger.Info("ICE gathering not complete, remaining candidates will be trickled")
	}

	// Snapshot the answer (with every candidate gathered so far) and switch
	// to trickling atomically, so each candidate is either in the SDP or
	// trickled (onLocalCandidate runs after the candidate is gathered).
	p.trickleMu.Lock()
	local := p.pc.LocalDescription()
	p.answered = true
	p.trickle = trickle
	p.trickleMu.Unlock()
	return local.SDP, nil
}

// OpenDataChannel opens a data channel; it is established with the
// transport. Text messages and events go to h.
func (p *Peer) OpenDataChannel(label, protocol string, h signaling.DataChannelHandler) (signaling.DataChannel, error) {
	dc, err := p.pc.CreateDataChannel(label, &webrtc.DataChannelInit{Protocol: &protocol})
	if err != nil {
		return nil, fmt.Errorf("failed to open data channel %q: %w", label, err)
	}
	logger := p.logger.With(slog.String("data_channel", label))
	dc.OnOpen(func() {
		logger.Debug("Data channel open")
		h.OnOpen()
	})
	dc.OnMessage(func(m webrtc.DataChannelMessage) {
		if m.IsString {
			h.OnMessage(string(m.Data))
		}
	})
	dc.OnClose(func() {
		logger.Debug("Data channel closed")
		h.OnClose()
	})
	return dataChannel{dc}, nil
}

// dataChannel implements signaling.DataChannel over Pion's.
type dataChannel struct{ dc *webrtc.DataChannel }

func (c dataChannel) SendText(msg string) error { return c.dc.SendText(msg) }

// LocalMediaKinds returns the media kinds the peer sends.
func (p *Peer) LocalMediaKinds() []string {
	return p.cfg.Kinds
}

// ReceiveLimit implements signaling.SignalingHandler: every remote source is
// negotiated; the bridge bounds what it forwards (research R8).
func (p *Peer) ReceiveLimit() int { return 0 }

// RequestedVideoHeight implements signaling.SignalingHandler.
func (p *Peer) RequestedVideoHeight() int { return p.cfg.VideoHeight }

// VideoLayerHeights implements signaling.SignalingHandler: remote senders'
// layers are the bridge's business.
func (p *Peer) VideoLayerHeights() []int { return nil }

// Renegotiate applies an updated remote offer (remote sources added or
// removed) and returns the answer.
func (p *Peer) Renegotiate(offer string) (string, error) {
	p.negMu.Lock()
	defer p.negMu.Unlock()
	p.recordOffered(offer)
	if err := p.pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: offer}); err != nil {
		return "", fmt.Errorf("failed to set remote description: %w", err)
	}
	answer, err := p.pc.CreateAnswer(nil)
	if err != nil {
		return "", fmt.Errorf("failed to create answer: %w", err)
	}
	if err := p.pc.SetLocalDescription(answer); err != nil {
		return "", fmt.Errorf("failed to set local description: %w", err)
	}
	return p.pc.LocalDescription().SDP, nil
}

// onLocalCandidate trickles candidates gathered after the answer was returned.
func (p *Peer) onLocalCandidate(c *webrtc.ICECandidate) {
	if c == nil {
		return // Gathering complete
	}
	p.trickleMu.Lock()
	answered, trickle := p.answered, p.trickle
	p.trickleMu.Unlock()
	if !answered || trickle == nil {
		return // Will be embedded in the answer SDP
	}
	init := c.ToJSON()
	mid := ""
	if init.SDPMid != nil {
		mid = *init.SDPMid
	}
	trickle(mid, init.Candidate)
}

// OnICECandidate handles an incoming ICE candidate. Candidates received
// before the offer are buffered and applied once it is set.
func (p *Peer) OnICECandidate(mid, candidate string) error {
	init := webrtc.ICECandidateInit{Candidate: candidate}
	if mid != "" {
		init.SDPMid = &mid
	}
	p.candMu.Lock()
	if !p.remoteSet {
		p.pendingRemote = append(p.pendingRemote, init)
		p.candMu.Unlock()
		return nil
	}
	p.candMu.Unlock()
	if err := p.pc.AddICECandidate(init); err != nil {
		return fmt.Errorf("failed to add ICE candidate: %w", err)
	}
	return nil
}

// OnSignalingLost reports the loss as a transport failure.
func (p *Peer) OnSignalingLost(err error) {
	p.report(TransportFailed, fmt.Errorf("signaling lost: %w", err))
}

func (p *Peer) onConnectionStateChange(state webrtc.PeerConnectionState) {
	p.logger.Debug("Peer connection state changed", slog.String("transport_state", state.String()))
	switch state {
	case webrtc.PeerConnectionStateConnected:
		p.report(TransportConnected, nil)
	case webrtc.PeerConnectionStateFailed:
		p.report(TransportFailed, errors.New("ICE/DTLS transport failed"))
	}
}

// report calls the transport state callback; connected is reported once
// (recoveries from a transient "disconnected" state are not news).
func (p *Peer) report(s TransportState, err error) {
	p.cbMu.Lock()
	if s == TransportConnected {
		if p.connected {
			p.cbMu.Unlock()
			return
		}
		p.connected = true
	}
	f := p.onState
	p.cbMu.Unlock()
	if f != nil {
		f(s, err)
	}
}

// CreatePublishOffer implements signaling.SignalingHandler: Jitsi carries
// the local tracks on the session it offers, so there is no publish session.
func (p *Peer) CreatePublishOffer(signaling.TrickleFunc) (string, error) {
	return "", errors.New("peer has no publish session")
}

// ApplyPublishAnswer implements signaling.SignalingHandler (unused).
func (p *Peer) ApplyPublishAnswer(string) error {
	return errors.New("peer has no publish session")
}

// OnPublishICECandidate implements signaling.SignalingHandler (unused).
func (p *Peer) OnPublishICECandidate(string, string) error {
	return errors.New("peer has no publish session")
}

// LocalVideoSize implements signaling.SignalingHandler: the size of the sent
// video, (0, 0) without.
func (p *Peer) LocalVideoSize() (width, height int) {
	if !slices.Contains(p.cfg.Kinds, KindVideo) {
		return 0, 0
	}
	return p.cfg.VideoWidth, p.cfg.VideoHeightSent
}
