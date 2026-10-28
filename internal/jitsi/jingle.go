package jitsi

import (
	"context"
	"encoding/xml"
	"fmt"
	"log/slog"
	"math/rand"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pion/webrtc/v3"
	"mellium.im/xmpp/stanza"

	"github.com/daimoc/jitsi-media-tools/internal/signaling"
)

// SessionState represents the state of a Jingle session.
type SessionState int

const (
	SessionStatePending SessionState = iota
	SessionStateActive
	SessionStateEnded
)

func (s SessionState) String() string {
	switch s {
	case SessionStatePending:
		return "PENDING"
	case SessionStateActive:
		return "ACTIVE"
	case SessionStateEnded:
		return "ENDED"
	default:
		return "UNKNOWN"
	}
}

// acceptTimeout bounds how long session-accept waits for the focus' reply.
const acceptTimeout = 10 * time.Second

// JingleSession represents a single media session negotiation state.
// All fields are guarded by SignalingClient.mu.
type JingleSession struct {
	ID        string
	PeerJID   string // The focus JID
	State     SessionState
	LocalSDP  *webrtc.SessionDescription
	RemoteSDP *webrtc.SessionDescription

	// Local ICE credentials and first content name, used for transport-info.
	localUfrag, localPwd string
	firstContent         string
	// Local candidates trickled before session-accept was sent.
	pendingCandidates []localCandidate

	// Remote sources (one SDP section each) and the serialization of local
	// renegotiations when they change.
	sources *sessionSources
	renegMu sync.Mutex // Held while a renegotiation runs; not guarded by mu

	// Bridge channel, when the offer has an SCTP content (set at creation).
	bridge *bridgeChannel
}

type localCandidate struct {
	mid, candidate string
}

// JingleIQWrapper wraps Jingle payload in an IQ stanza for sending.
type JingleIQWrapper struct {
	XMLName xml.Name `xml:"iq"`
	Type    string   `xml:"type,attr"`
	To      string   `xml:"to,attr"`
	ID      string   `xml:"id,attr"`
	Jingle  *JingleIQ
}

func newIQID() string {
	return "jingle-" + strconv.FormatUint(rand.Uint64(), 36)
}

// HandleSessionInitiate validates a session-initiate request and starts the
// negotiation in the background. A returned error means the offer was
// rejected and no session was created; the caller replies with an IQ error.
func (s *SignalingClient) HandleSessionInitiate(fromJID string, jingle *JingleIQ) error {
	s.logger.Info("Handling session-initiate", slog.String("sid", jingle.SID), slog.String("initiator", jingle.Initiator))

	// With two participants, Jitsi clients also offer a direct (P2P) session.
	// A bot only negotiates with the bridge through the focus: the P2P offer
	// is acknowledged and declined, and the client falls back to the bridge.
	if !strings.EqualFold(fromJID, s.roomJID()+"/focus") {
		s.logger.Info("Declining session-initiate not sent by the focus", slog.String("sid", jingle.SID), slog.String("from", fromJID))
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			ctx, cancel := context.WithTimeout(s.closedCtx, acceptTimeout)
			defer cancel()
			if err := s.sendTerminate(ctx, fromJID, jingle.SID, "decline"); err != nil {
				s.logger.Warn("Failed to decline session", slog.String("sid", jingle.SID), slog.String("error", err.Error()))
			}
		}()
		return nil
	}

	s.mu.Lock()
	limit, height := 0, 0
	var layers []int
	if s.handler != nil {
		limit = s.handler.ReceiveLimit()
		height = s.handler.RequestedVideoHeight()
		layers = s.handler.VideoLayerHeights()
	}
	s.mu.Unlock()
	sources := newSessionSources(jingle, limit, height)
	sources.layers = layers
	offer, err := sources.offer()
	if err != nil {
		return fmt.Errorf("invalid session-initiate: %w", err)
	}

	s.mu.Lock()
	if _, exists := s.sessions[jingle.SID]; exists {
		s.mu.Unlock()
		return fmt.Errorf("session already exists: %s", jingle.SID)
	}
	if s.handler == nil {
		s.mu.Unlock()
		return fmt.Errorf("no signaling handler registered")
	}
	session := &JingleSession{
		ID:        jingle.SID,
		PeerJID:   fromJID,
		State:     SessionStatePending,
		RemoteSDP: offer,
		sources:   sources,
	}
	if offersSCTP(jingle) && !s.config.NoBridgeChannel {
		session.bridge = newBridgeChannel(s.logger.With(slog.String("sid", jingle.SID)))
		session.bridge.onSpeaker = s.onDominantSpeaker
		session.bridge.policy = s.policy
	}
	s.sessions[jingle.SID] = session
	handler := s.handler
	remote := sources.remoteSources()
	s.mu.Unlock()
	notifySources(handler, remote)

	s.logger.Debug("Parsed remote SDP", slog.String("sdp", offer.SDP))
	switch {
	case s.config.NoBridgeChannel:
		s.logger.Info("Bridge channel disabled by configuration: video not requested, receive cap enforced by the bot only", slog.String("sid", jingle.SID))
	case session.bridge == nil:
		s.logger.Info("Bridge channel not offered: video not requested, receive cap enforced by the bot only", slog.String("sid", jingle.SID))
	}

	// Gathering may take a while: negotiate outside the stanza handler so
	// that the IQ is acknowledged immediately and the stream keeps flowing.
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.negotiate(session, offer.SDP, handler)
	}()
	return nil
}

// HandleSourceChange applies a source-add or source-remove from the focus
// (participants starting or stopping a stream). Once the session is
// accepted, the media side is renegotiated locally with the updated offer;
// Jicofo only expects the IQ result, as with lib-jitsi-meet. Changes received
// while the session is pending are applied after the accept.
func (s *SignalingClient) HandleSourceChange(fromJID string, jingle *JingleIQ) error {
	if !strings.EqualFold(fromJID, s.roomJID()+"/focus") {
		return fmt.Errorf("%s not sent by the focus", jingle.Action)
	}
	s.mu.Lock()
	session, ok := s.sessions[jingle.SID]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("session not found: %s", jingle.SID)
	}
	var n int
	if jingle.Action == "source-add" {
		n = session.sources.add(jingle)
	} else {
		n = session.sources.remove(jingle)
	}
	active := session.State == SessionStateActive
	s.mu.Unlock()

	s.logger.Debug("Remote sources changed", slog.String("sid", jingle.SID), slog.String("action", jingle.Action), slog.Int("streams", n))
	if n > 0 && active {
		s.startRenegotiation(jingle.SID)
	}
	return nil
}

// removeOwnerSources drops the sources of an occupant (by nick) who left the
// room from every session, and renegotiates the sessions that change.
func (s *SignalingClient) removeOwnerSources(nick string) {
	if nick == "" {
		return
	}
	var changed []string
	s.mu.Lock()
	for sid, session := range s.sessions {
		if session.sources.removeOwner(nick) > 0 && session.State == SessionStateActive {
			changed = append(changed, sid)
		}
	}
	s.mu.Unlock()
	for _, sid := range changed {
		s.logger.Debug("Remote sources removed with their owner", slog.String("sid", sid), slog.String("owner", nick))
		s.startRenegotiation(sid)
	}
}

// startRenegotiation renegotiates the session in the background.
func (s *SignalingClient) startRenegotiation(sid string) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.renegotiate(sid)
	}()
}

// renegotiate hands the current remote description to the media side.
// Renegotiations of a session are serialized; each one renders the latest
// state, so a burst of source changes may be applied at once.
func (s *SignalingClient) renegotiate(sid string) {
	s.mu.Lock()
	session, ok := s.sessions[sid]
	handler := s.handler
	s.mu.Unlock()
	if !ok || handler == nil {
		return
	}

	session.renegMu.Lock()
	defer session.renegMu.Unlock()

	s.mu.Lock()
	if !session.sources.changed || session.State != SessionStateActive {
		s.mu.Unlock()
		return // Already applied by a previous renegotiation, or ended
	}
	offer, err := session.sources.offer()
	if err == nil {
		session.RemoteSDP = offer
	}
	remote := session.sources.remoteSources()
	s.mu.Unlock()
	if err != nil {
		s.logger.Error("Failed to render the updated offer", slog.String("sid", sid), slog.String("error", err.Error()))
		return
	}
	notifySources(handler, remote) // Before the tracks of new sources can start

	s.logger.Debug("Renegotiating", slog.String("sid", sid), slog.String("sdp", offer.SDP))
	if _, err := handler.Renegotiate(offer.SDP); err != nil {
		s.logger.Warn("Renegotiation failed", slog.String("sid", sid), slog.String("error", err.Error()))
		return
	}
	s.updateBridge(session)
}

// notifySources tells a handler that follows the room the remote sources
// about to be negotiated.
func notifySources(handler signaling.SignalingHandler, remote []signaling.RemoteSource) {
	if o, ok := handler.(signaling.RoomObserver); ok {
		o.OnRemoteSources(remote)
	}
}

// onDominantSpeaker forwards the bridge's dominant speaker to the handler.
func (s *SignalingClient) onDominantSpeaker(id string, previous []string) {
	s.mu.RLock()
	handler := s.handler
	s.mu.RUnlock()
	if o, ok := handler.(signaling.RoomObserver); ok {
		o.OnDominantSpeaker(id, previous)
	}
}

// SetReceivePolicy sets which video the bridge forwards, for the current
// sessions and the next ones. Nil restores the default (every negotiated
// video at the handler's height).
func (s *SignalingClient) SetReceivePolicy(p *signaling.ReceivePolicy) {
	s.mu.Lock()
	s.policy = p
	var bridges []*bridgeChannel
	for _, session := range s.sessions {
		if session.bridge != nil {
			bridges = append(bridges, session.bridge)
		}
	}
	s.mu.Unlock()
	for _, b := range bridges {
		b.setPolicy(p)
	}
}

// SendEndpointStats sends the participant's connection statistics on the
// bridge channels of its sessions.
func (s *SignalingClient) SendEndpointStats(st signaling.EndpointStats) {
	s.mu.Lock()
	var bridges []*bridgeChannel
	for _, session := range s.sessions {
		if session.bridge != nil {
			bridges = append(bridges, session.bridge)
		}
	}
	s.mu.Unlock()
	for _, b := range bridges {
		b.sendStats(st)
	}
}

// updateBridge sends the session's current receive preferences on its
// bridge channel. Callers hold session.renegMu, so that updates follow the
// order of the applied offers.
func (s *SignalingClient) updateBridge(session *JingleSession) {
	if session.bridge == nil {
		return
	}
	s.mu.Lock()
	prefs := session.sources.receivePrefs()
	s.mu.Unlock()
	session.bridge.update(prefs)
}

// Bridge channel opened by Jitsi clients when the offer includes SCTP
// (lib-jitsi-meet BridgeChannel): the bridge logs an error and cannot send its
// control messages to endpoints that do not open it.
const (
	bridgeChannelLabel    = "JVB data channel"
	bridgeChannelProtocol = "http://jitsi.org/protocols/colibri"
)

// offersSCTP reports whether the offer includes an SCTP data section.
func offersSCTP(j *JingleIQ) bool {
	for _, c := range j.Content {
		if c.Transport != nil && c.Transport.SctpMap != nil {
			return true
		}
	}
	return false
}

// negotiate obtains the answer from the media side and sends session-accept,
// or terminates the session if that fails.
func (s *SignalingClient) negotiate(session *JingleSession, offer string, handler signaling.SignalingHandler) {
	sid := session.ID
	ctx, cancel := context.WithTimeout(s.closedCtx, acceptTimeout)
	defer cancel()

	answerSDP, err := handler.OnOffer(offer, func(mid, candidate string) {
		s.trickleLocalCandidate(sid, mid, candidate)
	})
	if err != nil {
		s.logger.Error("Handler failed to process offer", slog.String("sid", sid), slog.String("error", err.Error()))
		s.SendSessionTerminate(ctx, sid, "failed-application")
		return
	}

	if session.bridge != nil {
		dc, err := handler.OpenDataChannel(bridgeChannelLabel, bridgeChannelProtocol, session.bridge)
		if err != nil {
			s.logger.Warn("Failed to open the bridge channel", slog.String("sid", sid), slog.String("error", err.Error()))
		} else {
			session.bridge.setChannel(dc)
			session.renegMu.Lock()
			s.updateBridge(session)
			session.renegMu.Unlock()
		}
	}

	answer := &webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: answerSDP}
	if err := s.SendSessionAccept(ctx, sid, answer); err != nil {
		s.logger.Error("Failed to send session-accept", slog.String("sid", sid), slog.String("error", err.Error()))
		s.SendSessionTerminate(ctx, sid, "failed-application")
	}
}

// SendSessionAccept sends a session-accept Jingle stanza with the local SDP
// and waits for the focus to acknowledge it.
func (s *SignalingClient) SendSessionAccept(ctx context.Context, sid string, localSDP *webrtc.SessionDescription) error {
	s.mu.Lock()
	session, exists := s.sessions[sid]
	s.mu.Unlock()
	if !exists {
		return fmt.Errorf("session not found: %s", sid)
	}

	jingle, err := convertSDPToJingle(localSDP, sid, "session-accept", s.localJID(), session.sources.isContent)
	if err != nil {
		return fmt.Errorf("failed to convert SDP to Jingle: %w", err)
	}
	s.annotateLocalSources(jingle)

	s.mu.Lock()
	session.LocalSDP = localSDP
	if len(jingle.Content) > 0 {
		session.firstContent = jingle.Content[0].Name
		if tr := jingle.Content[0].Transport; tr != nil {
			session.localUfrag, session.localPwd = tr.Ufrag, tr.Pwd
		}
	}
	peerJID := session.PeerJID
	s.mu.Unlock()

	if err := s.sendIQ(ctx, JingleIQWrapper{Type: "set", To: peerJID, ID: newIQID(), Jingle: jingle}); err != nil {
		return err
	}

	s.mu.Lock()
	session.State = SessionStateActive
	pending := session.pendingCandidates
	session.pendingCandidates = nil
	changed := session.sources.changed
	s.mu.Unlock()
	s.logger.Info("Jingle session accepted", slog.String("sid", sid))
	if changed {
		s.startRenegotiation(sid) // Sources changed while the session was pending
	}

	for _, c := range pending {
		if err := s.SendTransportInfo(ctx, sid, c.mid, c.candidate); err != nil {
			s.logger.Warn("Failed to send transport-info", slog.String("sid", sid), slog.String("error", err.Error()))
		}
	}
	return nil
}

// localSourceName is the name lib-jitsi-meet gives a participant's first
// source of a kind: "<endpoint>-a0" / "<endpoint>-v0", the endpoint being the
// occupant nick.
func localSourceName(nick, kind string) string {
	return nick + "-" + kind[:1] + "0"
}

// annotateLocalSources adds to the bot's own sources what Jicofo and other
// clients expect from a participant (source-name signaling): the source
// name, the owner occupant and, for video, the video type.
func (s *SignalingClient) annotateLocalSources(jingle *JingleIQ) {
	nick := s.nickname()
	owner := s.roomJID() + "/" + nick
	for i := range jingle.Content {
		desc := jingle.Content[i].Description
		if desc == nil || (desc.Media != "audio" && desc.Media != "video") {
			continue
		}
		for j := range desc.Sources {
			src := &desc.Sources[j]
			src.Name = localSourceName(nick, desc.Media)
			src.SSRCInfo = &SSRCInfo{Owner: owner}
			if desc.Media == "video" {
				src.VideoType = "camera"
			}
		}
	}
}

// sendIQ sends an IQ and returns the error carried by the reply, if any.
func (s *SignalingClient) sendIQ(ctx context.Context, iq JingleIQWrapper) error {
	sess := s.currentSession()
	if sess == nil {
		return errClientClosed
	}
	resp, err := sess.EncodeIQ(ctx, iq)
	if err != nil {
		return err
	}
	defer resp.Close()
	tok, err := resp.Token()
	if err != nil {
		return err
	}
	start, ok := tok.(xml.StartElement)
	if !ok {
		return fmt.Errorf("expected IQ reply, got %T", tok)
	}
	_, err = stanza.UnmarshalIQError(resp, start)
	return err
}

// trickleLocalCandidate sends a local candidate found after the answer, or
// queues it until session-accept has been sent.
func (s *SignalingClient) trickleLocalCandidate(sid, mid, candidate string) {
	s.mu.Lock()
	session, exists := s.sessions[sid]
	if !exists {
		s.mu.Unlock()
		return
	}
	if session.State == SessionStatePending {
		session.pendingCandidates = append(session.pendingCandidates, localCandidate{mid, candidate})
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()

	ctx, cancel := context.WithTimeout(s.closedCtx, acceptTimeout)
	defer cancel()
	if err := s.SendTransportInfo(ctx, sid, mid, candidate); err != nil {
		s.logger.Warn("Failed to send transport-info", slog.String("sid", sid), slog.String("error", err.Error()))
	}
}

// HandleTransportInfo processes a transport-info Jingle request (trickle ICE).
func (s *SignalingClient) HandleTransportInfo(fromJID string, jingle *JingleIQ) error {
	s.logger.Debug("Handling transport-info", slog.String("sid", jingle.SID))

	s.mu.RLock()
	_, exists := s.sessions[jingle.SID]
	handler := s.handler
	s.mu.RUnlock()
	if !exists {
		return fmt.Errorf("unknown session: %s", jingle.SID)
	}

	for _, content := range jingle.Content {
		if content.Transport == nil {
			continue
		}
		for _, cand := range content.Transport.Candidates {
			if err := handler.OnICECandidate(content.Name, candidateToSDP(cand)); err != nil {
				s.logger.Warn("Failed to handle ICE candidate", slog.String("error", err.Error()))
			}
		}
	}
	return nil
}

// handleSessionTerminate processes a session-terminate from the focus.
func (s *SignalingClient) handleSessionTerminate(jingle *JingleIQ) {
	reason := ""
	if jingle.Reason != nil {
		reason = jingle.Reason.Condition
	}
	s.logger.Info("Jingle session terminated by remote", slog.String("sid", jingle.SID), slog.String("reason", reason))

	s.mu.Lock()
	session, ok := s.sessions[jingle.SID]
	if ok {
		session.State = SessionStateEnded
		delete(s.sessions, jingle.SID)
	}
	s.mu.Unlock()
	if ok && session.bridge != nil {
		session.bridge.stop()
	}
}

// SendSessionTerminate sends a session-terminate Jingle stanza with the given
// reason condition (e.g. "success", "failed-application").
func (s *SignalingClient) SendSessionTerminate(ctx context.Context, sid, reason string) error {
	s.mu.Lock()
	session, exists := s.sessions[sid]
	if exists {
		session.State = SessionStateEnded
		delete(s.sessions, sid)
	}
	s.mu.Unlock()
	if !exists {
		return nil // Already gone
	}
	if session.bridge != nil {
		session.bridge.stop()
	}

	return s.sendTerminate(ctx, session.PeerJID, sid, reason)
}

// sendTerminate sends a session-terminate to peerJID without waiting for the
// reply: the stream is usually closed right after.
func (s *SignalingClient) sendTerminate(ctx context.Context, peerJID, sid, reason string) error {
	sess := s.currentSession()
	if sess == nil {
		return errClientClosed
	}
	return sess.Encode(ctx, JingleIQWrapper{
		Type: "set",
		To:   peerJID,
		ID:   newIQID(),
		Jingle: &JingleIQ{
			Action: "session-terminate",
			SID:    sid,
			Reason: &JingleReason{Condition: reason},
		},
	})
}

// TerminateSessions terminates all active sessions.
func (s *SignalingClient) TerminateSessions(ctx context.Context) {
	s.mu.Lock()
	var sids []string
	for sid := range s.sessions {
		sids = append(sids, sid)
	}
	s.mu.Unlock()

	for _, sid := range sids {
		if err := s.SendSessionTerminate(ctx, sid, "success"); err != nil {
			s.logger.Error("Failed to terminate session", slog.String("sid", sid), slog.String("error", err.Error()))
		}
	}
}

// SendTransportInfo sends a transport-info Jingle stanza carrying one local
// ICE candidate ("candidate:..." SDP attribute value) for content mid. An
// empty mid targets the first (bundled) content.
func (s *SignalingClient) SendTransportInfo(ctx context.Context, sid, mid, candidate string) error {
	cand, err := candidateFromSDP(candidate)
	if err != nil {
		return err
	}

	s.mu.RLock()
	session, exists := s.sessions[sid]
	var ufrag, pwd, peerJID string
	if exists {
		ufrag, pwd, peerJID = session.localUfrag, session.localPwd, session.PeerJID
		mid = firstNonEmpty(mid, session.firstContent)
	}
	s.mu.RUnlock()
	if !exists {
		return fmt.Errorf("session not found: %s", sid)
	}

	sess := s.currentSession()
	if sess == nil {
		return errClientClosed
	}
	return sess.Encode(ctx, JingleIQWrapper{
		Type: "set",
		To:   peerJID,
		ID:   newIQID(),
		Jingle: &JingleIQ{
			Action: "transport-info",
			SID:    sid,
			Content: []JingleContent{{
				Creator:   "responder",
				Name:      mid,
				Transport: &JingleTransport{Ufrag: ufrag, Pwd: pwd, Candidates: []JingleCandidate{cand}},
			}},
		},
	})
}
