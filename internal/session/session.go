package session

import (
	"context"
	"errors"
	"fmt"

	"log/slog"
	"os"
	"slices"
	"sync"
	"time"

	"github.com/daimoc/jitsi-media-tools/internal/compose"
	"github.com/daimoc/jitsi-media-tools/internal/ffmpeg"
	"github.com/daimoc/jitsi-media-tools/internal/jitsi"
	"github.com/daimoc/jitsi-media-tools/internal/output"
	"github.com/daimoc/jitsi-media-tools/internal/peer"
	"github.com/daimoc/jitsi-media-tools/internal/signaling"
)

// State is the session's lifecycle state (data-model "Session").
type State string

const (
	StateConnecting  State = "connecting"
	StateWaitingHost State = "waiting_host"
	StateJoining     State = "joining"
	StateJoined      State = "joined"
	StateLeaving     State = "leaving"
	StateClosed      State = "closed"
	StateFailed      State = "failed"
)

// Config describes a session. Server fields left empty come from the
// meeting's config.js.
type Config struct {
	MeetingURL  string
	DisplayName string

	XMPPDomain, MUCDomain, AnonymousDomain, WebSocketURL, FocusJID string
	Insecure                                                       bool

	Token, AppID, AppSecret, Audience, Subject string
	TokenTTL                                   time.Duration
	Moderator                                  bool

	WaitHost       time.Duration // Guest wait for a host; 0 means 5 minutes
	ConnectTimeout time.Duration // XMPP connect and join; 0 means 30 s

	FFmpeg, FFplay string   // Executables (default: from PATH)
	FFmpegNeeds    []string // ffmpeg parts the session's media needs (ffmpeg.Need*)
	NeedFFplay     bool

	Publish   *Publish        // Nil: publish nothing
	Selection Selection       // Which video to receive
	Display   *DisplayOptions // Nil: no window
	OBS       *OBSOptions     // Nil: no SRT feeds
	Record    *RecordOptions  // Nil: no recording

	// OnGrid gets the composed grid at 30 fps and OnMix the room's mix
	// every 20 ms (ChunkSamples); buffers are valid during the call only,
	// and the calls must not block.
	OnGrid func(f *compose.Frame, at time.Time)
	OnMix  func(samples []int16, at time.Time)
	// Receive makes the session ready to decode what it receives for
	// callbacks set later (SetOnVideo…), checking the decoders at Start.
	Receive bool
	// SendOnly asks the bridge for no video (a gateway participant); the
	// selection is ignored.
	SendOnly bool

	Logger  *slog.Logger
	OnEvent func(Event)                  // Participant events; must not block
	OnState func(state State, err error) // State changes; must not block
	// OnWaitingHost is called once when a guest waits for a host.
	OnWaitingHost func()
}

// SignalClient is the signaling side as the session uses it;
// *jitsi.Adapter implements it, tests replace it.
type SignalClient interface {
	Connect(ctx context.Context, peerID string, handler signaling.SignalingHandler) error
	Disconnect(peerID string) error
	SetReceivePolicy(peerID string, p *signaling.ReceivePolicy)
	SendEndpointStats(peerID string, st signaling.EndpointStats)
}

// newSignal builds the signaling client for a configuration (tests replace
// it).
var newSignal = func(c map[string]any, logger *slog.Logger) (SignalClient, error) {
	a := jitsi.NewAdapter(logger)
	if err := a.ParseConfig(c); err != nil {
		return nil, err
	}
	return a, nil
}

// fetchMeeting reads a meeting's config.js (tests replace it).
var fetchMeeting = func(ctx context.Context, meetingURL string, insecure bool) (jitsi.Meeting, error) {
	return jitsi.FetchMeeting(ctx, meetingURL, jitsi.HTTPClient(insecure))
}

// Session is one participant in one room.
type Session struct {
	cfg    Config
	logger *slog.Logger
	sig    SignalClient
	peer   *peer.Peer
	reg    *registry
	room   string

	ctx    context.Context // Ends when the session closes; media goroutines and processes follow it
	cancel context.CancelFunc
	wg     sync.WaitGroup // Media goroutines

	mu     sync.Mutex
	state  State
	err    error
	warned map[string]bool // warnOnce keys

	media *media // Publish and receive pipelines

	done      chan struct{}
	closeOnce sync.Once
}

// Start resolves the server settings, checks ffmpeg, and joins the room. It
// returns once joined, or with the error that stopped it.
func Start(ctx context.Context, cfg Config) (*Session, error) {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.DisplayName == "" {
		return nil, errors.New("display name required")
	}
	if cfg.WaitHost <= 0 {
		cfg.WaitHost = 5 * time.Minute
	}
	if cfg.ConnectTimeout <= 0 {
		cfg.ConnectTimeout = 30 * time.Second
	}
	cfg.Selection = cfg.Selection.withDefaults()
	if err := cfg.Selection.validate(); err != nil {
		return nil, err
	}
	s := &Session{cfg: cfg, logger: cfg.Logger, reg: newRegistry(), state: StateConnecting, done: make(chan struct{})}
	s.ctx, s.cancel = context.WithCancel(context.Background())
	s.reg.setPins(cfg.Selection.Pins)

	// ffmpeg first: a missing part fails before contacting the server
	// (FR-023).
	tools, err := s.checkTools(ctx)
	if err != nil {
		s.cancel()
		return nil, err
	}
	if r := cfg.Record; r != nil {
		if err := os.MkdirAll(r.Dir, 0o755); err != nil {
			s.cancel()
			return nil, fmt.Errorf("record directory: %w", err)
		}
	}
	m, err := s.resolve(ctx)
	if err != nil {
		s.cancel()
		return nil, err
	}
	s.room = m.Room
	s.media = newMedia(s, tools)
	if err := s.media.prepare(ctx); err != nil {
		s.cancel()
		return nil, err
	}

	p, err := peer.New(peer.Config{
		Logger:      s.logger,
		Kinds:       s.media.publishKinds(),
		VideoHeight: cfg.Selection.Height,
		VideoWidth:  s.media.publishWidth(), VideoHeightSent: s.media.publishHeight(),
		VideoCodec: s.media.publishCodec(),
	})
	if err != nil {
		s.media.stop()
		s.cancel()
		return nil, err
	}
	s.peer = p
	p.OnTrack(s.media.onTrack)
	p.OnTransportState(s.onTransport)
	p.OnKeyframeRequest(s.media.onKeyframeRequest)

	sig, err := newSignal(s.signalConfig(m), s.logger)
	if err != nil {
		s.abort()
		return nil, err
	}
	s.sig = sig
	s.setState(StateJoining, nil)
	joinCtx, cancel := context.WithTimeout(ctx, cfg.ConnectTimeout+cfg.WaitHost)
	defer cancel()
	if err := sig.Connect(joinCtx, cfg.DisplayName, &handler{Peer: p, s: s}); err != nil {
		s.abort()
		if s.State() == StateWaitingHost {
			return nil, fmt.Errorf("timed out waiting for a host after %s: %w", cfg.WaitHost, err)
		}
		return nil, err
	}
	s.applySelection()
	if err := s.media.joined(); err != nil {
		if derr := sig.Disconnect(cfg.DisplayName); derr != nil {
			s.logger.Debug("Disconnect", slog.String("error", derr.Error()))
		}
		s.abort()
		return nil, err
	}
	s.setState(StateJoined, nil)
	s.logger.Info("Joined room", slog.String("room", s.room), slog.String("name", cfg.DisplayName))
	return s, nil
}

// checkTools locates and probes ffmpeg (and ffplay) when the session needs
// them.
func (s *Session) checkTools(ctx context.Context) (tools, error) {
	var t tools
	if s.cfg.Display != nil {
		s.cfg.NeedFFplay = true
	}
	if len(s.cfg.FFmpegNeeds) == 0 && !s.cfg.encodes() && !s.cfg.NeedFFplay && !s.cfg.receives() {
		return t, nil
	}
	bin, err := ffmpeg.Locate("ffmpeg", s.cfg.FFmpeg)
	if err != nil {
		return t, err
	}
	caps, err := ffmpeg.Probe(ctx, bin)
	if err != nil {
		return t, err
	}
	needs := s.cfg.FFmpegNeeds
	if s.cfg.encodes() {
		needs = append(slices.Clone(needs), publishNeeds...)
	}
	if s.cfg.receives() {
		needs = append(slices.Clone(needs), receiveNeeds...)
	}
	if s.cfg.OBS != nil {
		needs = append(slices.Clone(needs), feedNeeds...)
	}
	if s.cfg.Record != nil {
		needs = append(slices.Clone(needs), output.RecordNeeds...)
	}
	if err := caps.Missing(needs...); err != nil {
		return t, err
	}
	t.ffmpeg, t.caps = bin, caps
	if s.cfg.NeedFFplay {
		if t.ffplay, err = ffmpeg.Locate("ffplay", s.cfg.FFplay); err != nil {
			return t, err
		}
	}
	return t, nil
}

// resolve reads config.js and applies the overrides (research R12).
func (s *Session) resolve(ctx context.Context) (jitsi.Meeting, error) {
	s.setState(StateConnecting, nil)
	m, err := fetchMeeting(ctx, s.cfg.MeetingURL, s.cfg.Insecure)
	if err != nil {
		// Every setting given: config.js is not needed.
		if s.cfg.XMPPDomain == "" || s.cfg.WebSocketURL == "" {
			return m, err
		}
		_, base, perr := jitsi.ParseMeetingURL(s.cfg.MeetingURL)
		if perr != nil {
			return m, perr
		}
		s.logger.Warn("config.js not read; using the given settings", slog.String("error", err.Error()))
		m = base
	}
	for _, o := range []struct {
		dst *string
		v   string
	}{{&m.Domain, s.cfg.XMPPDomain}, {&m.MUC, s.cfg.MUCDomain}, {&m.AnonymousDomain, s.cfg.AnonymousDomain},
		{&m.WebSocket, s.cfg.WebSocketURL}, {&m.Focus, s.cfg.FocusJID}} {
		if o.v != "" {
			*o.dst = o.v
		}
	}
	if s.cfg.Token == "" {
		s.cfg.Token = m.Token
	}
	return m, nil
}

// signalConfig builds the Jitsi adapter configuration.
func (s *Session) signalConfig(m jitsi.Meeting) map[string]any {
	domain := m.Domain
	if s.cfg.Token == "" && s.cfg.AppID == "" && m.AnonymousDomain != "" {
		domain = m.AnonymousDomain // Guest on a secure-domain server
	}
	c := map[string]any{
		"xmpp_domain":     domain,
		"room_name":       m.Room,
		"muc_domain":      m.MUC,
		"websocket_url":   m.WebSocket,
		"insecure":        s.cfg.Insecure,
		"connect_timeout": (s.cfg.ConnectTimeout + s.cfg.WaitHost).String(),
	}
	if m.Focus != "" {
		c["focus_jid"] = m.Focus
	} else {
		c["focus_jid"] = "focus." + m.Domain
	}
	if m.MUC == "" {
		c["muc_domain"] = "conference." + m.Domain
	}
	switch {
	case s.cfg.Token != "":
		c["jwt"] = s.cfg.Token
	case s.cfg.AppID != "":
		c["jwt_app_id"], c["jwt_app_secret"] = s.cfg.AppID, s.cfg.AppSecret
		c["jwt_moderator"] = s.cfg.Moderator
		if s.cfg.Audience != "" {
			c["jwt_audience"] = s.cfg.Audience
		}
		c["jwt_subject"] = m.Domain
		if s.cfg.Subject != "" {
			c["jwt_subject"] = s.cfg.Subject
		}
		if s.cfg.TokenTTL > 0 {
			c["jwt_ttl"] = s.cfg.TokenTTL.String()
		}
	}
	return c
}

// abort undoes a failed Start.
func (s *Session) abort() {
	if s.peer != nil {
		s.peer.Close()
	}
	s.media.stop()
	s.cancel()
	s.wg.Wait()
}

func (s *Session) setState(st State, err error) {
	s.mu.Lock()
	if s.state == StateClosed || s.state == StateFailed {
		s.mu.Unlock()
		return
	}
	s.state, s.err = st, err
	s.mu.Unlock()
	if s.cfg.OnState != nil {
		s.cfg.OnState(st, err)
	}
}

// State returns the current state.
func (s *Session) State() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

// Room returns the room name.
func (s *Session) Room() string { return s.room }

// Done is closed when the session has ended (closed or failed).
func (s *Session) Done() <-chan struct{} { return s.done }

// Err returns why the session failed; nil after a Close.
func (s *Session) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

// Participants returns the room's other participants.
func (s *Session) Participants() []Participant { return s.reg.list() }

// onTransport fails the session when the media transport or the signaling
// connection is lost (FR-006).
func (s *Session) onTransport(st peer.TransportState, err error) {
	switch st {
	case peer.TransportConnected:
		s.logger.Info("Media connected")
		s.media.onConnected()
	case peer.TransportFailed:
		go s.fail(err)
	}
}

// fail ends the session with err.
func (s *Session) fail(err error) {
	s.mu.Lock()
	if s.state == StateClosed || s.state == StateFailed || s.state == StateLeaving {
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	s.logger.Error("Session failed", slog.String("error", err.Error()))
	s.shutdown()
	s.mu.Lock()
	s.state, s.err = StateFailed, err
	s.mu.Unlock()
	if s.cfg.OnState != nil {
		s.cfg.OnState(StateFailed, err)
	}
	s.closeOnce.Do(func() { close(s.done) })
}

// Close leaves the room and stops every media process; it returns once they
// have exited. It is idempotent.
func (s *Session) Close() error {
	s.mu.Lock()
	if s.state == StateClosed || s.state == StateFailed {
		s.mu.Unlock()
		<-s.done
		return nil
	}
	s.mu.Unlock()
	s.setState(StateLeaving, nil)
	s.shutdown()
	s.setState(StateClosed, nil)
	s.closeOnce.Do(func() { close(s.done) })
	return nil
}

// shutdown leaves the room and stops media.
func (s *Session) shutdown() {
	if s.sig != nil {
		if err := s.sig.Disconnect(s.cfg.DisplayName); err != nil {
			s.logger.Debug("Disconnect", slog.String("error", err.Error()))
		}
	}
	s.media.stop()
	s.cancel()
	if s.peer != nil {
		s.peer.Close()
	}
	s.wg.Wait()
}

// emit forwards a participant event.
func (s *Session) emit(evs ...Event) {
	if s.cfg.OnEvent == nil {
		return
	}
	for _, e := range evs {
		s.cfg.OnEvent(e)
	}
}

// handler is the peer seen by signaling, plus the room observer.
type handler struct {
	*peer.Peer
	s *Session
}

var (
	_ signaling.RoomObserver = (*handler)(nil)
	_ signaling.HostWaiter   = (*handler)(nil)
)

func (h *handler) OnWaitingForHost() {
	h.s.setState(StateWaitingHost, nil)
	if h.s.cfg.OnWaitingHost != nil {
		h.s.cfg.OnWaitingHost()
	}
}

func (h *handler) OnOccupant(o signaling.Occupant) {
	evs := h.s.reg.occupant(o)
	h.s.emit(evs...)
	h.s.media.participantsChanged() // Also a screen share starting or stopping
	if len(evs) > 0 {
		h.s.applySelection()
	}
}

func (h *handler) OnRemoteSources(list []signaling.RemoteSource) {
	h.s.reg.sources(list)
	h.s.applySelection()
	h.s.media.participantsChanged() // Screen shares come and go with their sources
}

func (h *handler) OnDominantSpeaker(id string, previous []string) {
	h.s.reg.speaker(id, previous, time.Now())
	h.s.media.speakersChanged()
}
