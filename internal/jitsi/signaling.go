package jitsi

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"mellium.im/sasl"
	"mellium.im/xmlstream"
	"mellium.im/xmpp"
	"mellium.im/xmpp/jid"
	"mellium.im/xmpp/mux"
	"mellium.im/xmpp/ping"
	"mellium.im/xmpp/stanza"
	xmppws "mellium.im/xmpp/websocket"
	ws "nhooyr.io/websocket"

	"github.com/daimoc/jitsi-media-tools/internal/signaling"
)

// Errors reported by the Jitsi signaling client (SC-003). They are wrapped
// with details, so match them with errors.Is.
var (
	ErrServerUnreachable = errors.New("xmpp server unreachable")
	ErrAuthFailed        = errors.New("xmpp authentication failed")
	ErrFocusUnavailable  = errors.New("conference focus unavailable")
	ErrRoomUnavailable   = errors.New("room unavailable")
	ErrKicked            = errors.New("removed from room")
	ErrConnectionLost    = errors.New("xmpp connection lost")

	errClientClosed = errors.New("signaling client closed")
)

// ClientState is the connection state of a SignalingClient.
type ClientState int32

const (
	ClientStateDisconnected ClientState = iota
	ClientStateConnected                // XMPP stream negotiated and authenticated
	ClientStateJoined                   // Self-presence received from the MUC
)

func (s ClientState) String() string {
	switch s {
	case ClientStateDisconnected:
		return "DISCONNECTED"
	case ClientStateConnected:
		return "CONNECTED"
	case ClientStateJoined:
		return "JOINED"
	default:
		return "UNKNOWN"
	}
}

// closeTimeout bounds the graceful part of Disconnect.
const closeTimeout = time.Second

const (
	nsMUC     = "http://jabber.org/protocol/muc"
	nsNick    = "http://jabber.org/protocol/nick"
	nsMUCUser = "http://jabber.org/protocol/muc#user"
	nsFocus   = "http://jitsi.org/protocol/focus"
)

// SignalingClient handles XMPP signaling for one Jitsi participant.
type SignalingClient struct {
	logger *slog.Logger
	config JingleConfig
	token  string       // JWT sent in the WebSocket URL, if any
	state  atomic.Int32 // ClientState

	lifeMu  sync.Mutex
	session *xmpp.Session      // Guarded by lifeMu
	cancel  context.CancelFunc // Ends the connection lifetime, guarded by lifeMu
	closing bool               // Set once by Disconnect or on loss, guarded by lifeMu
	nick    string             // Guarded by lifeMu

	joinOnce sync.Once
	joinDone chan struct{} // Closed when the join succeeds or fails
	joinErr  error         // Written before joinDone is closed

	lostOnce sync.Once
	hostOnce sync.Once   // OnWaitingForHost called
	onLost   func(error) // Called (in its own goroutine) when the connection is lost after joining

	mu       sync.RWMutex
	sessions map[string]*JingleSession  // Guarded by mu
	handler  signaling.SignalingHandler // Guarded by mu
	policy   *signaling.ReceivePolicy   // Guarded by mu; nil: default

	wg          sync.WaitGroup  // Background negotiations
	closedCtx   context.Context // Cancelled by Disconnect: aborts pending connects and negotiations
	closeCancel context.CancelFunc
}

// NewSignalingClient creates a new SignalingClient.
func NewSignalingClient(logger *slog.Logger, config JingleConfig) *SignalingClient {
	closedCtx, closeCancel := context.WithCancel(context.Background())
	return &SignalingClient{
		logger:      logger,
		config:      config,
		joinDone:    make(chan struct{}),
		sessions:    make(map[string]*JingleSession),
		closedCtx:   closedCtx,
		closeCancel: closeCancel,
	}
}

// SetHandler sets the signaling handler.
func (s *SignalingClient) SetHandler(handler signaling.SignalingHandler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handler = handler
}

// SetOnLost registers the callback invoked when the connection is lost after
// the room was joined (stream closed, kicked, keepalive timeout).
func (s *SignalingClient) SetOnLost(f func(error)) {
	s.onLost = f
}

// State returns the client's connection state.
func (s *SignalingClient) State() ClientState {
	return ClientState(s.state.Load())
}

func (s *SignalingClient) currentSession() *xmpp.Session {
	s.lifeMu.Lock()
	defer s.lifeMu.Unlock()
	return s.session
}

func (s *SignalingClient) localJID() string {
	if sess := s.currentSession(); sess != nil {
		return sess.LocalAddr().String()
	}
	return ""
}

func (s *SignalingClient) roomJID() string {
	return s.config.RoomName + "@" + s.config.MUCDomain
}

// nickname returns the occupant nick (set by JoinMUC).
func (s *SignalingClient) nickname() string {
	s.lifeMu.Lock()
	defer s.lifeMu.Unlock()
	return s.nick
}

func (s *SignalingClient) occupantJID() string {
	s.lifeMu.Lock()
	defer s.lifeMu.Unlock()
	return s.roomJID() + "/" + s.nick
}

// Connect establishes the XMPP connection using anonymous authentication
// (with token authentication, Prosody validates the JWT from the URL).
// ctx bounds the connection setup only; the connection lives until
// Disconnect or until it is lost.
func (s *SignalingClient) Connect(ctx context.Context) error {
	wsURL, err := websocketURL(s.config.WebSocketURL, s.config.RoomName, s.token)
	if err != nil {
		return err
	}
	s.logger.Info("Connecting to XMPP server",
		slog.String("websocket_url", redactToken(wsURL)),
		slog.String("xmpp_domain", s.config.XMPPDomain))

	domain, err := jid.Parse(s.config.XMPPDomain)
	if err != nil {
		return fmt.Errorf("invalid xmpp_domain: %w", err)
	}

	wsConn, resp, err := ws.Dial(ctx, wsURL, &ws.DialOptions{
		Subprotocols: []string{"xmpp"},
		HTTPClient:   HTTPClient(s.config.Insecure),
	})
	if err != nil {
		if strings.Contains(err.Error(), "certificate") {
			return fmt.Errorf("%w: %v (use the insecure option for a self-signed certificate)", ErrServerUnreachable, err)
		}
		// Prosody rejects an invalid or expired token at the upgrade.
		if resp != nil && (resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden) {
			return fmt.Errorf("%w: %v", ErrAuthFailed, err)
		}
		return fmt.Errorf("%w: %v", ErrServerUnreachable, err)
	}

	lifeCtx, cancel := context.WithCancel(context.Background())
	nc := newStanzaFramer(ws.NetConn(lifeCtx, wsConn, ws.MessageText))

	session, err := xmpp.NewSession(
		ctx,
		domain.Domain(),
		domain,
		nc,
		xmpp.Secure,
		xmppws.Negotiator(func(_ *xmpp.Session, cfg *xmpp.StreamConfig) xmpp.StreamConfig {
			var c xmpp.StreamConfig
			if cfg != nil {
				c = *cfg
			}
			c.Features = []xmpp.StreamFeature{
				xmpp.SASL("", "", sasl.Anonymous),
				xmpp.BindResource(),
			}
			if s.config.Debug {
				var lastDirection int32 = 0                                                           // 0=init, 1=in, 2=out
				c.TeeIn = &colorWriter{w: os.Stdout, color: ansiRed, last: &lastDirection, dir: 1}    // Recv -> Red
				c.TeeOut = &colorWriter{w: os.Stdout, color: ansiGreen, last: &lastDirection, dir: 2} // Send -> Green
			}
			return c
		}),
	)
	if err != nil {
		cancel()
		nc.Close()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("%w: %v", ErrAuthFailed, err)
	}

	s.lifeMu.Lock()
	if s.closing {
		s.lifeMu.Unlock()
		session.Close()
		cancel()
		return errClientClosed
	}
	s.session = session
	s.cancel = cancel
	s.lifeMu.Unlock()

	s.state.Store(int32(ClientStateConnected))
	s.logger.Info("XMPP connection established", slog.String("jid", session.LocalAddr().String()))

	go s.serve(session)
	go s.keepalive(lifeCtx, session, domain)
	return nil
}

// serve handles the incoming XML stream until it ends.
func (s *SignalingClient) serve(session *xmpp.Session) {
	m := mux.New(
		stanza.NSClient,
		ping.Handle(), // Answer server pings (XEP-0199)
		mux.IQFunc(stanza.SetIQ, xml.Name{Space: nsJingle, Local: "jingle"}, s.handleIQ),
		mux.IQFunc(stanza.GetIQ, xml.Name{Space: nsDiscoInfo, Local: "query"}, s.handleDiscoInfo),
	)
	h := xmpp.HandlerFunc(func(t xmlstream.TokenReadEncoder, start *xml.StartElement) error {
		// Presence is decoded as a whole (MUC status codes, errors) rather
		// than per payload as the mux would do.
		if start.Name.Local == "presence" {
			return s.handlePresence(t, start)
		}
		return m.HandleXMPP(t, start)
	})

	err := session.Serve(h)
	if err == nil || errors.Is(err, io.EOF) {
		err = errors.New("stream closed by server")
	}
	s.lost(fmt.Errorf("%w: %v", ErrConnectionLost, err))
}

// keepalive pings the server periodically (XEP-0199) so idle connections are
// not dropped and dead ones are detected.
func (s *SignalingClient) keepalive(ctx context.Context, session *xmpp.Session, server jid.JID) {
	interval := s.config.keepaliveInterval
	if interval <= 0 {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		pingCtx, cancel := context.WithTimeout(ctx, interval)
		err := ping.Send(pingCtx, session, server)
		cancel()
		if err != nil && ctx.Err() == nil {
			s.lost(fmt.Errorf("%w: keepalive ping failed: %v", ErrConnectionLost, err))
			return
		}
	}
}

// lost handles an unexpected end of the connection or of the room membership.
func (s *SignalingClient) lost(err error) {
	s.lifeMu.Lock()
	closing := s.closing
	s.lifeMu.Unlock()
	if closing {
		return // Disconnect in progress: not a loss
	}

	s.lostOnce.Do(func() {
		s.state.Store(int32(ClientStateDisconnected))
		if s.finishJoin(err) {
			return // Connect is still waiting and reports the error itself
		}
		s.logger.Error("Signaling lost", slog.String("error", err.Error()))
		if s.onLost != nil {
			go s.onLost(err)
		}
	})
}

// finishJoin completes the pending join with err (nil on success). It
// reports false if the join had already completed.
func (s *SignalingClient) finishJoin(err error) bool {
	done := false
	s.joinOnce.Do(func() {
		s.joinErr = err
		close(s.joinDone)
		done = true
	})
	return done
}

// conferenceResponse is the focus' reply to the conference request.
type conferenceResponse struct {
	XMLName  xml.Name `xml:"http://jitsi.org/protocol/focus conference"`
	Ready    string   `xml:"ready,attr"`
	FocusJID string   `xml:"focusjid,attr"`
}

// requestConference asks Jicofo to allocate the conference before joining,
// as lib-jitsi-meet does; Jicofo then invites us with session-initiate.
func (s *SignalingClient) requestConference(ctx context.Context, session *xmpp.Session) error {
	focus, err := jid.Parse(s.config.FocusJID)
	if err != nil {
		return fmt.Errorf("invalid focus_jid: %w", err)
	}
	// <conference xmlns="http://jitsi.org/protocol/focus" room="room@muc" machine-uid="..."/>
	machineUID := strconv.FormatUint(rand.Uint64(), 16)
	req := func() xml.TokenReader {
		return xmlstream.Wrap(nil, xml.StartElement{
			Name: xml.Name{Space: nsFocus, Local: "conference"},
			Attr: []xml.Attr{
				{Name: xml.Name{Local: "room"}, Value: s.roomJID()},
				{Name: xml.Name{Local: "machine-uid"}, Value: machineUID},
			},
		})
	}
	// Jicofo answers ready="false" while it is still allocating the room (for
	// instance for another participant); joining before that is refused by
	// Prosody ("Room creation is restricted"). Retry like lib-jitsi-meet.
	for delay := conferenceRetryMin; ; delay = min(2*delay, conferenceRetryMax) {
		var resp conferenceResponse
		err = session.UnmarshalIQElement(ctx, req(), stanza.IQ{Type: stanza.SetIQ, To: focus}, &resp)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			var se stanza.Error
			if errors.As(err, &se) && se.Condition == stanza.NotAuthorized {
				// Secure domain: a guest may not start the conference; it
				// waits for an authenticated host, asking again like
				// jitsi-meet does (research R12).
				if !s.waitForHost(ctx) {
					return fmt.Errorf("%w: not allowed to start the conference and no host came", ErrAuthFailed)
				}
				continue
			}
			return fmt.Errorf("%w: %v", ErrFocusUnavailable, err)
		}
		if resp.Ready == "true" {
			s.logger.Info("Conference allocated by focus", slog.String("focus", resp.FocusJID))
			return nil
		}
		s.logger.Debug("Focus reported conference not ready, retrying", slog.String("room", s.roomJID()), slog.Duration("retry_in", delay))
		select {
		case <-ctx.Done():
			return fmt.Errorf("%w: conference not ready: %v", ErrFocusUnavailable, ctx.Err())
		case <-time.After(delay):
		}
	}
}

// hostRetry is how often a guest asks again for a conference only a host
// may start.
var hostRetry = 5 * time.Second

// waitForHost tells the handler (once) that the guest waits for a host, then
// waits hostRetry; it reports false when ctx ends first.
func (s *SignalingClient) waitForHost(ctx context.Context) bool {
	s.mu.RLock()
	handler := s.handler
	s.mu.RUnlock()
	w, ok := handler.(signaling.HostWaiter)
	if !ok {
		return false // Nobody to tell: not a guest waiting for a host
	}
	s.hostOnce.Do(func() {
		s.logger.Info("Waiting for a host to start the conference")
		w.OnWaitingForHost()
	})
	select {
	case <-ctx.Done():
		return false
	case <-time.After(hostRetry):
		return true
	}
}

// Bounds of the backoff between conference requests while the focus is not
// ready.
const (
	conferenceRetryMin = 250 * time.Millisecond
	conferenceRetryMax = 2 * time.Second
)

// validNick is the occupant resource accepted by Jitsi's Prosody
// (mod_muc_resource_validate).
var validNick = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_]*$`)

// occupantNick returns the MUC resource to join with. Like lib-jitsi-meet, it
// is the first 8 characters of the server-assigned JID localpart, which
// Prosody requires from anonymous users in anonymous_strict mode; otherwise a
// random one.
func occupantNick(local jid.JID) string {
	if l := local.Localpart(); len(l) >= 8 && validNick.MatchString(l[:8]) {
		return l[:8]
	}
	return fmt.Sprintf("%08x", rand.Uint32())
}

// mucJoinPresence builds the XEP-0045 join request, with the display name
// (XEP-0172) shown to the other participants and the entity capabilities
// (XEP-0115) Jicofo uses to select what to offer:
// <presence to="room@muc/nick"><x xmlns="http://jabber.org/protocol/muc"/><nick xmlns="http://jabber.org/protocol/nick">name</nick></presence>
func mucJoinPresence(occupantJID, displayName string, sources xml.TokenReader) xml.TokenReader {
	mucX := xmlstream.Wrap(nil, xml.StartElement{
		Name: xml.Name{Space: nsMUC, Local: "x"},
	})
	nick := xmlstream.Wrap(xmlstream.Token(xml.CharData(displayName)), xml.StartElement{
		Name: xml.Name{Space: nsNick, Local: "nick"},
	})
	children := []xml.TokenReader{mucX, nick, capsElement()}
	if sources != nil {
		children = append(children, sources)
	}
	return xmlstream.Wrap(xmlstream.MultiReader(children...), xml.StartElement{
		Name: xml.Name{Local: "presence"},
		Attr: []xml.Attr{
			{Name: xml.Name{Local: "to"}, Value: occupantJID},
		},
	})
}

// sourcesPresence returns the presence extensions with which lib-jitsi-meet
// announces a participant's sources and their mute state: <SourceInfo> (a
// JSON map by source name, used with source-name signaling) and the legacy
// <audiomuted>/<videomuted>. Nil when nothing is sent.
func sourcesPresence(nick string, kinds []string) xml.TokenReader {
	if len(kinds) == 0 {
		return nil
	}
	type sourceInfo struct {
		Muted     bool   `json:"muted"`
		VideoType string `json:"videoType,omitempty"`
	}
	info := map[string]sourceInfo{}
	var muted []xml.TokenReader
	for _, kind := range kinds {
		si := sourceInfo{}
		if kind == "video" {
			si.VideoType = "camera"
		}
		info[localSourceName(nick, kind)] = si
		muted = append(muted, xmlstream.Wrap(xmlstream.Token(xml.CharData("false")), xml.StartElement{
			Name: xml.Name{Space: "http://jitsi.org/jitmeet/" + kind, Local: kind + "muted"},
		}))
	}
	data, _ := json.Marshal(info) // Map keys are sorted: stable output
	sourceInfoEl := xmlstream.Wrap(xmlstream.Token(xml.CharData(data)), xml.StartElement{Name: xml.Name{Local: "SourceInfo"}})
	return xmlstream.MultiReader(append([]xml.TokenReader{sourceInfoEl}, muted...)...)
}

// JoinMUC requests the conference from the focus, then joins the configured
// Multi-User Chat room under displayName. Use WaitJoined to wait for the
// outcome.
func (s *SignalingClient) JoinMUC(ctx context.Context, displayName string) error {
	session := s.currentSession()
	if session == nil {
		return errClientClosed
	}
	s.lifeMu.Lock()
	s.nick = occupantNick(session.LocalAddr())
	s.lifeMu.Unlock()

	if err := s.requestConference(ctx, session); err != nil {
		return err
	}
	s.mu.Lock()
	var kinds []string
	if s.handler != nil {
		kinds = s.handler.LocalMediaKinds()
	}
	s.mu.Unlock()
	s.logger.Debug("Joining room", slog.String("occupant", s.occupantJID()), slog.String("display_name", displayName), slog.Any("sent_kinds", kinds))
	return session.Send(ctx, mucJoinPresence(s.occupantJID(), displayName, sourcesPresence(s.nickname(), kinds)))
}

// WaitJoined waits for the MUC join to complete or fail.
func (s *SignalingClient) WaitJoined(ctx context.Context) error {
	select {
	case <-s.joinDone:
		return s.joinErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

// mucPresence is a presence stanza with the MUC user extension (XEP-0045).
type mucPresence struct {
	From  string        `xml:"from,attr"`
	Type  string        `xml:"type,attr"`
	X     *mucUserX     `xml:"http://jabber.org/protocol/muc#user x"`
	Error *stanza.Error `xml:"error"`

	// Jitsi participant state (lib-jitsi-meet presence extensions).
	Nick       string `xml:"http://jabber.org/protocol/nick nick"`
	AudioMuted string `xml:"http://jitsi.org/jitmeet/audio audiomuted"`
	VideoMuted string `xml:"http://jitsi.org/jitmeet/video videomuted"`
	SourceInfo string `xml:"SourceInfo"` // JSON: source name -> {"muted": bool, ...}
}

// occupant returns the participant state a presence announces.
func (p *mucPresence) occupant(id string) signaling.Occupant {
	o := signaling.Occupant{
		ID:          id,
		DisplayName: strings.TrimSpace(p.Nick),
		AudioMuted:  p.AudioMuted == "true",
		VideoMuted:  p.VideoMuted == "true",
		Left:        p.Type == string(stanza.UnavailablePresence),
	}
	// SourceInfo, when present, is the current source-name signaling and
	// wins over the legacy elements.
	var info map[string]struct {
		Muted bool `json:"muted"`
	}
	if p.SourceInfo != "" && json.Unmarshal([]byte(p.SourceInfo), &info) == nil && len(info) > 0 {
		o.AudioMuted, o.VideoMuted = true, true
		for name, si := range info {
			i := strings.LastIndex(name, "-")
			if i < 0 || i+1 >= len(name) {
				continue
			}
			switch name[i+1] {
			case 'a':
				o.AudioMuted = o.AudioMuted && si.Muted
			case 'v':
				o.VideoMuted = o.VideoMuted && si.Muted
			}
		}
	}
	return o
}

// focusNick is the occupant resource of Jicofo in the room.
const focusNick = "focus"

type mucUserX struct {
	Statuses []struct {
		Code string `xml:"code,attr"`
	} `xml:"status"`
}

func (p *mucPresence) hasStatus(code string) bool {
	if p.X == nil {
		return false
	}
	for _, s := range p.X.Statuses {
		if s.Code == code {
			return true
		}
	}
	return false
}

// removalReason maps self-presence status codes (XEP-0045 §15.6) to a reason.
func (p *mucPresence) removalReason() string {
	switch {
	case p.hasStatus("307"):
		return "kicked"
	case p.hasStatus("301"):
		return "banned"
	case p.hasStatus("321"):
		return "affiliation changed"
	case p.hasStatus("322"):
		return "room became members-only"
	case p.hasStatus("332"):
		return "MUC service shutting down"
	default:
		return "left room"
	}
}

// handlePresence tracks our own room presence: join confirmation (status
// 110), join errors, and removal from the room.
func (s *SignalingClient) handlePresence(t xmlstream.TokenReadEncoder, start *xml.StartElement) error {
	var p mucPresence
	if err := decodeElement(t, start, &p); err != nil {
		return err
	}
	from, err := jid.Parse(p.From)
	if err != nil || from.Bare().String() != s.roomJID() {
		return nil // Not from our room
	}
	self := p.hasStatus("110") || p.From == s.occupantJID()

	switch {
	case p.Type == string(stanza.ErrorPresence):
		condition := "unknown"
		if p.Error != nil {
			condition = string(p.Error.Condition)
		}
		err := fmt.Errorf("%w: %s", ErrRoomUnavailable, condition)
		if !s.finishJoin(err) {
			s.logger.Error("Presence error from room", slog.String("error", err.Error()))
		}
	case !self:
		s.logger.Debug("Participant presence", slog.String("from", p.From), slog.String("type", p.Type))
		if nick := from.Resourcepart(); nick != focusNick && nick != "" {
			s.mu.RLock()
			handler := s.handler
			s.mu.RUnlock()
			if o, ok := handler.(signaling.RoomObserver); ok {
				o.OnOccupant(p.occupant(nick))
			}
		}
		if p.Type == string(stanza.UnavailablePresence) {
			// Jicofo sends no source-remove for a participant who leaves:
			// clients drop its sources on its unavailable presence.
			s.removeOwnerSources(from.Resourcepart())
		}
	case p.Type == string(stanza.UnavailablePresence):
		reason := p.removalReason()
		err := fmt.Errorf("%w: %s", ErrKicked, reason)
		if reason == "left room" {
			err = fmt.Errorf("%w: %s", ErrRoomUnavailable, reason)
		}
		if !s.finishJoin(err) {
			s.lost(err)
		}
	default:
		if s.finishJoin(nil) {
			s.state.Store(int32(ClientStateJoined))
			s.logger.Info("Joined MUC successfully", slog.String("room", s.roomJID()))
		}
	}
	return nil
}

// handleIQ processes incoming Jingle IQs and always replies, as Jicofo
// expects every set IQ to be acknowledged.
func (s *SignalingClient) handleIQ(iq stanza.IQ, t xmlstream.TokenReadEncoder, start *xml.StartElement) error {
	var jingle JingleIQ
	if err := decodeElement(t, start, &jingle); err != nil {
		s.logger.Warn("Failed to decode Jingle IQ", slog.String("error", err.Error()))
		return replyIQ(t, iq.Error(stanza.Error{Type: stanza.Modify, Condition: stanza.BadRequest}))
	}

	// Sources may come JSON-encoded (http://jitsi.org/json-encoded-sources):
	// convert them to Jingle sources before handling the action.
	if err := applyJSONSources(&jingle); err != nil {
		s.logger.Warn("Rejected Jingle IQ", slog.String("action", jingle.Action), slog.String("error", err.Error()))
		return replyIQ(t, iq.Error(stanza.Error{Type: stanza.Modify, Condition: stanza.BadRequest}))
	}

	var err error
	switch jingle.Action {
	case "session-initiate":
		err = s.HandleSessionInitiate(iq.From.String(), &jingle)
	case "transport-info":
		err = s.HandleTransportInfo(iq.From.String(), &jingle)
	case "session-terminate":
		s.handleSessionTerminate(&jingle)
	case "source-add", "source-remove":
		err = s.HandleSourceChange(iq.From.String(), &jingle)
	default:
		s.logger.Debug("Unhandled Jingle action", slog.String("action", jingle.Action), slog.String("sid", jingle.SID))
	}
	if err != nil {
		s.logger.Error("Rejected Jingle IQ", slog.String("action", jingle.Action), slog.String("error", err.Error()))
		return replyIQ(t, iq.Error(stanza.Error{Type: stanza.Cancel, Condition: stanza.NotAcceptable}))
	}
	return replyIQ(t, iq.Result(nil))
}

// decodeElement decodes the element whose start token was already consumed
// from r. The start token is replayed so that the decoder sees a well-formed
// element.
func decodeElement(r xml.TokenReader, start *xml.StartElement, v any) error {
	return xml.NewTokenDecoder(xmlstream.MultiReader(xmlstream.Token(*start), r)).Decode(v)
}

func replyIQ(w xmlstream.TokenWriter, reply xml.TokenReader) error {
	_, err := xmlstream.Copy(w, reply)
	return err
}

// leavePresence builds <presence type="unavailable" to="room@muc/nick"/>.
func leavePresence(occupantJID string) xml.TokenReader {
	return xmlstream.Wrap(nil, xml.StartElement{
		Name: xml.Name{Local: "presence"},
		Attr: []xml.Attr{
			{Name: xml.Name{Local: "to"}, Value: occupantJID},
			{Name: xml.Name{Local: "type"}, Value: string(stanza.UnavailablePresence)},
		},
	})
}

// Disconnect leaves the room and closes the connection. It is idempotent,
// and safe to call while Connect is in progress.
func (s *SignalingClient) Disconnect() error {
	s.lifeMu.Lock()
	if s.closing {
		s.lifeMu.Unlock()
		return nil
	}
	s.closing = true
	session, cancel, nick := s.session, s.cancel, s.nick
	s.lifeMu.Unlock()

	s.logger.Info("Disconnecting XMPP client")
	s.finishJoin(errClientClosed)
	s.state.Store(int32(ClientStateDisconnected))
	s.closeCancel()
	s.wg.Wait()

	var err error
	if session != nil {
		// Leave the room and end the stream gracefully, but never wait on a
		// stuck connection: mellium's Send/Close take the output lock
		// without honouring a context.
		closed := make(chan error, 1)
		go func() {
			ctx, stop := context.WithTimeout(context.Background(), closeTimeout)
			defer stop()
			if nick != "" {
				session.Send(ctx, leavePresence(s.occupantJID()))
			}
			closed <- session.Close()
		}()
		select {
		case err = <-closed:
		case <-time.After(closeTimeout):
			err = fmt.Errorf("timed out closing the XMPP stream")
		}
	}
	if cancel != nil {
		cancel() // Closes the WebSocket (unblocking any writer) and stops the keepalive
	}
	return err
}

// --- Debug helpers ---

const (
	ansiRed   = "\033[31m"
	ansiGreen = "\033[32m"
	ansiReset = "\033[0m"
)

type colorWriter struct {
	w     io.Writer
	color string
	last  *int32 // Shared state: 0=init, 1=in, 2=out
	dir   int32  // This writer's direction: 1=in, 2=out
}

func (cw *colorWriter) Write(p []byte) (n int, err error) {
	// Check if direction changed
	if atomic.SwapInt32(cw.last, cw.dir) != cw.dir {
		cw.w.Write([]byte("\n"))
	}

	cw.w.Write([]byte(cw.color))
	n, err = cw.w.Write(p)
	cw.w.Write([]byte(ansiReset))
	return n, err
}
