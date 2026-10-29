package jmt

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/daimoc/jitsi-media-tools/internal/compose"
	"github.com/daimoc/jitsi-media-tools/internal/session"
)

// Session is one participant in one room. Create it with Join.
type Session struct {
	s      *session.Session
	events *eventQueue[Event]
}

// Join connects to the meeting and joins the room. It returns once joined,
// after waiting for a host if the server needs one, or with the error that
// stopped it. ctx bounds the join only; the session lasts until Close or
// until it fails.
func Join(ctx context.Context, o Options) (*Session, error) {
	cfg, err := o.sessionConfig()
	if err != nil {
		return nil, err
	}
	q := newEventQueue[Event]()
	cfg.OnEvent = func(e session.Event) { q.push(eventFrom(e)) }
	cfg.OnWaitingHost = func() {
		q.push(Event{Kind: WaitingForHost})
		if o.OnWaitingForHost != nil {
			o.OnWaitingForHost()
		}
	}
	s, err := session.Start(ctx, cfg)
	if err != nil {
		q.close()
		return nil, err
	}
	go func() {
		<-s.Done()
		q.close()
	}()
	return &Session{s: s, events: q}, nil
}

func (o Options) sessionConfig() (session.Config, error) {
	if o.MeetingURL == "" {
		return session.Config{}, errors.New("meeting URL required")
	}
	if o.DisplayName == "" {
		return session.Config{}, errors.New("display name required")
	}
	cfg := session.Config{
		MeetingURL: o.MeetingURL, DisplayName: o.DisplayName,
		XMPPDomain: o.XMPPDomain, MUCDomain: o.MUCDomain, AnonymousDomain: o.AnonymousDomain,
		WebSocketURL: o.WebSocketURL, FocusJID: o.FocusJID, Insecure: o.Insecure,
		Token: o.Token, AppID: o.AppID, AppSecret: o.AppSecret, Audience: o.Audience, Subject: o.Subject,
		TokenTTL: o.TokenTTL, Moderator: o.Moderator,
		WaitHost: o.WaitHost, FFmpeg: o.FFmpeg, FFplay: o.FFplay,
		Selection: session.Selection(o.Selection),
		Logger:    o.Logger,
		Receive:   true, // OnVideo and the others come after Join
	}
	if o.Publish != nil {
		cfg.Publish = o.Publish.publish()
	}
	if o.Display != nil {
		cfg.Display = &session.DisplayOptions{Title: o.Display.Title}
	}
	if o.Record != nil {
		cfg.Record = &session.RecordOptions{Dir: o.Record.Dir, Mixed: o.Record.Mode == RecordMixed}
	}
	if o.OBS != nil {
		cfg.OBS = &session.OBSOptions{Host: o.OBS.Host, URLHost: o.OBS.URLHost, BasePort: o.OBS.BasePort, MaxFeeds: o.OBS.MaxFeeds, Grid: o.OBS.Grid}
	}
	return cfg, nil
}

func participantFrom(p session.Participant) Participant {
	return Participant{ID: p.ID, Name: p.Name, AudioMuted: p.AudioMuted, VideoMuted: p.VideoMuted, Pinned: p.Pinned, Selected: p.Selected}
}

func eventFrom(e session.Event) Event {
	k := ParticipantChanged
	switch e.Kind {
	case session.EventJoined:
		k = ParticipantJoined
	case session.EventLeft:
		k = ParticipantLeft
	case session.EventFeedReady:
		return Event{Kind: FeedReady, Participant: participantFrom(e.Participant), FeedURL: e.FeedURL}
	case session.EventRecordingStarted:
		return Event{Kind: RecordingStarted, Participant: participantFrom(e.Participant), Path: e.Path}
	}
	return Event{Kind: k, Participant: participantFrom(e.Participant)}
}

// FeedURLs returns the OBS feed addresses served so far: participant name
// ("" for the grid) -> srt URL. A participant keeps their address for the
// session, also after leaving and coming back.
func (s *Session) FeedURLs() map[string]string { return s.s.FeedURLs() }

// Room returns the room name.
func (s *Session) Room() string { return s.s.Room() }

// Events returns the session's events, in order; the channel is closed when
// the session ends. Events are queued, never dropped.
func (s *Session) Events() <-chan Event { return s.events.out }

// Participants returns the room's other participants, in join order.
func (s *Session) Participants() []Participant {
	var out []Participant
	for _, p := range s.s.Participants() {
		out = append(out, participantFrom(p))
	}
	return out
}

// OnVideo sets the function that gets each selected participant's decoded
// video, at the size the selection asks for (nil stops it). Callbacks run on
// session goroutines, one per callback: a slow one loses frames and delays
// nothing else. The frame's buffers are valid during the call only.
// Received video is decoded only while OnVideo, OnGrid or a display
// consumes it.
func (s *Session) OnVideo(f func(VideoFrame)) {
	var g session.VideoFunc
	if f != nil {
		g = func(id string, fr *compose.Frame, at time.Time) { f(videoFrame(id, fr, at)) }
	}
	s.s.SetOnVideo(g)
}

// OnGrid sets the function that gets the composed tile grid (1280x720) at
// 30 fps, ParticipantID "", under the rules of OnVideo.
func (s *Session) OnGrid(f func(VideoFrame)) {
	var g session.VideoFunc
	if f != nil {
		g = func(id string, fr *compose.Frame, at time.Time) { f(videoFrame(id, fr, at)) }
	}
	s.s.SetOnGrid(g)
}

// OnAudio sets the function that gets each participant's audio, every
// 20 ms, under the rules of OnVideo.
func (s *Session) OnAudio(f func(AudioChunk)) { s.s.SetOnAudio(audioFunc(f)) }

// OnMixedAudio sets the function that gets the room's mixed audio, every
// 20 ms, ParticipantID "", under the rules of OnVideo.
func (s *Session) OnMixedAudio(f func(AudioChunk)) { s.s.SetOnMix(audioFunc(f)) }

func videoFrame(id string, f *compose.Frame, at time.Time) VideoFrame {
	return VideoFrame{ParticipantID: id, Width: f.W, Height: f.H, Y: f.Y, U: f.U, V: f.V, Time: at}
}

func audioFunc(f func(AudioChunk)) session.AudioFunc {
	if f == nil {
		return nil
	}
	return func(id string, s []int16, at time.Time) { f(AudioChunk{ParticipantID: id, Samples: s, Time: at}) }
}

// SetSelection changes which video the session receives.
func (s *Session) SetSelection(sel Selection) error {
	return s.s.SetSelection(session.Selection(sel))
}

// Close leaves the room and stops every helper process; it returns once
// they have exited. It is idempotent.
func (s *Session) Close() error { return s.s.Close() }

// Wait blocks until the session ends and returns why: nil after Close or
// once a file published once has ended.
func (s *Session) Wait() error {
	<-s.s.Done()
	return s.s.Err()
}

// Done is closed when the session ends.
func (s *Session) Done() <-chan struct{} { return s.s.Done() }

// eventQueue delivers events in order without blocking the producer.
type eventQueue[T any] struct {
	out chan T

	mu     sync.Mutex
	queue  []T
	closed bool
	wake   chan struct{}
}

func newEventQueue[T any]() *eventQueue[T] {
	q := &eventQueue[T]{out: make(chan T), wake: make(chan struct{}, 1)}
	go q.run()
	return q
}

func (q *eventQueue[T]) push(e T) {
	q.mu.Lock()
	if !q.closed {
		q.queue = append(q.queue, e)
	}
	q.mu.Unlock()
	q.signal()
}

func (q *eventQueue[T]) close() {
	q.mu.Lock()
	q.closed = true
	q.mu.Unlock()
	q.signal()
}

func (q *eventQueue[T]) signal() {
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

func (q *eventQueue[T]) run() {
	defer close(q.out)
	for {
		q.mu.Lock()
		if len(q.queue) == 0 {
			closed := q.closed
			q.mu.Unlock()
			if closed {
				return
			}
			<-q.wake
			continue
		}
		e := q.queue[0]
		q.queue = q.queue[1:]
		q.mu.Unlock()
		q.out <- e
	}
}

// Address is something the session listens on.
type Address struct {
	Label string // e.g. "feed Alice"
	URL   string
}

// Listening returns what the session listens on.
func (s *Session) Listening() []Address {
	var out []Address
	for _, a := range s.s.Listening() {
		out = append(out, Address{Label: a.Label, URL: a.URL})
	}
	return out
}
