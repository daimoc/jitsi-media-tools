package jitsi

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pion/webrtc/v3"

	"github.com/daimoc/jitsi-media-tools/internal/signaling"
)

const (
	testRoomJID  = "room@conference." + testDomain
	testFocusMUC = testRoomJID + "/focus"
)

var quietLogger = slog.New(slog.NewTextHandler(io.Discard, nil))

func newTestAdapter(t *testing.T, srv *fakeXMPPServer, overrides map[string]any) *Adapter {
	t.Helper()
	cfg := map[string]any{
		"xmpp_domain":        testDomain,
		"room_name":          "room",
		"websocket_url":      srv.url(),
		"connect_timeout":    "3s",
		"keepalive_interval": "1h",
	}
	for k, v := range overrides {
		cfg[k] = v
	}
	a := NewAdapter(quietLogger)
	if err := a.ParseConfig(cfg); err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	return a
}

func (a *Adapter) client(peerID string) *SignalingClient {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.clients[peerID]
}

// pionHandler is a SignalingHandler backed by a real Pion PeerConnection.
type pionHandler struct {
	pc           *webrtc.PeerConnection
	candidates   chan [2]string
	lost         chan error
	dataChannels chan [2]string // label, protocol
	kinds        []string       // Media kinds the handler reports sending
	limit        int            // Receive limit the handler reports
	height       int            // Requested video height the handler reports (0: 360)
	renegotiated chan string    // Offers passed to Renegotiate
	channel      *fakeDataChannel
}

func newPionHandler(t *testing.T) *pionHandler {
	t.Helper()
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatalf("NewPeerConnection: %v", err)
	}
	t.Cleanup(func() { pc.Close() })
	return &pionHandler{pc: pc, candidates: make(chan [2]string, 8), lost: make(chan error, 1), dataChannels: make(chan [2]string, 1), renegotiated: make(chan string, 8), channel: newFakeDataChannel()}
}

func (h *pionHandler) OnOffer(sdp string, _ signaling.TrickleFunc) (string, error) {
	if err := h.pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: sdp}); err != nil {
		return "", err
	}
	answer, err := h.pc.CreateAnswer(nil)
	if err != nil {
		return "", err
	}
	gathered := webrtc.GatheringCompletePromise(h.pc)
	if err := h.pc.SetLocalDescription(answer); err != nil {
		return "", err
	}
	<-gathered
	return h.pc.LocalDescription().SDP, nil
}

// newSendingPionHandler returns a handler whose PeerConnection sends a track
// of each kind, as execution.Peer does for a sending profile.
func newSendingPionHandler(t *testing.T, kinds ...string) *pionHandler {
	t.Helper()
	h := newPionHandler(t)
	h.kinds = kinds
	for _, kind := range kinds {
		codec := webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2}
		if kind == "video" {
			codec = webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8, ClockRate: 90000}
		}
		track, err := webrtc.NewTrackLocalStaticRTP(codec, kind, "bot")
		if err != nil {
			t.Fatalf("NewTrackLocalStaticRTP: %v", err)
		}
		if _, err := h.pc.AddTrack(track); err != nil {
			t.Fatalf("AddTrack: %v", err)
		}
	}
	return h
}

func (h *pionHandler) LocalMediaKinds() []string { return h.kinds }

func (h *pionHandler) ReceiveLimit() int { return h.limit }

func (h *pionHandler) RequestedVideoHeight() int {
	if h.height == 0 {
		return 360
	}
	return h.height
}

func (h *pionHandler) VideoLayerHeights() []int { return nil }

func (h *pionHandler) Renegotiate(sdp string) (string, error) {
	if err := h.pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: sdp}); err != nil {
		return "", err
	}
	answer, err := h.pc.CreateAnswer(nil)
	if err != nil {
		return "", err
	}
	if err := h.pc.SetLocalDescription(answer); err != nil {
		return "", err
	}
	h.renegotiated <- sdp
	return answer.SDP, nil
}

func (h *pionHandler) OnICECandidate(mid, candidate string) error {
	h.candidates <- [2]string{mid, candidate}
	return nil
}

func (h *pionHandler) OpenDataChannel(label, protocol string, dh signaling.DataChannelHandler) (signaling.DataChannel, error) {
	h.channel.setHandler(dh)
	h.dataChannels <- [2]string{label, protocol}
	return h.channel, nil
}

// fakeDataChannel is a signaling.DataChannel that the test opens and closes,
// recording the texts sent while open.
type fakeDataChannel struct {
	mu      sync.Mutex
	h       signaling.DataChannelHandler
	isOpen  bool
	fail    bool        // SendText fails while set
	sent    chan string // Texts sent while open
	handled chan struct{}
}

func newFakeDataChannel() *fakeDataChannel {
	return &fakeDataChannel{sent: make(chan string, 64), handled: make(chan struct{})}
}

func (c *fakeDataChannel) setHandler(h signaling.DataChannelHandler) {
	c.mu.Lock()
	c.h = h
	c.mu.Unlock()
	close(c.handled)
}

// handler waits for OpenDataChannel and returns the adapter's handler.
func (c *fakeDataChannel) handler(t *testing.T) signaling.DataChannelHandler {
	t.Helper()
	select {
	case <-c.handled:
	case <-time.After(5 * time.Second):
		t.Fatal("OpenDataChannel was not called")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.h
}

func (c *fakeDataChannel) SendText(msg string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.isOpen || c.fail {
		return errors.New("data channel not open")
	}
	c.sent <- msg
	return nil
}

// open marks the channel open and calls the handler's OnOpen.
func (c *fakeDataChannel) open(t *testing.T) {
	h := c.handler(t)
	c.mu.Lock()
	c.isOpen = true
	c.mu.Unlock()
	h.OnOpen()
}

// close marks the channel closed and calls the handler's OnClose.
func (c *fakeDataChannel) close(t *testing.T) {
	h := c.handler(t)
	c.mu.Lock()
	c.isOpen = false
	c.mu.Unlock()
	h.OnClose()
}

func (h *pionHandler) OnSignalingLost(err error) {
	h.lost <- err
}

// Jitsi has no publish session.
func (h *pionHandler) CreatePublishOffer(signaling.TrickleFunc) (string, error) {
	return "", errors.New("no publish session")
}
func (h *pionHandler) ApplyPublishAnswer(string) error { return errors.New("no publish session") }
func (h *pionHandler) OnPublishICECandidate(_, _ string) error {
	return errors.New("no publish session")
}
func (h *pionHandler) LocalVideoSize() (int, int) { return 0, 0 }

func waitErr(t *testing.T, errc <-chan error, what string) error {
	t.Helper()
	select {
	case err := <-errc:
		return err
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
		return nil
	}
}

// answerConference handles Jicofo's conference request with the given ready
// attribute.
func (c *fakeConn) answerConference(ready bool) {
	c.t.Helper()
	conf := c.expect("conference request", isIQ("set", nsFocus))
	if conf.attr("to") != "focus."+testDomain || !strings.Contains(conf.Inner, `room="`+testRoomJID+`"`) {
		c.t.Errorf("unexpected conference request %s", conf)
	}
	c.send(`<iq xmlns="jabber:client" type="result" id="` + conf.attr("id") + `" from="focus.` + testDomain + `" to="` + c.jid + `">` +
		`<conference xmlns="http://jitsi.org/protocol/focus" ready="` + fmt.Sprint(ready) + `" room="` + testRoomJID + `" focusjid="focus@auth.` + testDomain + `/focus"/></iq>`)
}

// expectJoin waits for the MUC join presence, which must use the nick derived
// from the bound JID and carry displayName.
func (c *fakeConn) expectJoin(displayName string) element {
	c.t.Helper()
	join := c.expect("MUC join presence", func(e element) bool {
		return e.XMLName.Local == "presence" && strings.Contains(e.Inner, nsMUC)
	})
	if join.attr("to") != testRoomJID+"/"+c.nick {
		c.t.Errorf("expected join as %s/%s, got to=%q", testRoomJID, c.nick, join.attr("to"))
	}
	if !strings.Contains(join.Inner, `<nick xmlns="http://jabber.org/protocol/nick">`+displayName+`</nick>`) {
		c.t.Errorf("expected display name %q in the join presence, got %s", displayName, join)
	}
	if !strings.Contains(join.Inner, `ver="`+capsVer+`"`) || !strings.Contains(join.Inner, `xmlns="`+nsCaps+`"`) {
		c.t.Errorf("expected entity capabilities in the join presence, got %s", join)
	}
	return join
}

// joinRoom plays the focus and the MUC for a successful join of displayName.
func (c *fakeConn) joinRoom(displayName string) {
	c.t.Helper()
	c.answerConference(true)
	c.expectJoin(displayName)
	c.confirmJoin()
}

// confirmJoin sends the room's presences for the client's join.
func (c *fakeConn) confirmJoin() {
	// Another participant's presence must not be mistaken for ours.
	c.send(`<presence xmlns="jabber:client" from="` + testRoomJID + `/someone" to="` + c.jid + `"><x xmlns="http://jabber.org/protocol/muc#user"><item affiliation="none" role="participant"/></x></presence>`)
	c.send(`<presence xmlns="jabber:client" from="` + testRoomJID + `/` + c.nick + `" to="` + c.jid + `"><x xmlns="http://jabber.org/protocol/muc#user"><item affiliation="none" role="participant"/><status code="110"/></x></presence>`)
}

// connectPeer joins peerID through the adapter against the fake server.
func connectPeer(t *testing.T, srv *fakeXMPPServer, a *Adapter, peerID string, h *pionHandler) *fakeConn {
	t.Helper()
	errc := make(chan error, 1)
	go func() { errc <- a.Connect(context.Background(), peerID, h) }()
	c := srv.accept()
	c.joinRoom(peerID)
	if err := waitErr(t, errc, "Connect"); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	return c
}

func isResult(id string) func(element) bool {
	return func(e element) bool {
		return e.XMLName.Local == "iq" && e.attr("type") == "result" && e.attr("id") == id
	}
}

func jingleIQ(id, to, jingle string) string {
	return `<iq xmlns="jabber:client" type="set" id="` + id + `" from="` + testFocusMUC + `" to="` + to + `">` + jingle + `</iq>`
}

func TestAdapter_JoinNegotiateAndDisconnect(t *testing.T) {
	srv := newFakeXMPPServer(t)
	a := newTestAdapter(t, srv, nil)
	h := newPionHandler(t)
	c := connectPeer(t, srv, a, "peer1", h)

	client := a.client("peer1")
	if client == nil || client.State() != ClientStateJoined {
		t.Fatalf("expected a joined client, got %v", client)
	}

	// Jicofo offers the session; it must be acknowledged, then accepted.
	offer, err := os.ReadFile("testdata/session-initiate.xml")
	if err != nil {
		t.Fatal(err)
	}
	c.send(jingleIQ("si1", c.jid, string(offer)))
	c.expect("session-initiate ack", isResult("si1"))

	accept := c.expect("session-accept", isJingle("session-accept"))
	if accept.attr("to") != testFocusMUC {
		t.Errorf("session-accept must be sent to the focus, got to=%q", accept.attr("to"))
	}
	var j JingleIQ
	if err := xml.Unmarshal([]byte(accept.Inner), &j); err != nil {
		t.Fatalf("decode session-accept: %v\n%s", err, accept.Inner)
	}
	if j.SID != "7fq3m0b9ai2v8" || j.Responder != c.jid || len(j.Content) != 3 {
		t.Errorf("unexpected session-accept sid=%s responder=%s contents=%d", j.SID, j.Responder, len(j.Content))
	}
	if len(j.Content) > 0 && (j.Content[0].Transport == nil || len(j.Content[0].Transport.Candidates) == 0) {
		t.Error("session-accept must carry the local candidates")
	}
	c.result(accept)

	// The offer includes SCTP: the bridge channel is opened like lib-jitsi-meet.
	select {
	case got := <-h.dataChannels:
		if got != [2]string{"JVB data channel", "http://jitsi.org/protocols/colibri"} {
			t.Errorf("unexpected data channel %v", got)
		}
	case <-time.After(5 * time.Second):
		t.Error("the bridge channel was not opened")
	}

	// Trickled remote candidates are acknowledged and handed to the media side.
	c.send(jingleIQ("ti1", c.jid, `<jingle xmlns="urn:xmpp:jingle:1" action="transport-info" sid="7fq3m0b9ai2v8">`+
		`<content creator="initiator" name="audio"><transport xmlns="urn:xmpp:jingle:transports:ice-udp:1" ufrag="8hhy41eau1ok3" pwd="2tkm5psqo9qv7f6pbal4f3fjbn">`+
		`<candidate component="1" foundation="9" generation="0" id="c9" network="0" priority="1" protocol="udp" type="host" ip="198.51.100.9" port="10001"/>`+
		`</transport></content></jingle>`))
	c.expect("transport-info ack", isResult("ti1"))
	select {
	case got := <-h.candidates:
		if got[0] != "audio" || got[1] != "candidate:9 1 udp 1 198.51.100.9 10001 typ host generation 0" {
			t.Errorf("unexpected candidate %v", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("remote candidate was not passed to the handler")
	}

	// Other Jingle actions are acknowledged too.
	c.send(jingleIQ("sa1", c.jid, `<jingle xmlns="urn:xmpp:jingle:1" action="source-add" sid="7fq3m0b9ai2v8"/>`))
	c.expect("source-add ack", isResult("sa1"))

	errc := make(chan error, 1)
	go func() { errc <- a.Disconnect("peer1") }()
	terminate := c.expect("session-terminate", isJingle("session-terminate"))
	if !strings.Contains(terminate.Inner, "<success") {
		t.Errorf("expected reason success, got %s", terminate.Inner)
	}
	c.expect("leave presence", isPresence("unavailable"))
	c.waitClosed()
	if err := waitErr(t, errc, "Disconnect"); err != nil {
		t.Errorf("Disconnect: %v", err)
	}
	if a.client("peer1") != nil || client.State() != ClientStateDisconnected {
		t.Error("client must be forgotten and disconnected")
	}
	if err := a.Disconnect("peer1"); err != nil {
		t.Errorf("second Disconnect must be a no-op, got %v", err)
	}
}

// A sending bot announces its sources the way lib-jitsi-meet does: source
// names, owner and video type in the session-accept, unmuted SourceInfo in
// its presence.
func TestAdapter_AnnouncesSentSources(t *testing.T) {
	srv := newFakeXMPPServer(t)
	a := newTestAdapter(t, srv, nil)
	h := newSendingPionHandler(t, "audio", "video")
	errc := make(chan error, 1)
	go func() { errc <- a.Connect(context.Background(), "peer1", h) }()
	c := srv.accept()
	c.answerConference(true)
	join := c.expectJoin("peer1")
	wantInfo := `<SourceInfo>{&#34;` + c.nick + `-a0&#34;:{&#34;muted&#34;:false},&#34;` + c.nick + `-v0&#34;:{&#34;muted&#34;:false,&#34;videoType&#34;:&#34;camera&#34;}}</SourceInfo>`
	if !strings.Contains(join.Inner, wantInfo) {
		t.Errorf("expected %s in the join presence, got %s", wantInfo, join.Inner)
	}
	for _, want := range []string{`<audiomuted xmlns="http://jitsi.org/jitmeet/audio">false</audiomuted>`, `<videomuted xmlns="http://jitsi.org/jitmeet/video">false</videomuted>`} {
		if !strings.Contains(join.Inner, want) {
			t.Errorf("expected %s in the join presence, got %s", want, join.Inner)
		}
	}
	c.confirmJoin()
	if err := waitErr(t, errc, "Connect"); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	offer, err := os.ReadFile("testdata/session-initiate.xml")
	if err != nil {
		t.Fatal(err)
	}
	c.send(jingleIQ("si1", c.jid, string(offer)))
	c.expect("session-initiate ack", isResult("si1"))
	accept := c.expect("session-accept", isJingle("session-accept"))
	var j JingleIQ
	if err := xml.Unmarshal([]byte(accept.Inner), &j); err != nil {
		t.Fatalf("decode session-accept: %v", err)
	}
	owner := testRoomJID + "/" + c.nick
	for _, content := range j.Content {
		if content.Description == nil {
			continue
		}
		kind := content.Description.Media
		if len(content.Description.Sources) != 1 {
			t.Fatalf("%s: expected one source, got %+v", kind, content.Description.Sources)
		}
		src := content.Description.Sources[0]
		wantName, wantType := c.nick+"-a0", ""
		if kind == "video" {
			wantName, wantType = c.nick+"-v0", "camera"
		}
		if src.Name != wantName || src.VideoType != wantType || src.SSRCInfo == nil || src.SSRCInfo.Owner != owner {
			t.Errorf("%s: expected name=%s videoType=%q owner=%s, got %+v (ssrc-info %+v)", kind, wantName, wantType, owner, src, src.SSRCInfo)
		}
		if content.Senders != "both" {
			t.Errorf("%s: expected senders both, got %q", kind, content.Senders)
		}
	}
	c.result(accept)
}

// A receive-only bot announces no source.
func TestAdapter_ReceiveOnlyAnnouncesNoSources(t *testing.T) {
	srv := newFakeXMPPServer(t)
	a := newTestAdapter(t, srv, nil)
	h := newPionHandler(t)
	errc := make(chan error, 1)
	go func() { errc <- a.Connect(context.Background(), "peer1", h) }()
	c := srv.accept()
	c.answerConference(true)
	join := c.expectJoin("peer1")
	if strings.Contains(join.Inner, "SourceInfo") || strings.Contains(join.Inner, "muted") {
		t.Errorf("receive-only join must not announce sources, got %s", join.Inner)
	}
	c.confirmJoin()
	if err := waitErr(t, errc, "Connect"); err != nil {
		t.Fatalf("Connect: %v", err)
	}
}

// Jicofo sends JSON-encoded sources (the bots advertise them): the offer
// keeps its data content (bridge channel opened), the participants' sources
// get their sections, and a JSON source-add renegotiates.
func TestAdapter_JSONEncodedSources(t *testing.T) {
	srv := newFakeXMPPServer(t)
	a := newTestAdapter(t, srv, nil)
	h := newPionHandler(t)
	c := connectPeer(t, srv, a, "peer1", h)

	offer, err := os.ReadFile("testdata/session-initiate-json.xml")
	if err != nil {
		t.Fatal(err)
	}
	c.send(jingleIQ("si1", c.jid, string(offer)))
	c.expect("session-initiate ack", isResult("si1"))
	accept := c.expect("session-accept", isJingle("session-accept"))
	var j JingleIQ
	if err := xml.Unmarshal([]byte(accept.Inner), &j); err != nil {
		t.Fatal(err)
	}
	if len(j.Content) != 3 {
		t.Errorf("expected audio, video and data contents in the accept, got %d", len(j.Content))
	}
	select {
	case got := <-h.dataChannels:
		if got[0] != "JVB data channel" {
			t.Errorf("unexpected data channel %v", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the bridge channel was not opened")
	}
	remote := h.pc.RemoteDescription().SDP
	for _, want := range []string{"a=mid:audio-1", "a=mid:video-2", "a=ssrc:21 ", "a=ssrc-group:FID 11 12"} {
		if !strings.Contains(remote, want) {
			t.Errorf("expected %q in the applied offer:\n%s", want, remote)
		}
	}
	if strings.Contains(remote, "a=ssrc:1584398292 ") {
		t.Error("the bridge's own sources must not be offered")
	}
	c.result(accept)

	c.send(jingleIQ("sa1", c.jid, `<jingle xmlns="urn:xmpp:jingle:1" action="source-add" sid="7fq3m0b9ai2v8"><json-message xmlns="http://jitsi.org/jitmeet">{"sources":{"eeee5555":[[],[],[{"s":51,"n":"eeee5555-a0","m":"m5 a"}]]}}</json-message></jingle>`))
	c.expect("source-add ack", isResult("sa1"))
	if o := waitOffer(t, h.renegotiated, "renegotiation after a JSON source-add"); !strings.Contains(o, "a=ssrc:51 ") {
		t.Errorf("expected the JSON source offered:\n%s", o)
	}

	c.send(jingleIQ("sa2", c.jid, `<jingle xmlns="urn:xmpp:jingle:1" action="source-add" sid="7fq3m0b9ai2v8"><json-message xmlns="http://jitsi.org/jitmeet">{</json-message></jingle>`))
	c.expect("invalid json-message error", func(e element) bool {
		return e.XMLName.Local == "iq" && e.attr("id") == "sa2" && e.attr("type") == "error"
	})
}

func TestBotFeatures_AdvertiseJSONSources(t *testing.T) {
	for _, f := range botFeatures {
		if f == "http://jitsi.org/json-encoded-sources" {
			return
		}
	}
	t.Fatal("bots must advertise JSON-encoded sources to be offered the data channel")
}

// Jicofo answers ready="false" while the room is being allocated; joining then
// would be refused, so the client asks again until the room is ready.
func TestAdapter_RetriesConferenceUntilReady(t *testing.T) {
	srv := newFakeXMPPServer(t)
	a := newTestAdapter(t, srv, nil)
	errc := make(chan error, 1)
	go func() { errc <- a.Connect(context.Background(), "peer1", newPionHandler(t)) }()
	c := srv.accept()
	defer a.Disconnect("peer1")

	c.answerConference(false)
	c.answerConference(false)
	c.answerConference(true)
	c.expectJoin("peer1")
	c.confirmJoin()
	if err := waitErr(t, errc, "Connect"); err != nil {
		t.Fatalf("Connect: %v", err)
	}
}

// With two participants, Jitsi clients offer a direct P2P session; the bot
// declines it and only negotiates with the bridge through the focus.
func TestAdapter_DeclinesP2PSessionInitiate(t *testing.T) {
	srv := newFakeXMPPServer(t)
	a := newTestAdapter(t, srv, nil)
	h := newPionHandler(t)
	c := connectPeer(t, srv, a, "peer1", h)
	defer a.Disconnect("peer1")

	offer, err := os.ReadFile("testdata/session-initiate.xml")
	if err != nil {
		t.Fatal(err)
	}
	participant := testRoomJID + "/someone"
	c.send(`<iq xmlns="jabber:client" type="set" id="p2p1" from="` + participant + `" to="` + c.jid + `">` + string(offer) + `</iq>`)
	c.expect("session-initiate ack", isResult("p2p1"))

	terminate := c.expect("session-terminate", isJingle("session-terminate"))
	if terminate.attr("to") != participant || !strings.Contains(terminate.Inner, "<decline") || !strings.Contains(terminate.Inner, `sid="7fq3m0b9ai2v8"`) {
		t.Errorf("expected a decline of the P2P session sent to the participant, got %s", terminate)
	}
	if h.pc.RemoteDescription() != nil {
		t.Error("the P2P offer must not reach the media side")
	}
}

func TestAdapter_RejectsInvalidSessionInitiate(t *testing.T) {
	srv := newFakeXMPPServer(t)
	a := newTestAdapter(t, srv, nil)
	c := connectPeer(t, srv, a, "peer1", newPionHandler(t))
	defer a.Disconnect("peer1")

	c.send(jingleIQ("si2", c.jid, `<jingle xmlns="urn:xmpp:jingle:1" action="session-initiate" sid="empty"/>`))
	reply := c.expect("session-initiate error", func(e element) bool { return e.attr("id") == "si2" })
	if reply.attr("type") != "error" {
		t.Errorf("expected an IQ error for an offer without media, got %s", reply)
	}

	client := a.client("peer1")
	client.mu.RLock()
	n := len(client.sessions)
	client.mu.RUnlock()
	if n != 0 {
		t.Errorf("rejected offer must not leave a session behind, got %d", n)
	}
}

func TestAdapter_ConnectFailsWhenServerUnreachable(t *testing.T) {
	a := NewAdapter(quietLogger)
	if err := a.ParseConfig(map[string]any{
		"xmpp_domain":   testDomain,
		"room_name":     "room",
		"websocket_url": "ws://127.0.0.1:1/xmpp-websocket",
	}); err != nil {
		t.Fatal(err)
	}
	err := a.Connect(context.Background(), "peer1", newPionHandler(t))
	if !errors.Is(err, ErrServerUnreachable) {
		t.Fatalf("expected ErrServerUnreachable, got %v", err)
	}
	if a.client("peer1") != nil {
		t.Error("failed connect must not leave a client behind")
	}
}

func TestAdapter_ConnectFailsWhenFocusUnavailable(t *testing.T) {
	srv := newFakeXMPPServer(t)
	a := newTestAdapter(t, srv, nil)
	errc := make(chan error, 1)
	go func() { errc <- a.Connect(context.Background(), "peer1", newPionHandler(t)) }()

	c := srv.accept()
	conf := c.expect("conference request", isIQ("set", nsFocus))
	c.send(`<iq xmlns="jabber:client" type="error" id="` + conf.attr("id") + `" from="focus.` + testDomain + `" to="` + c.jid + `">` +
		`<error type="cancel"><service-unavailable xmlns="urn:ietf:params:xml:ns:xmpp-stanzas"/></error></iq>`)

	err := waitErr(t, errc, "Connect")
	if !errors.Is(err, ErrFocusUnavailable) {
		t.Fatalf("expected ErrFocusUnavailable, got %v", err)
	}
	c.waitClosed()
	if a.client("peer1") != nil {
		t.Error("failed connect must not leave a client behind")
	}
}

func TestAdapter_ConnectFailsWhenRoomRejectsJoin(t *testing.T) {
	srv := newFakeXMPPServer(t)
	a := newTestAdapter(t, srv, nil)
	errc := make(chan error, 1)
	go func() { errc <- a.Connect(context.Background(), "peer1", newPionHandler(t)) }()

	c := srv.accept()
	c.answerConference(true)
	c.expectJoin("peer1")
	c.send(`<presence xmlns="jabber:client" type="error" from="` + testRoomJID + `/` + c.nick + `" to="` + c.jid + `">` +
		`<error type="auth"><not-authorized xmlns="urn:ietf:params:xml:ns:xmpp-stanzas"/></error></presence>`)

	err := waitErr(t, errc, "Connect")
	if !errors.Is(err, ErrRoomUnavailable) || !strings.Contains(err.Error(), "not-authorized") {
		t.Fatalf("expected ErrRoomUnavailable (not-authorized), got %v", err)
	}
	c.waitClosed()
	if a.client("peer1") != nil {
		t.Error("failed connect must not leave a client behind")
	}
}

func TestAdapter_ConnectHonoursCancellation(t *testing.T) {
	srv := newFakeXMPPServer(t)
	a := newTestAdapter(t, srv, nil)
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- a.Connect(ctx, "peer1", newPionHandler(t)) }()

	c := srv.accept()
	c.expect("conference request", isIQ("set", nsFocus)) // Never answered
	cancel()

	if err := waitErr(t, errc, "Connect"); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	c.waitClosed()
	if a.client("peer1") != nil {
		t.Error("cancelled connect must not leave a client behind")
	}
}

func TestAdapter_DisconnectDuringConnectClosesClient(t *testing.T) {
	srv := newFakeXMPPServer(t)
	a := newTestAdapter(t, srv, nil)
	errc := make(chan error, 1)
	go func() { errc <- a.Connect(context.Background(), "peer1", newPionHandler(t)) }()

	c := srv.accept()
	c.expect("conference request", isIQ("set", nsFocus)) // Never answered
	if err := a.Disconnect("peer1"); err != nil {
		t.Errorf("Disconnect: %v", err)
	}

	if err := waitErr(t, errc, "Connect"); err == nil {
		t.Fatal("Connect must fail when the peer is disconnected meanwhile")
	}
	c.waitClosed()
}

func TestAdapter_KickReportsSignalingLost(t *testing.T) {
	srv := newFakeXMPPServer(t)
	a := newTestAdapter(t, srv, nil)
	h := newPionHandler(t)
	c := connectPeer(t, srv, a, "peer1", h)

	c.send(`<presence xmlns="jabber:client" type="unavailable" from="` + testRoomJID + `/` + c.nick + `" to="` + c.jid + `">` +
		`<x xmlns="http://jabber.org/protocol/muc#user"><item affiliation="none" role="none"/><status code="307"/><status code="110"/></x></presence>`)

	err := waitErr(t, h.lost, "OnSignalingLost")
	if !errors.Is(err, ErrKicked) || !strings.Contains(err.Error(), "kicked") {
		t.Fatalf("expected ErrKicked, got %v", err)
	}
	c.waitClosed()
	if a.client("peer1") != nil {
		t.Error("kicked client must be forgotten")
	}
}

func TestAdapter_ServerDropReportsSignalingLost(t *testing.T) {
	srv := newFakeXMPPServer(t)
	a := newTestAdapter(t, srv, nil)
	h := newPionHandler(t)
	c := connectPeer(t, srv, a, "peer1", h)

	c.drop()

	if err := waitErr(t, h.lost, "OnSignalingLost"); !errors.Is(err, ErrConnectionLost) {
		t.Fatalf("expected ErrConnectionLost, got %v", err)
	}
	if a.client("peer1") != nil {
		t.Error("lost client must be forgotten")
	}
}

func TestAdapter_KeepalivePingsAndAnswersPings(t *testing.T) {
	srv := newFakeXMPPServer(t)
	a := newTestAdapter(t, srv, map[string]any{"keepalive_interval": "50ms"})
	c := connectPeer(t, srv, a, "peer1", newPionHandler(t))
	defer a.Disconnect("peer1")

	ping := c.expect("keepalive ping", isIQ("get", "urn:xmpp:ping"))
	c.result(ping)

	c.send(`<iq xmlns="jabber:client" type="get" id="srvping" from="` + testDomain + `" to="` + c.jid + `"><ping xmlns="urn:xmpp:ping"/></iq>`)
	c.expect("ping reply", isResult("srvping"))
}

func TestAdapter_UnansweredKeepaliveReportsSignalingLost(t *testing.T) {
	srv := newFakeXMPPServer(t)
	a := newTestAdapter(t, srv, map[string]any{"keepalive_interval": "100ms"})
	h := newPionHandler(t)
	connectPeer(t, srv, a, "peer1", h) // Pings are never answered

	err := waitErr(t, h.lost, "OnSignalingLost")
	if !errors.Is(err, ErrConnectionLost) || !strings.Contains(err.Error(), "keepalive") {
		t.Fatalf("expected a keepalive ErrConnectionLost, got %v", err)
	}
}

func TestParseConfig_Defaults(t *testing.T) {
	a := NewAdapter(quietLogger)
	if err := a.ParseConfig(map[string]any{"xmpp_domain": testDomain, "room_name": "room"}); err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	c := a.config
	if c.MUCDomain != "conference."+testDomain ||
		c.WebSocketURL != "wss://"+testDomain+"/xmpp-websocket" ||
		c.FocusJID != "focus."+testDomain ||
		c.connectTimeout != 10*time.Second ||
		c.keepaliveInterval != 30*time.Second {
		t.Errorf("unexpected defaults %+v", c)
	}
}

func TestParseConfig_Rejects(t *testing.T) {
	for name, cfg := range map[string]map[string]any{
		"missing xmpp_domain":   {"room_name": "room"},
		"missing room_name":     {"xmpp_domain": testDomain},
		"bad connect_timeout":   {"xmpp_domain": testDomain, "room_name": "room", "connect_timeout": "soon"},
		"zero connect_timeout":  {"xmpp_domain": testDomain, "room_name": "room", "connect_timeout": "0s"},
		"bad keepalive":         {"xmpp_domain": testDomain, "room_name": "room", "keepalive_interval": "-1s"},
		"wrong type for domain": {"xmpp_domain": 42, "room_name": "room"},
	} {
		if err := NewAdapter(quietLogger).ParseConfig(cfg); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func waitOffer(t *testing.T, offers <-chan string, what string) string {
	t.Helper()
	select {
	case o := <-offers:
		return o
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
		return ""
	}
}

// Participants starting or stopping streams are announced by source-add /
// source-remove: each is acknowledged and the media side is renegotiated
// with the updated offer. A source-add received before the session is
// accepted is applied after it.
func TestAdapter_RenegotiatesOnSourceChanges(t *testing.T) {
	srv := newFakeXMPPServer(t)
	a := newTestAdapter(t, srv, nil)
	h := newPionHandler(t)
	c := connectPeer(t, srv, a, "peer1", h)

	offer, err := os.ReadFile("testdata/session-initiate.xml")
	if err != nil {
		t.Fatal(err)
	}
	c.send(jingleIQ("si1", c.jid, string(offer)))
	c.expect("session-initiate ack", isResult("si1"))
	accept := c.expect("session-accept", isJingle("session-accept"))

	sourceIQ := func(id, action string, j *JingleIQ) string {
		j.Action = action
		out, err := xml.Marshal(j)
		if err != nil {
			t.Fatal(err)
		}
		return jingleIQ(id, c.jid, string(out))
	}

	// Pending: acknowledged, renegotiated only once accepted.
	c.send(sourceIQ("sa1", "source-add", participantSources("aaaa1111", "11", "12", "13")))
	c.expect("source-add ack", isResult("sa1"))
	select {
	case <-h.renegotiated:
		t.Fatal("renegotiated before the session was accepted")
	case <-time.After(200 * time.Millisecond):
	}
	c.result(accept)
	added := waitOffer(t, h.renegotiated, "renegotiation after accept")
	for _, want := range []string{"a=mid:audio-1", "a=mid:video-2", "a=ssrc:11 ", "a=ssrc-group:FID 12 13"} {
		if !strings.Contains(added, want) {
			t.Errorf("expected %q in the renegotiated offer:\n%s", want, added)
		}
	}

	c.send(sourceIQ("sr1", "source-remove", participantSources("aaaa1111", "11", "12", "13")))
	c.expect("source-remove ack", isResult("sr1"))
	removed := waitOffer(t, h.renegotiated, "renegotiation after source-remove")
	if strings.Contains(removed, "a=ssrc:11 ") || strings.Contains(removed, "a=ssrc:12 ") {
		t.Errorf("removed sources still offered:\n%s", removed)
	}
	if !strings.Contains(removed, "a=mid:video-2") || !strings.Contains(removed, "a=inactive") {
		t.Errorf("expected the removed sections kept inactive:\n%s", removed)
	}

	// Jicofo sends no source-remove when a participant leaves: its
	// unavailable presence removes its sources.
	c.send(sourceIQ("sa3", "source-add", participantSources("dddd4444", "41", "42", "43")))
	c.expect("source-add ack", isResult("sa3"))
	if o := waitOffer(t, h.renegotiated, "renegotiation after the second source-add"); !strings.Contains(o, "a=ssrc:41 ") {
		t.Fatalf("expected dddd4444's sources offered:\n%s", o)
	}
	c.send(`<presence xmlns="jabber:client" type="unavailable" from="` + testRoomJID + `/dddd4444" to="` + c.jid + `"><x xmlns="http://jabber.org/protocol/muc#user"><item affiliation="none" role="none"/></x></presence>`)
	if o := waitOffer(t, h.renegotiated, "renegotiation after the owner left"); strings.Contains(o, "a=ssrc:41 ") {
		t.Errorf("the sources of a participant who left are still offered:\n%s", o)
	}

	// A source change for an unknown session is rejected.
	unknown := participantSources("cccc3333", "31", "32", "33")
	unknown.SID = "nope"
	c.send(sourceIQ("sa2", "source-add", unknown))
	c.expect("source-add error", func(e element) bool {
		return e.XMLName.Local == "iq" && e.attr("id") == "sa2" && e.attr("type") == "error"
	})
}

// startBridgeSession connects a peer with h, sends the fixture's
// session-initiate (with an SCTP content unless noSCTP) and accepts it.
func startBridgeSession(t *testing.T, h *pionHandler, noSCTP bool, overrides map[string]any) (*fakeConn, *logBuffer, func(id, action string, j *JingleIQ) string) {
	t.Helper()
	srv := newFakeXMPPServer(t)
	a := newTestAdapter(t, srv, overrides)
	logs, logger := newLogBuffer()
	a.logger = logger
	c := connectPeer(t, srv, a, "peer1", h)

	offer, err := os.ReadFile("testdata/session-initiate.xml")
	if err != nil {
		t.Fatal(err)
	}
	if noSCTP {
		var j JingleIQ
		if err := xml.Unmarshal(offer, &j); err != nil {
			t.Fatal(err)
		}
		kept := j.Content[:0]
		for _, c := range j.Content {
			if c.Transport == nil || c.Transport.SctpMap == nil {
				kept = append(kept, c)
			}
		}
		j.Content = kept
		if offer, err = xml.Marshal(&j); err != nil {
			t.Fatal(err)
		}
	}
	c.send(jingleIQ("si1", c.jid, string(offer)))
	c.expect("session-initiate ack", isResult("si1"))
	c.result(c.expect("session-accept", isJingle("session-accept")))

	sourceIQ := func(id, action string, j *JingleIQ) string {
		j.Action = action
		out, err := xml.Marshal(j)
		if err != nil {
			t.Fatal(err)
		}
		return jingleIQ(id, c.jid, string(out))
	}
	return c, logs, sourceIQ
}

// expectClass returns the next message of class sent on the bridge channel,
// skipping messages of other classes.
func expectClass(t *testing.T, ch *fakeDataChannel, class string) string {
	t.Helper()
	deadline := time.After(2 * time.Second) // FR-005
	for {
		select {
		case m := <-ch.sent:
			if colibriClass(t, m) == class {
				return m
			}
		case <-deadline:
			t.Fatalf("no %s sent on the bridge channel within 2 s", class)
			return ""
		}
	}
}

// Without a receive cap, the bot asks for every video once the bridge
// channel opens.
func TestAdapter_BridgeChannel_RequestsVideo(t *testing.T) {
	h := newPionHandler(t)
	startBridgeSession(t, h, false, nil)
	h.channel.open(t)
	jsonEqual(t, expectClass(t, h.channel, "ReceiverVideoConstraints"),
		`{"colibriClass":"ReceiverVideoConstraints","lastN":-1,"defaultConstraints":{"maxHeight":360}}`)
}

// The height asked for is the handler's (006 contracts/signaling-handler.md).
func TestAdapter_BridgeChannel_RequestsHandlerHeight(t *testing.T) {
	h := newPionHandler(t)
	h.height = 180
	startBridgeSession(t, h, false, nil)
	h.channel.open(t)
	jsonEqual(t, expectClass(t, h.channel, "ReceiverVideoConstraints"),
		`{"colibriClass":"ReceiverVideoConstraints","lastN":-1,"defaultConstraints":{"maxHeight":180}}`)
}

// With a cap, the video asked for follows the received sources: a source
// beyond the cap waits, and is asked for once it replaces a removed one.
func TestAdapter_BridgeChannel_FollowsReceivedVideo(t *testing.T) {
	h := newPionHandler(t)
	h.limit = 1
	c, _, sourceIQ := startBridgeSession(t, h, false, nil)
	h.channel.open(t)
	jsonEqual(t, expectClass(t, h.channel, "ReceiverVideoConstraints"),
		`{"colibriClass":"ReceiverVideoConstraints","lastN":-1,"defaultConstraints":{"maxHeight":0},"constraints":{}}`)

	c.send(sourceIQ("sa1", "source-add", participantSources("aaaa1111", "11", "12", "13")))
	c.expect("source-add ack", isResult("sa1"))
	waitOffer(t, h.renegotiated, "renegotiation after source-add")
	jsonEqual(t, expectClass(t, h.channel, "ReceiverVideoConstraints"),
		`{"colibriClass":"ReceiverVideoConstraints","lastN":-1,"defaultConstraints":{"maxHeight":0},"constraints":{"aaaa1111-v0":{"maxHeight":360}}}`)

	c.send(sourceIQ("sa2", "source-add", participantSources("bbbb2222", "21", "22", "23")))
	c.expect("source-add ack", isResult("sa2"))
	select {
	case m := <-h.channel.sent:
		if colibriClass(t, m) == "ReceiverVideoConstraints" {
			t.Fatalf("a waiting source must not change the video asked for, got %s", m)
		}
	case <-time.After(200 * time.Millisecond):
	}

	c.send(sourceIQ("sr1", "source-remove", participantSources("aaaa1111", "11", "12", "13")))
	c.expect("source-remove ack", isResult("sr1"))
	jsonEqual(t, expectClass(t, h.channel, "ReceiverVideoConstraints"),
		`{"colibriClass":"ReceiverVideoConstraints","lastN":-1,"defaultConstraints":{"maxHeight":0},"constraints":{"bbbb2222-v0":{"maxHeight":360}}}`)
}

// An offer without SCTP: no bridge channel, logged once, session accepted
// as before (FR-007, SC-005).
func TestAdapter_NoBridgeChannel(t *testing.T) {
	h := newPionHandler(t)
	_, logs, _ := startBridgeSession(t, h, true, nil)
	select {
	case dc := <-h.dataChannels:
		t.Fatalf("no data channel expected without SCTP, got %v", dc)
	case <-time.After(200 * time.Millisecond):
	}
	if n := logs.count("Bridge channel not offered"); n != 1 {
		t.Errorf("expected 'Bridge channel not offered' logged once, got %d", n)
	}
}

// With no_bridge_channel, the offered bridge channel is not opened: no
// preferences are sent and the receive cap is enforced by the bot only.
func TestAdapter_BridgeChannelDisabled(t *testing.T) {
	h := newPionHandler(t)
	_, logs, _ := startBridgeSession(t, h, false, map[string]any{"no_bridge_channel": true})
	select {
	case dc := <-h.dataChannels:
		t.Fatalf("no data channel expected with no_bridge_channel, got %v", dc)
	case <-time.After(200 * time.Millisecond):
	}
	if n := logs.count("Bridge channel disabled"); n != 1 {
		t.Errorf("expected 'Bridge channel disabled' logged once, got %d", n)
	}
}

// With a cap, the audio subscription names the received sources and follows
// a promotion when a participant leaves (US2 scenario 2).
func TestAdapter_BridgeChannel_SubscribesToReceivedAudio(t *testing.T) {
	h := newPionHandler(t)
	h.limit = 1
	c, _, sourceIQ := startBridgeSession(t, h, false, nil)
	h.channel.open(t)
	jsonEqual(t, expectClass(t, h.channel, "ReceiverAudioSubscription"),
		`{"colibriClass":"ReceiverAudioSubscription","mode":"Include","list":[]}`)

	for i, owner := range []string{"aaaa1111", "bbbb2222"} {
		id := fmt.Sprintf("sa%d", i)
		c.send(sourceIQ(id, "source-add", participantSources(owner, fmt.Sprint(11+10*i), fmt.Sprint(12+10*i), fmt.Sprint(13+10*i))))
		c.expect("source-add ack", isResult(id))
	}
	jsonEqual(t, expectClass(t, h.channel, "ReceiverAudioSubscription"),
		`{"colibriClass":"ReceiverAudioSubscription","mode":"Include","list":["aaaa1111-a0"]}`)

	c.send(`<presence xmlns="jabber:client" type="unavailable" from="` + testRoomJID + `/aaaa1111" to="` + c.jid + `"><x xmlns="http://jabber.org/protocol/muc#user"><item affiliation="none" role="none"/></x></presence>`)
	// Video constraints are sent before the audio subscription.
	jsonEqual(t, expectClass(t, h.channel, "ReceiverVideoConstraints"),
		`{"colibriClass":"ReceiverVideoConstraints","lastN":-1,"defaultConstraints":{"maxHeight":0},"constraints":{"bbbb2222-v0":{"maxHeight":360}}}`)
	jsonEqual(t, expectClass(t, h.channel, "ReceiverAudioSubscription"),
		`{"colibriClass":"ReceiverAudioSubscription","mode":"Include","list":["bbbb2222-a0"]}`)
}
