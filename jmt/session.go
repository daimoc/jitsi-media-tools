package jmt

import (
	"context"
	"errors"
	"sync"

	"github.com/daimoc/jitsi-media-tools/internal/session"
)

// Session is one participant in one room. Create it with Join.
type Session struct {
	s      *session.Session
	events *eventQueue
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
	q := newEventQueue()
	cfg.OnEvent = func(e session.Event) { q.push(eventFrom(e)) }
	cfg.OnWaitingHost = func() { q.push(Event{Kind: WaitingForHost}) }
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
	}
	if o.Publish != nil {
		cfg.Publish = o.Publish.publish()
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
	}
	return Event{Kind: k, Participant: participantFrom(e.Participant)}
}

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
type eventQueue struct {
	out chan Event

	mu     sync.Mutex
	queue  []Event
	closed bool
	wake   chan struct{}
}

func newEventQueue() *eventQueue {
	q := &eventQueue{out: make(chan Event), wake: make(chan struct{}, 1)}
	go q.run()
	return q
}

func (q *eventQueue) push(e Event) {
	q.mu.Lock()
	if !q.closed {
		q.queue = append(q.queue, e)
	}
	q.mu.Unlock()
	q.signal()
}

func (q *eventQueue) close() {
	q.mu.Lock()
	q.closed = true
	q.mu.Unlock()
	q.signal()
}

func (q *eventQueue) signal() {
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

func (q *eventQueue) run() {
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

// Address is something the session listens on, e.g. the OBS input.
type Address struct {
	Label string // e.g. "obs input"
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
