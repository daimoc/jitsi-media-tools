package jitsi

import (
	"fmt"
	"hash/fnv"
	"math/rand"
	"strconv"
	"strings"

	"github.com/pion/sdp/v3"
	"github.com/pion/webrtc/v3"
)

// Jingle <-> SDP translation, following the mapping used by lib-jitsi-meet
// (XEP-0167 RTP, XEP-0176 ICE-UDP, XEP-0320 DTLS, XEP-0338 grouping,
// XEP-0339 SSRC, plus Jitsi's sctpmap for the data channel).

// convertJingleToSDP converts a Jingle offer (session-initiate) to an SDP
// offer, as is. Sessions use sessionSources, which splits remote sources into
// their own sections before rendering.
func convertJingleToSDP(jingle *JingleIQ) (*webrtc.SessionDescription, error) {
	return renderSDP(jingle, 2)
}

// renderSDP renders a Jingle description as an SDP offer with the given o=
// session version.
func renderSDP(jingle *JingleIQ, version uint64) (*webrtc.SessionDescription, error) {
	var b strings.Builder
	line := func(format string, a ...any) {
		fmt.Fprintf(&b, format, a...)
		b.WriteString("\r\n")
	}

	// o= requires a numeric session id; derive a stable one from the SID.
	h := fnv.New64a()
	h.Write([]byte(jingle.SID))
	line("v=0")
	line("o=- %d %d IN IP4 0.0.0.0", h.Sum64()>>1, version)
	line("s=-")
	line("t=0 0")

	emitted := map[string]bool{}
	var media strings.Builder
	for _, content := range jingle.Content {
		section, ok := contentToSDP(content)
		if !ok {
			continue
		}
		emitted[content.Name] = true
		media.WriteString(section)
	}
	if len(emitted) == 0 {
		return nil, fmt.Errorf("jingle %s carries no RTP or SCTP content", jingle.Action)
	}

	for _, g := range jingle.Groups {
		var names []string
		for _, c := range g.Contents {
			if emitted[c.Name] {
				names = append(names, c.Name)
			}
		}
		if len(names) > 0 {
			line("a=group:%s %s", g.Semantics, strings.Join(names, " "))
		}
	}
	line("a=msid-semantic: WMS *")
	b.WriteString(media.String())

	return &webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: b.String()}, nil
}

// contentToSDP renders one Jingle content as an SDP media section. It reports
// false for contents that carry neither an RTP description nor an SCTP map.
func contentToSDP(content JingleContent) (string, bool) {
	var b strings.Builder
	line := func(format string, a ...any) {
		fmt.Fprintf(&b, format, a...)
		b.WriteString("\r\n")
	}

	desc, tr := content.Description, content.Transport
	switch {
	case desc != nil && len(desc.PayloadTypes) > 0:
		ids := make([]string, len(desc.PayloadTypes))
		for i, pt := range desc.PayloadTypes {
			ids[i] = pt.ID
		}
		line("m=%s 9 UDP/TLS/RTP/SAVPF %s", desc.Media, strings.Join(ids, " "))
	case tr != nil && tr.SctpMap != nil:
		line("m=application 9 UDP/DTLS/SCTP webrtc-datachannel")
	default:
		return "", false
	}
	line("c=IN IP4 0.0.0.0")

	if tr != nil {
		if tr.Ufrag != "" {
			line("a=ice-ufrag:%s", tr.Ufrag)
		}
		if tr.Pwd != "" {
			line("a=ice-pwd:%s", tr.Pwd)
		}
		setup := "actpass"
		for _, fp := range tr.Fingerprints {
			line("a=fingerprint:%s %s", fp.Hash, strings.TrimSpace(fp.Value))
			if fp.Setup != "" {
				setup = fp.Setup
			}
		}
		line("a=setup:%s", setup)
	}
	line("a=mid:%s", content.Name)

	if desc == nil {
		line("a=sctp-port:%s", tr.SctpMap.Number)
	} else {
		line("a=%s", sendersToDirection(content.Senders))
		line("a=rtcp-mux") // Jitsi always multiplexes RTCP
		for _, ext := range desc.HdrExts {
			line("a=extmap:%s %s", ext.ID, ext.URI)
		}
		for _, pt := range desc.PayloadTypes {
			rtpmap := pt.Name + "/" + pt.ClockRate
			if pt.Channels != "" && pt.Channels != "1" {
				rtpmap += "/" + pt.Channels
			}
			line("a=rtpmap:%s %s", pt.ID, rtpmap)
			if len(pt.Parameters) > 0 {
				params := make([]string, len(pt.Parameters))
				for i, p := range pt.Parameters {
					if p.Name == "" {
						params[i] = p.Value
					} else {
						params[i] = p.Name + "=" + p.Value
					}
				}
				line("a=fmtp:%s %s", pt.ID, strings.Join(params, ";"))
			}
			for _, fb := range pt.RtcpFb {
				line("a=rtcp-fb:%s %s", pt.ID, strings.TrimSpace(fb.Type+" "+fb.Subtype))
			}
		}
		for _, g := range desc.SSRCGroups {
			ssrcs := make([]string, len(g.Sources))
			for i, s := range g.Sources {
				ssrcs[i] = s.SSRC
			}
			line("a=ssrc-group:%s %s", g.Semantics, strings.Join(ssrcs, " "))
		}
		for _, src := range desc.Sources {
			if len(src.Parameters) == 0 {
				line("a=ssrc:%s cname:%s", src.SSRC, firstNonEmpty(src.Name, src.SSRC))
			}
			for _, p := range src.Parameters {
				if p.Value == "" {
					line("a=ssrc:%s %s", src.SSRC, p.Name)
				} else {
					line("a=ssrc:%s %s:%s", src.SSRC, p.Name, p.Value)
				}
			}
		}
	}

	if tr != nil {
		for _, c := range tr.Candidates {
			line("a=%s", candidateToSDP(c))
		}
	}
	return b.String(), true
}

// sendersToDirection maps Jingle senders (offerer's view) to an SDP direction.
func sendersToDirection(senders string) string {
	switch senders {
	case "initiator":
		return "sendonly"
	case "responder":
		return "recvonly"
	case "none":
		return "inactive"
	default:
		return "sendrecv"
	}
}

// directionToSenders maps the responder's SDP answer direction to Jingle senders.
func directionToSenders(md *sdp.MediaDescription) string {
	for _, a := range md.Attributes {
		switch a.Key {
		case "recvonly":
			return "initiator"
		case "sendonly":
			return "responder"
		case "inactive":
			return "none"
		case "sendrecv":
			return "both"
		}
	}
	return "both"
}

// candidateToSDP renders a Jingle candidate as an SDP "candidate:" attribute value.
func candidateToSDP(c JingleCandidate) string {
	var b strings.Builder
	fmt.Fprintf(&b, "candidate:%s %s %s %s %s %s typ %s",
		firstNonEmpty(c.Foundation, c.ID, "1"), c.Component, strings.ToLower(c.Protocol), c.Priority, c.IP, c.Port, c.Type)
	if c.RelAddr != "" {
		fmt.Fprintf(&b, " raddr %s rport %s", c.RelAddr, c.RelPort)
	}
	if c.TCPType != "" {
		fmt.Fprintf(&b, " tcptype %s", c.TCPType)
	}
	fmt.Fprintf(&b, " generation %s", firstNonEmpty(c.Generation, "0"))
	return b.String()
}

// candidateFromSDP parses an SDP candidate attribute value (with or without
// the "candidate:" prefix) into a Jingle candidate.
func candidateFromSDP(value string) (JingleCandidate, error) {
	fields := strings.Fields(strings.TrimPrefix(value, "candidate:"))
	if len(fields) < 8 || fields[6] != "typ" {
		return JingleCandidate{}, fmt.Errorf("malformed ICE candidate %q", value)
	}
	c := JingleCandidate{
		Foundation: fields[0],
		Component:  fields[1],
		Protocol:   strings.ToLower(fields[2]),
		Priority:   fields[3],
		IP:         fields[4],
		Port:       fields[5],
		Type:       fields[7],
		Generation: "0",
		Network:    "0",
		ID:         strconv.FormatUint(rand.Uint64(), 36),
	}
	for i := 8; i+1 < len(fields); i += 2 {
		switch fields[i] {
		case "raddr":
			c.RelAddr = fields[i+1]
		case "rport":
			c.RelPort = fields[i+1]
		case "tcptype":
			c.TCPType = fields[i+1]
		case "generation":
			c.Generation = fields[i+1]
		}
	}
	return c, nil
}

// convertSDPToJingle converts a local SDP (the answer) to a Jingle element
// for action (session-accept). responder is the local full JID. When keep is
// not nil, only the media sections whose mid it accepts become contents (and
// BUNDLE members): the per-source sections of sessionSources are local only.
func convertSDPToJingle(desc *webrtc.SessionDescription, sid, action, responder string, keep func(mid string) bool) (*JingleIQ, error) {
	parsed := &sdp.SessionDescription{}
	if err := parsed.Unmarshal([]byte(desc.SDP)); err != nil {
		return nil, fmt.Errorf("failed to parse local SDP: %w", err)
	}

	jingle := &JingleIQ{
		Action:    action,
		SID:       sid,
		Responder: responder,
	}

	if group, ok := parsed.Attribute("group"); ok {
		fields := strings.Fields(group)
		if len(fields) > 1 {
			g := JingleGroup{Semantics: fields[0]}
			for _, name := range fields[1:] {
				if keep == nil || keep(name) {
					g.Contents = append(g.Contents, JingleGroupEntry{Name: name})
				}
			}
			jingle.Groups = append(jingle.Groups, g)
		}
	}

	// Session-level ICE/DTLS attributes apply to every media section that
	// does not override them.
	session := transportAttrs{}
	session.read(parsed.Attributes)

	for _, md := range parsed.MediaDescriptions {
		mid, _ := md.Attribute("mid")
		if keep != nil && !keep(mid) {
			continue
		}
		content := JingleContent{
			Creator: "responder", // As lib-jitsi-meet does in session-accept
			Name:    firstNonEmpty(mid, md.MediaName.Media),
		}

		attrs := session
		attrs.read(md.Attributes)
		tr := &JingleTransport{Ufrag: attrs.ufrag, Pwd: attrs.pwd}
		if attrs.fingerprint != "" {
			parts := strings.SplitN(attrs.fingerprint, " ", 2)
			if len(parts) == 2 {
				tr.Fingerprints = append(tr.Fingerprints, Fingerprint{Hash: parts[0], Setup: attrs.setup, Value: parts[1]})
			}
		}
		_, rtcpMux := md.Attribute("rtcp-mux")
		for _, a := range md.Attributes {
			if a.Key != "candidate" {
				continue
			}
			c, err := candidateFromSDP(a.Value)
			if err != nil {
				return nil, err
			}
			if rtcpMux && c.Component != "1" {
				continue // RTCP candidates are unused when RTCP is multiplexed
			}
			tr.Candidates = append(tr.Candidates, c)
		}

		if md.MediaName.Media == "application" {
			port, _ := md.Attribute("sctp-port")
			tr.SctpMap = &SctpMap{Number: firstNonEmpty(port, "5000"), Protocol: "webrtc-datachannel", Streams: "1024"}
		} else {
			content.Senders = directionToSenders(md)
			content.Description = rtpDescriptionFromSDP(md)
			if content.Description.RtcpMux != nil {
				tr.RtcpMux = &RtcpMux{}
			}
		}
		content.Transport = tr
		jingle.Content = append(jingle.Content, content)
	}

	return jingle, nil
}

// transportAttrs collects ICE/DTLS attributes from a session or media section.
type transportAttrs struct {
	ufrag, pwd, fingerprint, setup string
}

func (t *transportAttrs) read(attrs []sdp.Attribute) {
	for _, a := range attrs {
		switch a.Key {
		case "ice-ufrag":
			t.ufrag = a.Value
		case "ice-pwd":
			t.pwd = a.Value
		case "fingerprint":
			t.fingerprint = a.Value
		case "setup":
			t.setup = a.Value
		}
	}
}

// rtpDescriptionFromSDP builds the RTP description of an audio/video section.
func rtpDescriptionFromSDP(md *sdp.MediaDescription) *JingleDescription {
	desc := &JingleDescription{Media: md.MediaName.Media}
	byID := map[string]*PayloadType{}
	for _, id := range md.MediaName.Formats {
		desc.PayloadTypes = append(desc.PayloadTypes, PayloadType{ID: id})
	}
	for i := range desc.PayloadTypes {
		byID[desc.PayloadTypes[i].ID] = &desc.PayloadTypes[i]
	}

	sources := map[string]*Source{}
	var sourceOrder []string
	for _, a := range md.Attributes {
		id, rest, _ := strings.Cut(a.Value, " ")
		switch a.Key {
		case "rtpmap":
			if pt := byID[id]; pt != nil {
				parts := strings.Split(rest, "/")
				pt.Name = parts[0]
				if len(parts) > 1 {
					pt.ClockRate = parts[1]
				}
				if len(parts) > 2 {
					pt.Channels = parts[2]
				}
			}
		case "fmtp":
			if pt := byID[id]; pt != nil {
				for _, kv := range strings.Split(rest, ";") {
					k, v, found := strings.Cut(strings.TrimSpace(kv), "=")
					if !found {
						pt.Parameters = append(pt.Parameters, Parameter{Value: k})
						continue
					}
					pt.Parameters = append(pt.Parameters, Parameter{Name: k, Value: v})
				}
			}
		case "rtcp-fb":
			typ, sub, _ := strings.Cut(rest, " ")
			fb := RtcpFb{Type: typ, Subtype: sub}
			if id == "*" {
				for i := range desc.PayloadTypes {
					desc.PayloadTypes[i].RtcpFb = append(desc.PayloadTypes[i].RtcpFb, fb)
				}
			} else if pt := byID[id]; pt != nil {
				pt.RtcpFb = append(pt.RtcpFb, fb)
			}
		case "extmap":
			if uri := strings.Fields(rest); len(uri) > 0 {
				desc.HdrExts = append(desc.HdrExts, RtpHdrExt{ID: strings.Split(id, "/")[0], URI: uri[0]})
			}
		case "rtcp-mux":
			desc.RtcpMux = &RtcpMux{}
		case "ssrc":
			src, ok := sources[id]
			if !ok {
				src = &Source{SSRC: id}
				sources[id] = src
				sourceOrder = append(sourceOrder, id)
			}
			name, value, _ := strings.Cut(rest, ":")
			src.Parameters = append(src.Parameters, Parameter{Name: name, Value: value})
		case "ssrc-group":
			fields := strings.Fields(a.Value)
			if len(fields) < 2 {
				continue
			}
			g := SSRCGroup{Semantics: fields[0]}
			for _, s := range fields[1:] {
				g.Sources = append(g.Sources, SSRCGroupID{SSRC: s})
			}
			desc.SSRCGroups = append(desc.SSRCGroups, g)
		}
	}
	for _, id := range sourceOrder {
		desc.Sources = append(desc.Sources, *sources[id])
	}
	return desc
}

// firstNonEmpty returns the first non-empty string.
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
