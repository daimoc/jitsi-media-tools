package jitsi

import (
	"strings"
	"testing"
)

// XEP-0115 §5.2 example.
func TestComputeCapsVer(t *testing.T) {
	got := computeCapsVer("client", "pc", "Exodus 0.9.1", []string{
		"http://jabber.org/protocol/muc",
		"http://jabber.org/protocol/disco#info",
		"http://jabber.org/protocol/caps",
		"http://jabber.org/protocol/disco#items",
	})
	if got != "QgayPKawpkPSDYmwT/WM94uAlu0=" {
		t.Errorf("computeCapsVer = %q", got)
	}
}

// Jicofo discovers each participant's features (directly or through its caps
// node) to decide what to offer: SCTP for the bridge channel in particular.
func TestAdapter_AnswersDiscoInfo(t *testing.T) {
	srv := newFakeXMPPServer(t)
	a := newTestAdapter(t, srv, nil)
	c := connectPeer(t, srv, a, "peer1", newPionHandler(t))
	defer a.Disconnect("peer1")

	for _, node := range []string{"", capsNode + "#" + capsVer} {
		nodeAttr := ""
		if node != "" {
			nodeAttr = ` node="` + node + `"`
		}
		c.send(`<iq xmlns="jabber:client" type="get" id="disco1" from="` + testFocusMUC + `" to="` + c.jid + `"><query xmlns="http://jabber.org/protocol/disco#info"` + nodeAttr + `/></iq>`)
		reply := c.expect("disco#info result", isResult("disco1"))
		for _, want := range []string{
			`category="client"`, `type="bot"`,
			`var="urn:xmpp:jingle:transports:dtls-sctp:1"`, `var="urn:xmpp:jingle:apps:rtp:audio"`, `var="urn:ietf:rfc:5888"`,
		} {
			if !strings.Contains(reply.Inner, want) {
				t.Errorf("node %q: disco#info result lacks %s: %s", node, want, reply.Inner)
			}
		}
		if node != "" && !strings.Contains(reply.Inner, `node="`+node+`"`) {
			t.Errorf("the result must echo the queried node, got %s", reply.Inner)
		}
	}

	c.send(`<iq xmlns="jabber:client" type="get" id="disco2" from="` + testFocusMUC + `" to="` + c.jid + `"><query xmlns="http://jabber.org/protocol/disco#info" node="other#node"/></iq>`)
	if reply := c.expect("disco#info error", func(e element) bool { return e.attr("id") == "disco2" }); reply.attr("type") != "error" {
		t.Errorf("unknown node must be an error, got %s", reply)
	}
}

// meet.jit.si sends a message (its components, as disco#info) before the
// result of the bind request: the connection must still be set up.
func TestAdapter_StanzaBeforeBindResult(t *testing.T) {
	srv := newFakeXMPPServer(t)
	srv.beforeBind = `<message xmlns="jabber:client" from="` + testDomain + `"><query xmlns="http://jabber.org/protocol/disco#info"><identity category="component" name="visitors.` + testDomain + `" type="visitors"/></query></message>`
	a := newTestAdapter(t, srv, nil)
	connectPeer(t, srv, a, "peer1", newPionHandler(t))
	a.Disconnect("peer1")
}
