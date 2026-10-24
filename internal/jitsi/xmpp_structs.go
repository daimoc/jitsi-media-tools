package jitsi

import (
	"encoding/xml"
	"io"
)

// Jingle-related XML namespaces.
const (
	nsJingle         = "urn:xmpp:jingle:1"
	nsJingleRTP      = "urn:xmpp:jingle:apps:rtp:1"
	nsJingleICEUDP   = "urn:xmpp:jingle:transports:ice-udp:1"
	nsJingleDTLS     = "urn:xmpp:jingle:apps:dtls:0"
	nsJingleSCTP     = "urn:xmpp:jingle:transports:dtls-sctp:1"
	nsJingleRtcpFb   = "urn:xmpp:jingle:apps:rtp:rtcp-fb:0"
	nsJingleGrouping = "urn:xmpp:jingle:apps:grouping:0"
)

// JingleIQ represents the 'jingle' element within an IQ stanza.
// Namespace: urn:xmpp:jingle:1
type JingleIQ struct {
	XMLName   xml.Name        `xml:"urn:xmpp:jingle:1 jingle"`
	Action    string          `xml:"action,attr"`
	SID       string          `xml:"sid,attr"`
	Initiator string          `xml:"initiator,attr,omitempty"`
	Responder string          `xml:"responder,attr,omitempty"`
	Content   []JingleContent `xml:"content"`
	Groups    []JingleGroup   `xml:"urn:xmpp:jingle:apps:grouping:0 group"`
	Reason    *JingleReason   `xml:"reason,omitempty"`
	// JSONMessage carries the remote sources instead of the descriptions
	// for clients advertising JSON-encoded sources (see json_sources.go).
	JSONMessage *JSONMessage `xml:"http://jitsi.org/jitmeet json-message,omitempty"`
}

// JSONMessage is Jitsi's <json-message xmlns="http://jitsi.org/jitmeet">.
type JSONMessage struct {
	Text string `xml:",chardata"`
}

// JingleGroup represents a content group (XEP-0338), e.g. BUNDLE.
type JingleGroup struct {
	Semantics string             `xml:"semantics,attr"`
	Contents  []JingleGroupEntry `xml:"content"`
}

// JingleGroupEntry names a content that belongs to a group.
type JingleGroupEntry struct {
	Name string `xml:"name,attr"`
}

// JingleContent represents the 'content' element.
type JingleContent struct {
	Creator     string             `xml:"creator,attr"`
	Name        string             `xml:"name,attr"`
	Senders     string             `xml:"senders,attr,omitempty"`
	Description *JingleDescription `xml:"description,omitempty"`
	Transport   *JingleTransport   `xml:"transport,omitempty"`
}

// JingleDescription represents the 'description' element.
// Namespace: urn:xmpp:jingle:apps:rtp:1 (XEP-0167)
type JingleDescription struct {
	XMLName      xml.Name      `xml:"urn:xmpp:jingle:apps:rtp:1 description"`
	Media        string        `xml:"media,attr"`
	SSRC         string        `xml:"ssrc,attr,omitempty"`
	PayloadTypes []PayloadType `xml:"payload-type"`
	HdrExts      []RtpHdrExt   `xml:"urn:xmpp:jingle:apps:rtp:rtp-hdrext:0 rtp-hdrext"`
	Sources      []Source      `xml:"urn:xmpp:jingle:apps:rtp:ssma:0 source"`
	SSRCGroups   []SSRCGroup   `xml:"urn:xmpp:jingle:apps:rtp:ssma:0 ssrc-group"`
	RtcpMux      *RtcpMux      `xml:"rtcp-mux,omitempty"`
}

// RtcpMux represents the 'rtcp-mux' element.
type RtcpMux struct{}

// PayloadType represents the 'payload-type' element.
type PayloadType struct {
	ID         string      `xml:"id,attr"`
	Name       string      `xml:"name,attr"`
	ClockRate  string      `xml:"clockrate,attr,omitempty"`
	Channels   string      `xml:"channels,attr,omitempty"`
	Parameters []Parameter `xml:"parameter"`
	RtcpFb     []RtcpFb    `xml:"urn:xmpp:jingle:apps:rtp:rtcp-fb:0 rtcp-fb"`
}

// Parameter represents a generic parameter.
type Parameter struct {
	Name  string `xml:"name,attr"`
	Value string `xml:"value,attr,omitempty"`
}

// RtcpFb represents the 'rtcp-fb' element (XEP-0293).
type RtcpFb struct {
	Type    string `xml:"type,attr"`
	Subtype string `xml:"subtype,attr,omitempty"`
}

// RtpHdrExt represents the 'rtp-hdrext' element (XEP-0294).
type RtpHdrExt struct {
	ID  string `xml:"id,attr"`
	URI string `xml:"uri,attr"`
}

// Source represents an SSRC 'source' element (XEP-0339).
type Source struct {
	SSRC       string      `xml:"ssrc,attr"`
	Name       string      `xml:"name,attr,omitempty"`
	VideoType  string      `xml:"videoType,attr,omitempty"` // Jitsi: "camera" or "desktop"
	Parameters []Parameter `xml:"parameter"`
	SSRCInfo   *SSRCInfo   `xml:"http://jitsi.org/jitmeet ssrc-info"`
}

// SSRCInfo is Jitsi's <ssrc-info/> extension naming the occupant that owns a
// source ("jvb" for the bridge's own sources).
type SSRCInfo struct {
	Owner string `xml:"owner,attr"`
}

// SSRCGroup represents an 'ssrc-group' element (XEP-0339), e.g. FID or SIM.
type SSRCGroup struct {
	Semantics string        `xml:"semantics,attr"`
	Sources   []SSRCGroupID `xml:"source"`
}

// SSRCGroupID references a source of an SSRC group.
type SSRCGroupID struct {
	SSRC string `xml:"ssrc,attr"`
}

// JingleTransport represents the 'transport' element.
// Namespace: urn:xmpp:jingle:transports:ice-udp:1 (XEP-0176)
type JingleTransport struct {
	XMLName      xml.Name          `xml:"urn:xmpp:jingle:transports:ice-udp:1 transport"`
	Ufrag        string            `xml:"ufrag,attr,omitempty"`
	Pwd          string            `xml:"pwd,attr,omitempty"`
	Fingerprints []Fingerprint     `xml:"urn:xmpp:jingle:apps:dtls:0 fingerprint"`
	SctpMap      *SctpMap          `xml:"urn:xmpp:jingle:transports:dtls-sctp:1 sctpmap,omitempty"`
	RtcpMux      *RtcpMux          `xml:"rtcp-mux,omitempty"`
	Candidates   []JingleCandidate `xml:"candidate"`
}

// Fingerprint represents the 'fingerprint' element (XEP-0320).
type Fingerprint struct {
	Hash  string `xml:"hash,attr"`
	Setup string `xml:"setup,attr,omitempty"`
	Value string `xml:",chardata"`
}

// SctpMap represents the 'sctpmap' element used by Jitsi for the data channel.
type SctpMap struct {
	Number   string `xml:"number,attr"`
	Protocol string `xml:"protocol,attr"`
	Streams  string `xml:"streams,attr,omitempty"`
}

// JingleCandidate represents a candidate in the transport.
type JingleCandidate struct {
	Component  string `xml:"component,attr"`
	Foundation string `xml:"foundation,attr"`
	Generation string `xml:"generation,attr"`
	ID         string `xml:"id,attr"`
	IP         string `xml:"ip,attr"`
	Network    string `xml:"network,attr,omitempty"`
	Port       string `xml:"port,attr"`
	Priority   string `xml:"priority,attr"`
	Protocol   string `xml:"protocol,attr"`
	Type       string `xml:"type,attr"`
	RelAddr    string `xml:"rel-addr,attr,omitempty"`
	RelPort    string `xml:"rel-port,attr,omitempty"`
	TCPType    string `xml:"tcptype,attr,omitempty"`
}

// JingleReason represents the 'reason' element (XEP-0166 §7.4).
// The condition is encoded as the name of an empty child element, e.g.
// <reason><success/><text>optional</text></reason>, so it needs custom XML
// (un)marshalling.
type JingleReason struct {
	Condition string // e.g. "success", "decline", "busy"
	Text      string // Optional human-readable text
}

// MarshalXML encodes the reason with the condition as a child element name.
func (r JingleReason) MarshalXML(e *xml.Encoder, start xml.StartElement) error {
	start.Name = xml.Name{Local: "reason"}
	if err := e.EncodeToken(start); err != nil {
		return err
	}
	if r.Condition != "" {
		cond := xml.StartElement{Name: xml.Name{Local: r.Condition}}
		if err := e.EncodeToken(cond); err != nil {
			return err
		}
		if err := e.EncodeToken(cond.End()); err != nil {
			return err
		}
	}
	if r.Text != "" {
		text := xml.StartElement{Name: xml.Name{Local: "text"}}
		if err := e.EncodeElement(r.Text, text); err != nil {
			return err
		}
	}
	return e.EncodeToken(start.End())
}

// UnmarshalXML decodes the reason, taking the first non-<text> child element
// name as the condition.
func (r *JingleReason) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	for {
		tok, err := d.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local == "text" {
				if err := d.DecodeElement(&r.Text, &t); err != nil {
					return err
				}
				continue
			}
			if r.Condition == "" {
				r.Condition = t.Name.Local
			}
			if err := d.Skip(); err != nil {
				return err
			}
		case xml.EndElement:
			if t.Name == start.Name {
				return nil
			}
		}
	}
}
