package jitsi

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// waitingHandler is a handler that waits for a host.
type waitingHandler struct {
	*pionHandler
	waits atomic.Int32
}

func (h *waitingHandler) OnWaitingForHost() { h.waits.Add(1) }

// refuseConference answers the conference request with not-authorized, as
// Jicofo does for a guest when no host started the conference.
func (c *fakeConn) refuseConference() {
	c.t.Helper()
	conf := c.expect("conference request", isIQ("set", nsFocus))
	c.send(`<iq xmlns="jabber:client" type="error" id="` + conf.attr("id") + `" from="focus.` + testDomain + `" to="` + c.jid + `">` +
		`<conference xmlns="http://jitsi.org/protocol/focus" room="` + testRoomJID + `"/>` +
		`<error type="cancel"><not-authorized xmlns="urn:ietf:params:xml:ns:xmpp-stanzas"/></error></iq>`)
}

func TestAdapter_GuestWaitsForHost(t *testing.T) {
	old := hostRetry
	hostRetry = 50 * time.Millisecond
	t.Cleanup(func() { hostRetry = old })

	srv := newFakeXMPPServer(t)
	a := newTestAdapter(t, srv, nil)
	h := &waitingHandler{pionHandler: newPionHandler(t)}
	errc := make(chan error, 1)
	go func() { errc <- a.Connect(context.Background(), "guest", h) }()
	c := srv.accept()
	defer a.Disconnect("guest")

	c.refuseConference()
	c.refuseConference()
	c.answerConference(true)
	c.expectJoin("guest")
	c.confirmJoin()
	if err := waitErr(t, errc, "Connect"); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if n := h.waits.Load(); n != 1 {
		t.Errorf("OnWaitingForHost called %d times, want 1", n)
	}
}

func TestAdapter_RefusedWithoutWaitingIsAuthFailure(t *testing.T) {
	srv := newFakeXMPPServer(t)
	a := newTestAdapter(t, srv, nil)
	errc := make(chan error, 1)
	go func() { errc <- a.Connect(context.Background(), "peer1", newPionHandler(t)) }()
	c := srv.accept()
	c.refuseConference()
	err := waitErr(t, errc, "Connect")
	if !errors.Is(err, ErrAuthFailed) {
		t.Errorf("Connect = %v, want ErrAuthFailed", err)
	}
}

// refuseJoin answers the join presence as meet.jit.si does for a guest when
// no host is in the room (waiting) or when the room has a lobby.
func (c *fakeConn) refuseJoin(waiting bool) {
	c.t.Helper()
	c.expectJoin("guest")
	wait := ""
	if waiting {
		wait = `<waiting-for-host xmlns="http://jitsi.org/jitmeet"/>`
	}
	c.send(`<presence xmlns="jabber:client" type="error" from="` + testRoomJID + `/` + c.nick + `" to="` + c.jid + `">` +
		`<error type="auth" code="407"><registration-required xmlns="urn:ietf:params:xml:ns:xmpp-stanzas"/>` + wait +
		`<lobbyroom xmlns="http://jitsi.org/jitmeet">room@lobby.` + testDomain + `</lobbyroom></error><x xmlns="http://jabber.org/protocol/muc"/></presence>`)
}

// meet.jit.si refuses a guest's join until a host is in the room: the guest
// waits and joins again.
func TestAdapter_GuestWaitsForHostInRoom(t *testing.T) {
	old := hostRetry
	hostRetry = 50 * time.Millisecond
	t.Cleanup(func() { hostRetry = old })

	srv := newFakeXMPPServer(t)
	a := newTestAdapter(t, srv, nil)
	h := &waitingHandler{pionHandler: newPionHandler(t)}
	errc := make(chan error, 1)
	go func() { errc <- a.Connect(context.Background(), "guest", h) }()
	c := srv.accept()
	defer a.Disconnect("guest")

	c.answerConference(true)
	c.refuseJoin(true)
	c.answerConference(true)
	c.refuseJoin(true)
	c.answerConference(true)
	c.expectJoin("guest")
	c.confirmJoin()
	if err := waitErr(t, errc, "Connect"); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if n := h.waits.Load(); n != 1 {
		t.Errorf("OnWaitingForHost called %d times, want 1", n)
	}
}

// A room with a lobby, where a moderator admits each guest, is refused
// with a message saying so.
func TestAdapter_LobbyRefused(t *testing.T) {
	srv := newFakeXMPPServer(t)
	a := newTestAdapter(t, srv, nil)
	h := &waitingHandler{pionHandler: newPionHandler(t)}
	errc := make(chan error, 1)
	go func() { errc <- a.Connect(context.Background(), "guest", h) }()
	c := srv.accept()
	c.answerConference(true)
	c.refuseJoin(false)
	err := waitErr(t, errc, "Connect")
	if !errors.Is(err, ErrRoomUnavailable) || !strings.Contains(err.Error(), "lobby") {
		t.Errorf("Connect = %v, want ErrRoomUnavailable naming the lobby", err)
	}
}
