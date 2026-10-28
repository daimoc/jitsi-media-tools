package jitsi

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"math/rand"
	"strconv"

	"mellium.im/xmlstream"
	"mellium.im/xmpp"
	"mellium.im/xmpp/jid"
	"mellium.im/xmpp/stanza"
)

const nsBind = "urn:ietf:params:xml:ns:xmpp-bind"

// maxBeforeBind bounds the stanzas skipped while waiting for the bind
// result.
const maxBeforeBind = 32

// bindResource is mellium's client resource binding, except that it skips
// the stanzas a server sends before the bind result: meet.jit.si sends a
// message listing its components (visitors, lobby…), which mellium's
// BindResource rejects as bad-format.
func bindResource() xmpp.StreamFeature {
	return xmpp.StreamFeature{
		Name:       xml.Name{Space: nsBind, Local: "bind"},
		Necessary:  xmpp.Authn,
		Prohibited: xmpp.Ready,
		List: func(context.Context, xmlstream.TokenWriter, xml.StartElement) (bool, error) {
			return true, nil // Client only
		},
		Parse: func(_ context.Context, d *xml.Decoder, _ *xml.StartElement) (bool, interface{}, error) {
			return true, nil, d.Skip()
		},
		Negotiate: func(_ context.Context, session *xmpp.Session, _ interface{}) (xmpp.SessionState, io.ReadWriter, error) {
			r := session.TokenReader()
			defer r.Close()
			d := xml.NewTokenDecoder(r)
			w := session.TokenWriter()
			defer w.Close()

			id := strconv.FormatUint(rand.Uint64(), 16)
			req := stanza.IQ{XMLName: xml.Name{Space: stanza.NSClient, Local: "iq"}, ID: id, Type: stanza.SetIQ}.Wrap(xmlstream.Wrap(nil, xml.StartElement{Name: xml.Name{Space: nsBind, Local: "bind"}}))
			if _, err := xmlstream.Copy(w, req); err != nil {
				return 0, nil, err
			}
			if err := w.Flush(); err != nil {
				return 0, nil, err
			}
			for range maxBeforeBind {
				tok, err := d.Token()
				if err != nil {
					return 0, nil, err
				}
				start, ok := tok.(xml.StartElement)
				if !ok {
					continue // Whitespace between stanzas
				}
				if start.Name.Local != "iq" || attrValue(start, "id") != id {
					if err := d.Skip(); err != nil {
						return 0, nil, err
					}
					continue
				}
				var resp struct {
					stanza.IQ
					JID string        `xml:"urn:ietf:params:xml:ns:xmpp-bind bind>jid"`
					Err *stanza.Error `xml:"error"`
				}
				if err := d.DecodeElement(&resp, &start); err != nil {
					return 0, nil, err
				}
				if resp.Type != stanza.ResultIQ {
					if resp.Err != nil {
						return 0, nil, *resp.Err
					}
					return 0, nil, fmt.Errorf("bind: %s reply", resp.Type)
				}
				j, err := jid.Parse(resp.JID)
				if err != nil {
					return 0, nil, fmt.Errorf("bind: %w", err)
				}
				session.UpdateAddr(j)
				return xmpp.Ready, nil, nil
			}
			return 0, nil, fmt.Errorf("bind: no result after %d stanzas", maxBeforeBind)
		},
	}
}

// attrValue returns the value of start's attribute name, or "".
func attrValue(start xml.StartElement, name string) string {
	for _, a := range start.Attr {
		if a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}
