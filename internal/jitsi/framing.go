package jitsi

import (
	"bytes"
	"net"
	"sync"
)

// wsCloseElement is the RFC 7395 stream close, sent instead of the RFC 6120
// stream footer.
var wsCloseElement = []byte(`<close xmlns="urn:ietf:params:xml:ns:xmpp-framing"/>`)

// stanzaFramer adapts the XML stream written by the XMPP session to RFC 7395
// framing, where every WebSocket message carries exactly one complete
// top-level element. The session writes through a 4 KiB buffer, so a large
// stanza (a session-accept is around 10 KiB) arrives in several writes, which
// servers such as Prosody reject when sent as separate messages.
//
// Writes are buffered until a top-level element is complete. The scanner
// relies on '<' and '>' being escaped in character data and attribute values,
// which the encoder guarantees; comments and CDATA are not emitted.
type stanzaFramer struct {
	net.Conn

	mu       sync.Mutex
	buf      []byte
	depth    int  // Open elements below the stream level
	inTag    bool // Between '<' and '>'
	tagStart bool // Just after '<'
	closing  bool // Current tag is an end tag
	special  bool // Current tag is a declaration or processing instruction
	prev     byte
}

func newStanzaFramer(c net.Conn) *stanzaFramer {
	return &stanzaFramer{Conn: c}
}

// Write implements net.Conn. Each complete top-level element in the data is
// sent as its own message; an incomplete tail is kept for the next write.
func (f *stanzaFramer) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	start := len(f.buf)
	f.buf = append(f.buf, p...)
	sent := 0
	for i := start; i < len(f.buf); i++ {
		if !f.scan(f.buf[i]) {
			continue
		}
		msg := bytes.TrimSpace(f.buf[sent : i+1])
		sent = i + 1
		if len(msg) == 0 {
			continue
		}
		if bytes.Equal(msg, []byte("</stream:stream>")) {
			msg = wsCloseElement
		}
		if _, err := f.Conn.Write(msg); err != nil {
			f.buf = f.buf[:0]
			return 0, err
		}
	}
	f.buf = append(f.buf[:0], f.buf[sent:]...)
	return len(p), nil
}

// scan advances the tokenizer by one byte and reports whether it completed a
// top-level element.
func (f *stanzaFramer) scan(c byte) bool {
	defer func() { f.prev = c }()
	if !f.inTag {
		if c == '<' {
			f.inTag, f.tagStart, f.closing, f.special = true, true, false, false
		}
		return false
	}
	if f.tagStart {
		f.tagStart = false
		switch c {
		case '/':
			f.closing = true
		case '?', '!':
			f.special = true
		}
	}
	if c != '>' {
		return false
	}
	f.inTag = false
	switch {
	case f.special:
	case f.closing:
		f.depth--
	case f.prev == '/': // Empty element
	default:
		f.depth++
	}
	if f.depth <= 0 {
		f.depth = 0
		return true
	}
	return false
}
