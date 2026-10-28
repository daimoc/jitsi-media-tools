package jitsi

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	ws "nhooyr.io/websocket"
)

// fakeXMPPServer is a minimal XMPP-over-WebSocket server (RFC 7395) that
// performs SASL ANONYMOUS and resource binding, then hands every other
// top-level element to the test through a fakeConn.
type fakeXMPPServer struct {
	t      *testing.T
	domain string
	srv    *httptest.Server
	conns  chan *fakeConn
	// beforeBind is sent between the bind request and its result, as
	// meet.jit.si sends its disco#info message.
	beforeBind string
}

const testDomain = "meet.example.org"

func newFakeXMPPServer(t *testing.T) *fakeXMPPServer {
	t.Helper()
	s := &fakeXMPPServer{t: t, domain: testDomain, conns: make(chan *fakeConn, 4)}
	s.srv = httptest.NewServer(http.HandlerFunc(s.serveHTTP))
	t.Cleanup(s.srv.Close)
	return s
}

// url returns the WebSocket URL of the server.
func (s *fakeXMPPServer) url() string {
	return "ws" + strings.TrimPrefix(s.srv.URL, "http")
}

// accept returns the next client connection.
func (s *fakeXMPPServer) accept() *fakeConn {
	s.t.Helper()
	select {
	case c := <-s.conns:
		return c
	case <-time.After(5 * time.Second):
		s.t.Fatal("no client connected to the fake XMPP server")
		return nil
	}
}

// element is a top-level element received from the client.
type element struct {
	XMLName xml.Name
	Attrs   []xml.Attr `xml:",any,attr"`
	Inner   string     `xml:",innerxml"`
}

func (e element) attr(name string) string {
	for _, a := range e.Attrs {
		if a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}

func (e element) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "<%s", e.XMLName.Local)
	for _, a := range e.Attrs {
		fmt.Fprintf(&b, " %s=%q", a.Name.Local, a.Value)
	}
	fmt.Fprintf(&b, ">%s</%s>", e.Inner, e.XMLName.Local)
	return b.String()
}

// fakeConn is one client connection, after authentication and binding.
type fakeConn struct {
	t      *testing.T
	ws     *ws.Conn
	jid    string             // Full JID bound to the client
	nick   string             // MUC resource the client is expected to use
	in     chan element       // Stanzas sent by the client
	closed chan struct{}      // Closed when the client connection ends
	cancel context.CancelFunc // Drops the connection
	query  url.Values         // Query parameters of the WebSocket URL
}

func (s *fakeXMPPServer) serveHTTP(w http.ResponseWriter, r *http.Request) {
	conn, err := ws.Accept(w, r, &ws.AcceptOptions{Subprotocols: []string{"xmpp"}})
	if err != nil {
		s.t.Errorf("websocket accept: %v", err)
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	local := uuid.New().String() // Anonymous JIDs are UUIDs, as in Prosody
	c := &fakeConn{
		t:      s.t,
		ws:     conn,
		jid:    local + "@" + s.domain + "/res",
		nick:   local[:8],
		in:     make(chan element, 32),
		closed: make(chan struct{}),
		cancel: cancel,
		query:  r.URL.Query(),
	}

	// The client may split elements across messages: decode a byte stream.
	pr, pw := io.Pipe()
	go func() {
		defer pw.Close()
		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				return
			}
			if err := checkFrame(data); err != nil {
				s.t.Errorf("RFC 7395 framing violation: %v in message %q", err, data)
			}
			pw.Write(data)
		}
	}()

	go func() {
		defer close(c.closed)
		defer conn.Close(ws.StatusNormalClosure, "")
		defer cancel()
		dec := xml.NewDecoder(pr)
		authed, announced := false, false
		for {
			tok, err := dec.Token()
			if err != nil {
				return
			}
			start, ok := tok.(xml.StartElement)
			if !ok {
				continue
			}
			var e element
			if err := dec.DecodeElement(&e, &start); err != nil {
				return
			}
			switch e.XMLName.Local {
			case "open":
				c.write(ctx, `<open xmlns="urn:ietf:params:xml:ns:xmpp-framing" from="`+s.domain+`" id="`+fmt.Sprint(time.Now().UnixNano())+`" version="1.0" xml:lang="en"/>`)
				if !authed {
					c.write(ctx, `<stream:features xmlns:stream="http://etherx.jabber.org/streams"><mechanisms xmlns="urn:ietf:params:xml:ns:xmpp-sasl"><mechanism>ANONYMOUS</mechanism></mechanisms></stream:features>`)
				} else {
					c.write(ctx, `<stream:features xmlns:stream="http://etherx.jabber.org/streams"><bind xmlns="urn:ietf:params:xml:ns:xmpp-bind"/></stream:features>`)
				}
			case "auth":
				authed = true
				c.write(ctx, `<success xmlns="urn:ietf:params:xml:ns:xmpp-sasl"/>`)
			case "close":
				c.write(ctx, `<close xmlns="urn:ietf:params:xml:ns:xmpp-framing"/>`)
				return
			case "iq":
				if strings.Contains(e.Inner, "urn:ietf:params:xml:ns:xmpp-bind") {
					if s.beforeBind != "" {
						c.write(ctx, s.beforeBind)
					}
					c.write(ctx, `<iq xmlns="jabber:client" type="result" id="`+e.attr("id")+`"><bind xmlns="urn:ietf:params:xml:ns:xmpp-bind"><jid>`+c.jid+`</jid></bind></iq>`)
					if !announced {
						announced = true
						s.conns <- c
					}
					continue
				}
				c.in <- e
			default:
				c.in <- e
			}
		}
	}()
}

// checkFrame checks that a WebSocket message holds exactly one complete
// top-level element, as RFC 7395 requires (servers such as Prosody close the
// connection otherwise).
func checkFrame(data []byte) error {
	dec := xml.NewDecoder(bytes.NewReader(data))
	depth, elements := 0, 0
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		switch tok := tok.(type) {
		case xml.StartElement:
			if depth == 0 {
				elements++
			}
			depth++
		case xml.EndElement:
			depth--
		case xml.CharData:
			if depth == 0 && len(bytes.TrimSpace(tok)) > 0 {
				return fmt.Errorf("text outside of an element")
			}
		}
	}
	if depth != 0 || elements != 1 {
		return fmt.Errorf("%d top-level elements, depth %d at end", elements, depth)
	}
	return nil
}

func (c *fakeConn) write(ctx context.Context, s string) {
	if err := c.ws.Write(ctx, ws.MessageText, []byte(s)); err != nil && ctx.Err() == nil {
		c.t.Logf("fake server write: %v", err)
	}
}

// send writes a stanza to the client.
func (c *fakeConn) send(s string) {
	c.write(context.Background(), s)
}

// next returns the next stanza from the client.
func (c *fakeConn) next() element {
	c.t.Helper()
	select {
	case e := <-c.in:
		return e
	case <-time.After(5 * time.Second):
		c.t.Fatal("timed out waiting for a stanza from the client")
		return element{}
	}
}

// expect returns the next stanza for which match is true, skipping (and
// acknowledging, if they are set IQs) the others.
func (c *fakeConn) expect(what string, match func(element) bool) element {
	c.t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case e := <-c.in:
			if match(e) {
				return e
			}
			c.t.Logf("skipping %s", e)
			if e.XMLName.Local == "iq" && e.attr("type") == "set" {
				c.result(e)
			}
		case <-deadline:
			c.t.Fatalf("timed out waiting for %s", what)
			return element{}
		}
	}
}

// result acknowledges an IQ.
func (c *fakeConn) result(iq element) {
	c.send(`<iq xmlns="jabber:client" type="result" id="` + iq.attr("id") + `" to="` + c.jid + `" from="` + iq.attr("to") + `"/>`)
}

// waitClosed waits until the client has closed the connection.
func (c *fakeConn) waitClosed() {
	c.t.Helper()
	select {
	case <-c.closed:
	case <-time.After(5 * time.Second):
		c.t.Fatal("client did not close the connection")
	}
}

// drop closes the connection from the server side, as a crashed server would.
func (c *fakeConn) drop() {
	c.cancel()
	c.ws.Close(ws.StatusGoingAway, "server going away")
}

func isIQ(typ, payloadNS string) func(element) bool {
	return func(e element) bool {
		return e.XMLName.Local == "iq" && e.attr("type") == typ && strings.Contains(e.Inner, payloadNS)
	}
}

func isJingle(action string) func(element) bool {
	return func(e element) bool {
		return e.XMLName.Local == "iq" && strings.Contains(e.Inner, `action="`+action+`"`)
	}
}

func isPresence(typ string) func(element) bool {
	return func(e element) bool {
		return e.XMLName.Local == "presence" && e.attr("type") == typ
	}
}
