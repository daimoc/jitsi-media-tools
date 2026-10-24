package jitsi

import (
	"context"
	"errors"
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
