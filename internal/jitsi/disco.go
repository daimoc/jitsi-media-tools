package jitsi

import (
	"crypto/sha1"
	"encoding/base64"
	"encoding/xml"
	"sort"
	"strings"

	"mellium.im/xmlstream"
	"mellium.im/xmpp/stanza"
)

const (
	nsDiscoInfo = "http://jabber.org/protocol/disco#info"
	nsCaps      = "http://jabber.org/protocol/caps"

	// capsNode identifies the bot software in entity capabilities (XEP-0115).
	capsNode = "https://github.com/daimoc/jitsi-media-tools"
)

// botIdentity is the service discovery identity (XEP-0030) of the bots.
var botIdentity = struct{ Category, Type, Name string }{"client", "bot", "jitsi-media-tools"}

// botFeatures are the features advertised to Jicofo, which selects what to
// offer from them (Jicofo's org.jitsi.jicofo.xmpp.Features). Features that
// change the signaling format (JSON-encoded sources, SSRC rewriting) are left
// out until the bots handle them.
var botFeatures = []string{
	nsCaps,
	nsDiscoInfo,
	nsJingle,    // urn:xmpp:jingle:1
	nsJingleRTP, // urn:xmpp:jingle:apps:rtp:1
	"urn:xmpp:jingle:apps:rtp:audio",
	"urn:xmpp:jingle:apps:rtp:video",
	nsJingleICEUDP, // urn:xmpp:jingle:transports:ice-udp:1
	"urn:xmpp:jingle:apps:dtls:0",
	"urn:ietf:rfc:5761",            // rtcp-mux
	"urn:ietf:rfc:5888",            // BUNDLE
	"urn:ietf:rfc:4588",            // RTX
	nsJingleSCTP,                   // Bridge channel over SCTP
	"http://jitsi.org/tcc",         // Transport-wide congestion control
	"http://jitsi.org/source-name", // Source names in source signaling
	// JSON-encoded sources: Jicofo then also keeps the offer's data (SCTP)
	// content, which it drops for XML-source clients (json_sources.go).
	"http://jitsi.org/json-encoded-sources",
	"urn:xmpp:ping", // XEP-0199
}

// capsVer is the XEP-0115 verification string of the bot.
var capsVer = computeCapsVer(botIdentity.Category, botIdentity.Type, botIdentity.Name, botFeatures)

// computeCapsVer computes an XEP-0115 §5.1 verification string for a single
// identity without language and no extended information.
func computeCapsVer(category, typ, name string, features []string) string {
	sorted := append([]string(nil), features...)
	sort.Strings(sorted)
	var s strings.Builder
	s.WriteString(category + "/" + typ + "//" + name + "<")
	for _, f := range sorted {
		s.WriteString(f + "<")
	}
	sum := sha1.Sum([]byte(s.String()))
	return base64.StdEncoding.EncodeToString(sum[:])
}

// capsElement builds the entity capabilities element for presences:
// <c xmlns="http://jabber.org/protocol/caps" hash="sha-1" node="..." ver="..."/>
func capsElement() xml.TokenReader {
	return xmlstream.Wrap(nil, xml.StartElement{
		Name: xml.Name{Space: nsCaps, Local: "c"},
		Attr: []xml.Attr{
			{Name: xml.Name{Local: "hash"}, Value: "sha-1"},
			{Name: xml.Name{Local: "node"}, Value: capsNode},
			{Name: xml.Name{Local: "ver"}, Value: capsVer},
		},
	})
}

// handleDiscoInfo answers disco#info queries (XEP-0030), addressed to the bot
// or to its caps node, with the bot's identity and features.
func (s *SignalingClient) handleDiscoInfo(iq stanza.IQ, t xmlstream.TokenReadEncoder, start *xml.StartElement) error {
	var node string
	for _, a := range start.Attr {
		if a.Name.Local == "node" {
			node = a.Value
		}
	}
	if node != "" && node != capsNode+"#"+capsVer {
		return replyIQ(t, iq.Error(stanza.Error{Type: stanza.Cancel, Condition: stanza.ItemNotFound}))
	}

	identity := xmlstream.Wrap(nil, xml.StartElement{
		Name: xml.Name{Local: "identity"},
		Attr: []xml.Attr{
			{Name: xml.Name{Local: "category"}, Value: botIdentity.Category},
			{Name: xml.Name{Local: "type"}, Value: botIdentity.Type},
			{Name: xml.Name{Local: "name"}, Value: botIdentity.Name},
		},
	})
	children := []xml.TokenReader{identity}
	for _, f := range botFeatures {
		children = append(children, xmlstream.Wrap(nil, xml.StartElement{
			Name: xml.Name{Local: "feature"},
			Attr: []xml.Attr{{Name: xml.Name{Local: "var"}, Value: f}},
		}))
	}
	queryStart := xml.StartElement{Name: xml.Name{Space: nsDiscoInfo, Local: "query"}}
	if node != "" {
		queryStart.Attr = []xml.Attr{{Name: xml.Name{Local: "node"}, Value: node}}
	}
	return replyIQ(t, iq.Result(xmlstream.Wrap(xmlstream.MultiReader(children...), queryStart)))
}
