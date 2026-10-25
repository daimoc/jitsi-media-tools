package session

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/daimoc/jitsi-media-tools/internal/jitsi"
	"github.com/daimoc/jitsi-media-tools/internal/signaling"
)

type collected struct {
	mu     sync.Mutex
	events []Event
	states []State
}

func (c *collected) config(cfg Config) Config {
	cfg.Logger = quietLogger
	cfg.OnEvent = func(e Event) { c.mu.Lock(); c.events = append(c.events, e); c.mu.Unlock() }
	cfg.OnState = func(s State, _ error) { c.mu.Lock(); c.states = append(c.states, s); c.mu.Unlock() }
	return cfg
}

func TestSession_JoinEventsClose(t *testing.T) {
	f := &fakeSignal{
		Occupants: []signaling.Occupant{{ID: "a", DisplayName: "Alice"}, {ID: "b", DisplayName: "Bob"}},
		Sources:   []signaling.RemoteSource{{SSRC: 1, Kind: "video", Name: "b-v0", Owner: "b"}},
	}
	useFake(t, f)
	var c collected
	s, err := Start(context.Background(), c.config(Config{
		MeetingURL: "https://meet.test/MyRoom", DisplayName: "Viewer",
		Selection: Selection{Pins: []string{"bob"}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if s.State() != StateJoined || s.Room() != "myroom" {
		t.Errorf("state %s room %s", s.State(), s.Room())
	}
	if got := len(s.Participants()); got != 2 {
		t.Errorf("%d participants, want 2", got)
	}
	p := f.LastPolicy()
	if p == nil || p.LastN != 9 || p.DefaultMaxHeight != 180 || len(p.Selected) != 1 || p.Selected[0] != "b-v0" || p.MaxHeights["b-v0"] != 720 {
		t.Errorf("policy %+v", p)
	}
	if f.Config()["xmpp_domain"] != "meet.test" || f.Config()["room_name"] != "myroom" || f.Config()["focus_jid"] != "focus.meet.test" {
		t.Errorf("signal config %v", f.Config())
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-s.Done():
	default:
		t.Fatal("Done not closed after Close")
	}
	if s.State() != StateClosed || s.Err() != nil || !f.Disconnected() {
		t.Errorf("after Close: state %s err %v disconnected %v", s.State(), s.Err(), f.Disconnected())
	}
	s.Close() // Idempotent
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.events) != 2 || c.events[0].Participant.Name != "Alice" || !c.events[1].Participant.Pinned {
		t.Errorf("events %+v", c.events)
	}
}

func TestSession_SignalingLostFails(t *testing.T) {
	f := &fakeSignal{}
	useFake(t, f)
	s, err := Start(context.Background(), Config{MeetingURL: "https://meet.test/r", DisplayName: "X", Logger: quietLogger})
	if err != nil {
		t.Fatal(err)
	}
	h := f.Handler()
	h.OnSignalingLost(errFake)
	select {
	case <-s.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("session did not end after signaling loss")
	}
	if s.State() != StateFailed || !errors.Is(s.Err(), errFake) {
		t.Errorf("state %s err %v", s.State(), s.Err())
	}
}

func TestSession_StartErrors(t *testing.T) {
	f := &fakeSignal{ConnectErr: jitsi.ErrAuthFailed}
	useFake(t, f)
	if _, err := Start(context.Background(), Config{MeetingURL: "https://meet.test/r", DisplayName: "X", Logger: quietLogger}); !errors.Is(err, jitsi.ErrAuthFailed) {
		t.Errorf("connect error: %v", err)
	}
	if _, err := Start(context.Background(), Config{MeetingURL: "https://meet.test/notjitsi", DisplayName: "X", Logger: quietLogger}); !errors.Is(err, jitsi.ErrNotJitsi) {
		t.Errorf("not jitsi: %v", err)
	}
	if _, err := Start(context.Background(), Config{MeetingURL: "https://meet.test/r", DisplayName: "X", Logger: quietLogger, Selection: Selection{Limit: 30}}); err == nil {
		t.Error("expected a selection error")
	}
	if _, err := Start(context.Background(), Config{MeetingURL: "https://meet.test/r", DisplayName: "X", Logger: quietLogger,
		FFmpeg: "/nonexistent/ffmpeg", FFmpegNeeds: []string{"encoder:libvpx"}}); err == nil {
		t.Error("expected a missing ffmpeg error")
	}
}

func TestSession_GuestUsesAnonymousDomain(t *testing.T) {
	f := &fakeSignal{}
	useFake(t, f)
	s, err := Start(context.Background(), Config{MeetingURL: "https://meet.test/r", DisplayName: "X", Logger: quietLogger, AnonymousDomain: "guest.meet.test"})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if f.Config()["xmpp_domain"] != "guest.meet.test" || f.Config()["focus_jid"] != "focus.meet.test" {
		t.Errorf("guest config %v", f.Config())
	}
}

func TestSession_TokenSources(t *testing.T) {
	f := &fakeSignal{}
	useFake(t, f)
	s, err := Start(context.Background(), Config{MeetingURL: "https://meet.test/r?jwt=fromurl", DisplayName: "X", Logger: quietLogger, AnonymousDomain: "guest.meet.test"})
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	// A token (here from the URL) means an authenticated user: the main
	// domain, not the guest one.
	if f.Config()["jwt"] != "fromurl" || f.Config()["xmpp_domain"] != "meet.test" {
		t.Errorf("config %v", f.Config())
	}
	s, err = Start(context.Background(), Config{MeetingURL: "https://meet.test/r", DisplayName: "X", Logger: quietLogger,
		AppID: "app", AppSecret: "sec", Moderator: true, Subject: "tenant", TokenTTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	if f.Config()["jwt_app_id"] != "app" || f.Config()["jwt_app_secret"] != "sec" || f.Config()["jwt_moderator"] != true || f.Config()["jwt_subject"] != "tenant" || f.Config()["jwt_ttl"] != "1h0m0s" {
		t.Errorf("config %v", f.Config())
	}
}
