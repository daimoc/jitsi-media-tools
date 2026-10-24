package jitsi

import (
	"net"
	"reflect"
	"testing"
)

// recordConn records each Write as one message.
type recordConn struct {
	net.Conn
	msgs []string
}

func (c *recordConn) Write(p []byte) (int, error) {
	c.msgs = append(c.msgs, string(p))
	return len(p), nil
}

func TestStanzaFramer(t *testing.T) {
	for name, tc := range map[string]struct {
		writes []string
		want   []string
	}{
		"stanza split across writes": {
			writes: []string{`<iq type="set"><jingle a="x/y">`, `<content name="0"/></jin`, `gle></iq>`},
			want:   []string{`<iq type="set"><jingle a="x/y"><content name="0"/></jingle></iq>`},
		},
		"several stanzas in one write": {
			writes: []string{`<presence/><iq id="1"><ping/></iq><message><body>a &gt; b</body></message>`},
			want:   []string{`<presence/>`, `<iq id="1"><ping/></iq>`, `<message><body>a &gt; b</body></message>`},
		},
		"stream open and whitespace": {
			writes: []string{`<?xml version="1.0"?>`, `<open xmlns="urn:ietf:params:xml:ns:xmpp-framing" to="d"/>`, " ", `<iq/>`},
			want:   []string{`<?xml version="1.0"?>`, `<open xmlns="urn:ietf:params:xml:ns:xmpp-framing" to="d"/>`, `<iq/>`},
		},
		"stream footer becomes close": {
			writes: []string{`</stream:stream>`},
			want:   []string{string(wsCloseElement)},
		},
	} {
		t.Run(name, func(t *testing.T) {
			rc := &recordConn{}
			f := newStanzaFramer(rc)
			for _, w := range tc.writes {
				if n, err := f.Write([]byte(w)); err != nil || n != len(w) {
					t.Fatalf("Write(%q) = %d, %v", w, n, err)
				}
			}
			if !reflect.DeepEqual(rc.msgs, tc.want) {
				t.Errorf("messages:\n got %q\nwant %q", rc.msgs, tc.want)
			}
		})
	}
}
