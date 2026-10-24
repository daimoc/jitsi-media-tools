package jitsi

import (
	"fmt"
	"strings"
	"testing"

	"github.com/pion/sdp/v3"
	"github.com/pion/webrtc/v3"
)

// participantSources returns a source-add style Jingle with an audio source
// and a video source plus its RTX (FID group) owned by owner.
func participantSources(owner, audio, video, rtx string) *JingleIQ {
	src := func(ssrc, name string) Source {
		return Source{SSRC: ssrc, Name: name, Parameters: []Parameter{{Name: "msid", Value: owner + " " + name}}, SSRCInfo: &SSRCInfo{Owner: "room@muc/" + owner}}
	}
	return &JingleIQ{Action: "source-add", SID: "7fq3m0b9ai2v8", Content: []JingleContent{
		{Name: "audio", Description: &JingleDescription{Media: "audio", Sources: []Source{src(audio, owner+"-a0")}}},
		{Name: "video", Description: &JingleDescription{Media: "video",
			Sources:    []Source{src(video, owner+"-v0"), src(rtx, owner+"-v0")},
			SSRCGroups: []SSRCGroup{{Semantics: "FID", Sources: []SSRCGroupID{{SSRC: video}, {SSRC: rtx}}}}}},
	}}
}

func parseOffer(t *testing.T, offer *webrtc.SessionDescription) *sdp.SessionDescription {
	t.Helper()
	parsed := &sdp.SessionDescription{}
	if err := parsed.Unmarshal([]byte(offer.SDP)); err != nil {
		t.Fatalf("unmarshal offer: %v\n%s", err, offer.SDP)
	}
	return parsed
}

func mids(s *sdp.SessionDescription) []string {
	var out []string
	for _, md := range s.MediaDescriptions {
		mid, _ := md.Attribute("mid")
		out = append(out, mid)
	}
	return out
}

func TestSessionSources_InitialOfferSplitsSourcesPerSection(t *testing.T) {
	j := loadSessionInitiate(t)
	// The fixture's own sources belong to the bridge ("jvb"); add two
	// participants as Jicofo does for a late joiner.
	for _, p := range []*JingleIQ{participantSources("aaaa1111", "11", "12", "13"), participantSources("bbbb2222", "21", "22", "23")} {
		for i := range j.Content {
			for _, c := range p.Content {
				if c.Name == j.Content[i].Name {
					j.Content[i].Description.Sources = append(j.Content[i].Description.Sources, c.Description.Sources...)
					j.Content[i].Description.SSRCGroups = append(j.Content[i].Description.SSRCGroups, c.Description.SSRCGroups...)
				}
			}
		}
	}

	ss := newSessionSources(j, 0, 0)
	offer, err := ss.offer()
	if err != nil {
		t.Fatalf("offer: %v", err)
	}
	parsed := parseOffer(t, offer)
	if got, want := strings.Join(mids(parsed), ","), "audio,video,data,audio-1,audio-2,video-3,video-4"; got != want {
		t.Fatalf("expected sections %s, got %s", want, got)
	}
	if group, _ := parsed.Attribute("group"); group != "BUNDLE audio video data audio-1 audio-2 video-3 video-4" {
		t.Errorf("unexpected BUNDLE group %q", group)
	}
	// Main sections carry no remote source: participants' sources have their
	// own sections and the bridge's own ones are left out.
	for _, mid := range []string{"audio", "video"} {
		md := mediaByMid(t, parsed, mid)
		if _, ok := md.Attribute("ssrc"); ok {
			t.Errorf("%s: expected no remote source in the main section", mid)
		}
		if !hasAttr(md, "sendrecv", "") {
			t.Errorf("%s: expected sendrecv", mid)
		}
	}
	video := mediaByMid(t, parsed, "video-3")
	if !hasAttr(video, "sendonly", "") || !hasAttr(video, "ssrc-group", "FID 12 13") || !hasAttr(video, "ssrc", "12 msid:aaaa1111 aaaa1111-v0") {
		t.Errorf("video-3: expected sendonly with the participant's source and FID group, got %+v", video.Attributes)
	}
	if _, ok := video.Attribute("candidate"); ok {
		t.Error("extra sections must not repeat the candidates (bundled)")
	}

	// Pion accepts it and answers one receiver per remote source.
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	if err := pc.SetRemoteDescription(*offer); err != nil {
		t.Fatalf("Pion rejected the offer: %v\n%s", err, offer.SDP)
	}
	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		t.Fatalf("CreateAnswer: %v", err)
	}
	if err := pc.SetLocalDescription(answer); err != nil {
		t.Fatalf("SetLocalDescription: %v", err)
	}
	if n := strings.Count(answer.SDP, "a=recvonly"); n != 6 {
		t.Errorf("expected 6 recvonly sections (2 main + 4 participants), got %d", n)
	}

	// The session-accept only carries the Jingle contents.
	accept, err := convertSDPToJingle(pc.LocalDescription(), "7fq3m0b9ai2v8", "session-accept", "bot@meet.example.org/res", ss.isContent)
	if err != nil {
		t.Fatalf("convertSDPToJingle: %v", err)
	}
	var names []string
	for _, c := range accept.Content {
		names = append(names, c.Name)
	}
	if strings.Join(names, ",") != "audio,video,data" {
		t.Errorf("expected contents audio,video,data, got %v", names)
	}
	if len(accept.Groups) != 1 || len(accept.Groups[0].Contents) != 3 {
		t.Errorf("expected the BUNDLE group filtered to the 3 contents, got %+v", accept.Groups)
	}
}

func TestSessionSources_AddAndRemove(t *testing.T) {
	ss := newSessionSources(loadSessionInitiate(t), 0, 0)
	first, _ := ss.offer()
	if got := strings.Join(mids(parseOffer(t, first)), ","); got != "audio,video,data" {
		t.Fatalf("expected only the contents, got %s", got)
	}
	if ss.changed {
		t.Fatal("rendering must clear changed")
	}

	if n := ss.add(participantSources("aaaa1111", "11", "12", "13")); n != 2 {
		t.Fatalf("expected 2 new sources, got %d", n)
	}
	if n := ss.add(participantSources("aaaa1111", "11", "12", "13")); n != 0 {
		t.Fatalf("a repeated source-add must not add sections, got %d", n)
	}
	if !ss.changed {
		t.Fatal("add must mark the state changed")
	}
	second, _ := ss.offer()
	p2 := parseOffer(t, second)
	if got := strings.Join(mids(p2), ","); got != "audio,video,data,audio-1,video-2" {
		t.Fatalf("unexpected sections %s", got)
	}

	removal := participantSources("aaaa1111", "11", "12", "13")
	removal.Action = "source-remove"
	if n := ss.remove(removal); n != 2 {
		t.Fatalf("expected 2 removed sources, got %d", n)
	}
	third, _ := ss.offer()
	p3 := parseOffer(t, third)
	// Sections are never dropped or reused: the removed ones become inactive
	// without source, keeping their mids and order.
	if got := strings.Join(mids(p3), ","); got != "audio,video,data,audio-1,video-2" {
		t.Fatalf("unexpected sections %s", got)
	}
	for _, mid := range []string{"audio-1", "video-2"} {
		md := mediaByMid(t, p3, mid)
		if _, ok := md.Attribute("ssrc"); ok || !hasAttr(md, "inactive", "") {
			t.Errorf("%s: expected inactive without source, got %+v", mid, md.Attributes)
		}
	}
	ss.add(participantSources("bbbb2222", "21", "22", "23"))
	fourth, _ := ss.offer()
	if got := strings.Join(mids(parseOffer(t, fourth)), ","); got != "audio,video,data,audio-1,video-2,audio-3,video-4" {
		t.Fatalf("unexpected sections %s", got)
	}
	// The session version grows with each offer.
	if !(sessionVersion(first) < sessionVersion(second) && sessionVersion(second) < sessionVersion(third)) {
		t.Errorf("expected increasing o= versions, got %d %d %d", sessionVersion(first), sessionVersion(second), sessionVersion(third))
	}
}

func TestSessionSources_SimulcastKeepsFirstLayer(t *testing.T) {
	j := &JingleIQ{Action: "source-add", Content: []JingleContent{{Name: "video", Description: &JingleDescription{Media: "video",
		Sources: []Source{{SSRC: "1"}, {SSRC: "2"}, {SSRC: "3"}, {SSRC: "4"}, {SSRC: "5"}, {SSRC: "6"}},
		SSRCGroups: []SSRCGroup{
			{Semantics: "SIM", Sources: []SSRCGroupID{{SSRC: "1"}, {SSRC: "2"}, {SSRC: "3"}}},
			{Semantics: "FID", Sources: []SSRCGroupID{{SSRC: "1"}, {SSRC: "4"}}},
			{Semantics: "FID", Sources: []SSRCGroupID{{SSRC: "2"}, {SSRC: "5"}}},
			{Semantics: "FID", Sources: []SSRCGroupID{{SSRC: "3"}, {SSRC: "6"}}},
		}}}}}
	ss := newSessionSources(loadSessionInitiate(t), 0, 0)
	if n := ss.add(j); n != 1 {
		t.Fatalf("a simulcast source is one stream, got %d", n)
	}
	offer, _ := ss.offer()
	md := mediaByMid(t, parseOffer(t, offer), "video-1")
	if !hasAttr(md, "ssrc-group", "FID 1 4") {
		t.Errorf("expected the first layer and its RTX, got %+v", md.Attributes)
	}
	for _, a := range md.Attributes {
		if a.Key == "ssrc-group" && strings.HasPrefix(a.Value, "SIM") {
			t.Errorf("the SIM group must not be offered, got %s", a.Value)
		}
	}
}

func sessionVersion(offer *webrtc.SessionDescription) uint64 {
	parsed := &sdp.SessionDescription{}
	parsed.Unmarshal([]byte(offer.SDP))
	return parsed.Origin.SessionVersion
}

func TestSessionSources_RemoveOwner(t *testing.T) {
	ss := newSessionSources(loadSessionInitiate(t), 0, 0)
	ss.add(participantSources("aaaa1111", "11", "12", "13"))
	ss.add(participantSources("bbbb2222", "21", "22", "23"))
	ss.offer()
	if n := ss.removeOwner("aaaa1111"); n != 2 || !ss.changed {
		t.Fatalf("expected the 2 streams of aaaa1111 removed, got %d (changed=%v)", n, ss.changed)
	}
	if n := ss.removeOwner("aaaa1111"); n != 0 {
		t.Fatalf("a second removal must be a no-op, got %d", n)
	}
	offer, _ := ss.offer()
	if strings.Contains(offer.SDP, "a=ssrc:11 ") || !strings.Contains(offer.SDP, "a=ssrc:21 ") {
		t.Errorf("expected only bbbb2222's sources left:\n%s", offer.SDP)
	}
}

// audioOnly returns a source-add with one audio source of owner.
func audioOnly(owner, ssrc string) *JingleIQ {
	j := participantSources(owner, ssrc, "v"+ssrc, "r"+ssrc)
	j.Content = j.Content[:1]
	return j
}

func audioSSRCs(t *testing.T, ss *sessionSources) []string {
	t.Helper()
	offer, err := ss.offer()
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, md := range parseOffer(t, offer).MediaDescriptions {
		if md.MediaName.Media != "audio" {
			continue
		}
		if v, ok := md.Attribute("ssrc"); ok {
			out = append(out, strings.Fields(v)[0])
		}
	}
	return out
}

func TestSessionSources_ReceiveLimit(t *testing.T) {
	j := loadSessionInitiate(t) // Its own sources are the bridge's: not counted
	for _, p := range []*JingleIQ{audioOnly("aaaa1111", "11"), audioOnly("bbbb2222", "21"), audioOnly("cccc3333", "31")} {
		j.Content[0].Description.Sources = append(j.Content[0].Description.Sources, p.Content[0].Description.Sources...)
	}
	ss := newSessionSources(j, 2, 0)
	if got := strings.Join(audioSSRCs(t, ss), ","); got != "11,21" {
		t.Fatalf("expected the first 2 audio sources offered, got %s", got)
	}

	// A 4th source waits too; adding it does not change the offer.
	if n := ss.add(audioOnly("dddd4444", "41")); n != 0 || ss.changed {
		t.Fatalf("a source beyond the cap must wait (n=%d changed=%v)", n, ss.changed)
	}

	// Removing an active source promotes the oldest waiting one.
	removal := audioOnly("aaaa1111", "11")
	removal.Action = "source-remove"
	if n := ss.remove(removal); n != 1 {
		t.Fatalf("expected 1 active source removed, got %d", n)
	}
	if got := strings.Join(audioSSRCs(t, ss), ","); got != "21,31" {
		t.Fatalf("expected 31 promoted, got %s", got)
	}

	// Its owner leaving removes a waiting source too, without changing the offer.
	if n := ss.removeOwner("dddd4444"); n != 0 || ss.changed {
		t.Fatalf("removing a waiting source must not change the offer (n=%d changed=%v)", n, ss.changed)
	}
	if n := ss.removeOwner("bbbb2222"); n != 1 {
		t.Fatalf("expected 1 active source removed, got %d", n)
	}
	if got := strings.Join(audioSSRCs(t, ss), ","); got != "31" {
		t.Fatalf("expected only 31 left (41 left with its owner), got %s", got)
	}
}

func TestSessionSources_NoLimit(t *testing.T) {
	ss := newSessionSources(loadSessionInitiate(t), 0, 0)
	for i, owner := range []string{"aaaa1111", "bbbb2222", "cccc3333"} {
		ss.add(audioOnly(owner, fmt.Sprint(11+10*i)))
	}
	if got := strings.Join(audioSSRCs(t, ss), ","); got != "11,21,31" {
		t.Fatalf("expected all sources offered, got %s", got)
	}
}

func TestSessionSources_ReceivedNames(t *testing.T) {
	ss := newSessionSources(loadSessionInitiate(t), 2, 0)
	for i, owner := range []string{"aaaa1111", "bbbb2222", "cccc3333"} {
		ss.add(participantSources(owner, fmt.Sprint(11+10*i), fmt.Sprint(12+10*i), fmt.Sprint(13+10*i)))
	}
	check := func(content, want string, wantUnnamed bool) {
		t.Helper()
		names, unnamed := ss.receivedNames(content)
		if got := strings.Join(names, ","); got != want || unnamed != wantUnnamed {
			t.Errorf("receivedNames(%s) = %q, %v; want %q, %v", content, got, unnamed, want, wantUnnamed)
		}
	}
	// The third participant waits (cap 2), the bridge's sources are skipped.
	check("audio", "aaaa1111-a0,bbbb2222-a0", false)
	check("video", "aaaa1111-v0,bbbb2222-v0", false)

	// The first leaves: its sections become inactive, the third is promoted
	// into new sections, after the second.
	ss.removeOwner("aaaa1111")
	check("audio", "bbbb2222-a0,cccc3333-a0", false)
	check("video", "bbbb2222-v0,cccc3333-v0", false)

	// A received source without a name is flagged.
	unnamed := audioOnly("dddd4444", "41")
	unnamed.Content[0].Description.Sources[0].Name = ""
	ss.removeOwner("bbbb2222")
	ss.add(unnamed)
	check("audio", "cccc3333-a0", true)

	if names, _ := ss.receivedNames("data"); len(names) != 0 {
		t.Errorf("expected no names for the data content, got %v", names)
	}
}
