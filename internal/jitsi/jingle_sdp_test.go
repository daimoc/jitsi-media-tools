package jitsi

import (
	"encoding/xml"
	"os"
	"strings"
	"testing"

	"github.com/pion/sdp/v3"
	"github.com/pion/webrtc/v3"
)

func loadSessionInitiate(t *testing.T) *JingleIQ {
	t.Helper()
	data, err := os.ReadFile("testdata/session-initiate.xml")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var j JingleIQ
	if err := xml.Unmarshal(data, &j); err != nil {
		t.Fatalf("unmarshal fixture: %v", err)
	}
	return &j
}

func mediaByMid(t *testing.T, s *sdp.SessionDescription, mid string) *sdp.MediaDescription {
	t.Helper()
	for _, md := range s.MediaDescriptions {
		if v, _ := md.Attribute("mid"); v == mid {
			return md
		}
	}
	t.Fatalf("no media section with mid %q", mid)
	return nil
}

func hasAttr(md *sdp.MediaDescription, key, value string) bool {
	for _, a := range md.Attributes {
		if a.Key == key && a.Value == value {
			return true
		}
	}
	return false
}

func TestConvertJingleToSDP_JitsiOffer(t *testing.T) {
	offer, err := convertJingleToSDP(loadSessionInitiate(t))
	if err != nil {
		t.Fatalf("convertJingleToSDP: %v", err)
	}

	var parsed sdp.SessionDescription
	if err := parsed.Unmarshal([]byte(offer.SDP)); err != nil {
		t.Fatalf("generated SDP does not parse: %v\n%s", err, offer.SDP)
	}
	if v, _ := parsed.Attribute("group"); v != "BUNDLE audio video data" {
		t.Errorf("expected BUNDLE group, got %q", v)
	}

	audio := mediaByMid(t, &parsed, "audio")
	for _, want := range [][2]string{
		{"rtpmap", "111 opus/48000/2"},
		{"rtpmap", "126 telephone-event/8000"},
		{"fmtp", "111 minptime=10;useinbandfec=1"},
		{"rtcp-fb", "111 transport-cc"},
		{"extmap", "1 urn:ietf:params:rtp-hdrext:ssrc-audio-level"},
		{"ice-ufrag", "8hhy41eau1ok3"},
		{"ice-pwd", "2tkm5psqo9qv7f6pbal4f3fjbn"},
		{"setup", "actpass"},
		{"sendrecv", ""},
		{"rtcp-mux", ""},
		{"ssrc", "1584398292 msid:mixedmslabel mixedlabelaudio0"},
		{"candidate", "1 1 udp 2130706431 10.0.0.5 10000 typ host generation 0"},
		{"candidate", "2 1 udp 1694498815 203.0.113.7 10000 typ srflx raddr 10.0.0.5 rport 10000 generation 0"},
	} {
		if !hasAttr(audio, want[0], want[1]) {
			t.Errorf("audio section missing a=%s:%s\n%s", want[0], want[1], offer.SDP)
		}
	}
	if !strings.HasPrefix(audio.MediaName.Protos[0], "UDP") || strings.Join(audio.MediaName.Formats, " ") != "111 126" {
		t.Errorf("unexpected audio m-line %+v", audio.MediaName)
	}

	video := mediaByMid(t, &parsed, "video")
	for _, want := range [][2]string{
		{"rtpmap", "100 VP8/90000"},
		{"rtpmap", "96 rtx/90000"},
		{"fmtp", "96 apt=100"},
		{"rtcp-fb", "100 nack pli"},
		{"rtcp-fb", "100 ccm fir"},
		{"ssrc-group", "FID 1171298375 2958314010"},
	} {
		if !hasAttr(video, want[0], want[1]) {
			t.Errorf("video section missing a=%s:%s", want[0], want[1])
		}
	}

	data := mediaByMid(t, &parsed, "data")
	if data.MediaName.Media != "application" || !hasAttr(data, "sctp-port", "5000") {
		t.Errorf("expected an SCTP application section, got %+v", data)
	}
}

func TestConvertJingleToSDP_SendersMapToDirection(t *testing.T) {
	for senders, want := range map[string]string{
		"":          "sendrecv",
		"both":      "sendrecv",
		"initiator": "sendonly",
		"responder": "recvonly",
		"none":      "inactive",
	} {
		j := loadSessionInitiate(t)
		j.Content[0].Senders = senders
		offer, err := convertJingleToSDP(j)
		if err != nil {
			t.Fatalf("convertJingleToSDP: %v", err)
		}
		var parsed sdp.SessionDescription
		if err := parsed.Unmarshal([]byte(offer.SDP)); err != nil {
			t.Fatalf("parse: %v", err)
		}
		if !hasAttr(mediaByMid(t, &parsed, "audio"), want, "") {
			t.Errorf("senders=%q: expected a=%s", senders, want)
		}
	}
}

func TestConvertJingleToSDP_RejectsOfferWithoutMedia(t *testing.T) {
	_, err := convertJingleToSDP(&JingleIQ{Action: "session-initiate", SID: "x"})
	if err == nil {
		t.Fatal("expected an error for an offer without contents")
	}
}

// TestJitsiOfferRoundTripThroughPion is the contract between the adapter and
// the media engine: the SDP built from a Jitsi offer must be accepted by Pion,
// and Pion's answer must translate back into a complete session-accept.
func TestJitsiOfferRoundTripThroughPion(t *testing.T) {
	offer, err := convertJingleToSDP(loadSessionInitiate(t))
	if err != nil {
		t.Fatalf("convertJingleToSDP: %v", err)
	}

	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatalf("NewPeerConnection: %v", err)
	}
	defer pc.Close()
	if err := pc.SetRemoteDescription(*offer); err != nil {
		t.Fatalf("Pion rejected the offer: %v\n%s", err, offer.SDP)
	}
	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		t.Fatalf("CreateAnswer: %v", err)
	}
	gathered := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(answer); err != nil {
		t.Fatalf("SetLocalDescription: %v", err)
	}
	<-gathered

	accept, err := convertSDPToJingle(pc.LocalDescription(), "7fq3m0b9ai2v8", "session-accept", "bot@meet.example.org/res", nil)
	if err != nil {
		t.Fatalf("convertSDPToJingle: %v", err)
	}
	if accept.Action != "session-accept" || accept.SID != "7fq3m0b9ai2v8" || accept.Responder != "bot@meet.example.org/res" {
		t.Errorf("unexpected jingle header %+v", accept)
	}
	if len(accept.Groups) != 1 || accept.Groups[0].Semantics != "BUNDLE" || len(accept.Groups[0].Contents) != 3 {
		t.Errorf("expected a BUNDLE group of 3 contents, got %+v", accept.Groups)
	}
	if len(accept.Content) != 3 {
		t.Fatalf("expected 3 contents, got %d", len(accept.Content))
	}

	for _, c := range accept.Content {
		if c.Creator != "responder" {
			t.Errorf("content %s: expected creator=responder, got %q", c.Name, c.Creator)
		}
		tr := c.Transport
		if tr == nil || tr.Ufrag == "" || tr.Pwd == "" {
			t.Fatalf("content %s: missing ICE credentials", c.Name)
		}
		if len(tr.Fingerprints) != 1 || tr.Fingerprints[0].Hash != "sha-256" || tr.Fingerprints[0].Setup != "active" {
			t.Errorf("content %s: expected one sha-256 fingerprint with setup=active, got %+v", c.Name, tr.Fingerprints)
		}
		for _, cand := range tr.Candidates {
			if cand.Component != "1" {
				t.Errorf("content %s: RTCP candidate must be dropped with rtcp-mux, got %+v", c.Name, cand)
			}
			if cand.Foundation == "" || cand.Component == "" || cand.IP == "" || cand.Port == "" || cand.Type == "" || cand.ID == "" {
				t.Errorf("content %s: incomplete candidate %+v", c.Name, cand)
			}
		}
	}

	// With BUNDLE, candidates are carried by the first content only.
	if len(accept.Content[0].Transport.Candidates) == 0 {
		t.Error("expected gathered candidates in the first (bundled) content")
	}

	audio := accept.Content[0]
	if audio.Name != "audio" || audio.Senders != "initiator" {
		t.Errorf("expected recvonly audio (senders=initiator), got name=%s senders=%s", audio.Name, audio.Senders)
	}
	if audio.Description == nil || audio.Description.RtcpMux == nil || audio.Transport.RtcpMux == nil {
		t.Fatal("expected rtcp-mux in audio description and transport")
	}
	var opus *PayloadType
	for i := range audio.Description.PayloadTypes {
		if audio.Description.PayloadTypes[i].Name == "opus" {
			opus = &audio.Description.PayloadTypes[i]
		}
	}
	if opus == nil || opus.ID != "111" || opus.ClockRate != "48000" || opus.Channels != "2" {
		t.Errorf("expected opus 111/48000/2 payload type, got %+v", audio.Description.PayloadTypes)
	}

	video := accept.Content[1]
	var vp8 *PayloadType
	for i := range video.Description.PayloadTypes {
		if video.Description.PayloadTypes[i].Name == "VP8" {
			vp8 = &video.Description.PayloadTypes[i]
		}
	}
	if vp8 == nil || len(vp8.RtcpFb) == 0 {
		t.Errorf("expected VP8 payload type with rtcp-fb, got %+v", video.Description.PayloadTypes)
	}

	data := accept.Content[2]
	if data.Name != "data" || data.Description != nil || data.Transport.SctpMap == nil || data.Transport.SctpMap.Number != "5000" {
		t.Errorf("expected data content with sctpmap 5000, got %+v", data)
	}

	// The accept must marshal with the Jingle namespaces Jicofo expects.
	out, err := xml.Marshal(accept)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, ns := range []string{nsJingle, nsJingleRTP, nsJingleICEUDP, nsJingleDTLS, nsJingleSCTP, nsJingleRtcpFb, nsJingleGrouping} {
		if !strings.Contains(string(out), `xmlns="`+ns+`"`) {
			t.Errorf("marshalled session-accept lacks namespace %s", ns)
		}
	}
}

func TestCandidateSDPRoundTrip(t *testing.T) {
	c := JingleCandidate{
		Component: "1", Foundation: "3", Generation: "0", ID: "x", IP: "198.51.100.4", Port: "4443",
		Priority: "1518280447", Protocol: "tcp", Type: "host", TCPType: "passive",
	}
	line := candidateToSDP(c)
	if line != "candidate:3 1 tcp 1518280447 198.51.100.4 4443 typ host tcptype passive generation 0" {
		t.Errorf("unexpected candidate line %q", line)
	}

	back, err := candidateFromSDP(line)
	if err != nil {
		t.Fatalf("candidateFromSDP: %v", err)
	}
	back.ID = c.ID
	back.Network = c.Network
	if back != c {
		t.Errorf("round trip mismatch:\n got %+v\nwant %+v", back, c)
	}
}
