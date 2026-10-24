package jitsi

import (
	"encoding/xml"
	"reflect"
	"testing"
)

// A session-initiate as Jicofo sends it to a client with JSON sources: the
// contents carry no sources, the json-message lists them per owner.
const jsonSessionInitiate = `<jingle xmlns="urn:xmpp:jingle:1" action="session-initiate" sid="s1">` +
	`<content creator="initiator" name="audio" senders="both"><description xmlns="urn:xmpp:jingle:apps:rtp:1" media="audio"><payload-type id="111" name="opus" clockrate="48000" channels="2"/></description></content>` +
	`<content creator="initiator" name="video" senders="both"><description xmlns="urn:xmpp:jingle:apps:rtp:1" media="video"><payload-type id="100" name="VP8" clockrate="90000"/></description></content>` +
	`<json-message xmlns="http://jitsi.org/jitmeet">{"sources":{` +
	`"jvb":[[{"s":2369987446,"n":"jvb-v0","m":"mixedmslabel mixedlabelvideo0"}],[],[{"s":1870972583,"n":"jvb-a0","m":"mixedmslabel mixedlabelaudio0"}]],` +
	`"abcd1234":[[{"s":11,"n":"abcd1234-v0","m":"m1 v"},{"s":12,"n":"abcd1234-v0","m":"m1 v"},{"s":13,"n":"abcd1234-v1","v":"d"}],[["f",11,12]],[{"s":21,"n":"abcd1234-a0","m":"m1 a"}],[]]` +
	`}}</json-message></jingle>`

func TestApplyJSONSources_SessionInitiate(t *testing.T) {
	var j JingleIQ
	if err := xml.Unmarshal([]byte(jsonSessionInitiate), &j); err != nil {
		t.Fatal(err)
	}
	if j.JSONMessage == nil {
		t.Fatal("json-message not decoded")
	}
	if err := applyJSONSources(&j); err != nil {
		t.Fatalf("applyJSONSources: %v", err)
	}
	var audio, video *JingleDescription
	for _, c := range j.Content {
		switch c.Name {
		case "audio":
			audio = c.Description
		case "video":
			video = c.Description
		}
	}

	wantVideo := []Source{
		{SSRC: "2369987446", Name: "jvb-v0", VideoType: "camera", Parameters: []Parameter{{Name: "msid", Value: "mixedmslabel mixedlabelvideo0"}}, SSRCInfo: &SSRCInfo{Owner: "jvb"}},
		{SSRC: "11", Name: "abcd1234-v0", VideoType: "camera", Parameters: []Parameter{{Name: "msid", Value: "m1 v"}}, SSRCInfo: &SSRCInfo{Owner: "abcd1234"}},
		{SSRC: "12", Name: "abcd1234-v0", VideoType: "camera", Parameters: []Parameter{{Name: "msid", Value: "m1 v"}}, SSRCInfo: &SSRCInfo{Owner: "abcd1234"}},
		{SSRC: "13", Name: "abcd1234-v1", VideoType: "desktop", SSRCInfo: &SSRCInfo{Owner: "abcd1234"}},
	}
	if !reflect.DeepEqual(video.Sources, wantVideo) {
		t.Errorf("video sources:\n got %+v\nwant %+v", video.Sources, wantVideo)
	}
	if want := []SSRCGroup{{Semantics: "FID", Sources: []SSRCGroupID{{SSRC: "11"}, {SSRC: "12"}}}}; !reflect.DeepEqual(video.SSRCGroups, want) {
		t.Errorf("video groups: got %+v, want %+v", video.SSRCGroups, want)
	}
	if len(audio.Sources) != 2 || audio.Sources[1].SSRC != "21" || audio.Sources[1].SSRCInfo.Owner != "abcd1234" || audio.Sources[1].VideoType != "" {
		t.Errorf("unexpected audio sources %+v", audio.Sources)
	}

	// The converted session splits like an XML one: the bridge's sources are
	// skipped, abcd1234 gets one audio and two video sections (camera with
	// its RTX, screen share).
	ss := newSessionSources(&j, 0, 0)
	offer, err := ss.offer()
	if err != nil {
		t.Fatal(err)
	}
	if got := mids(parseOffer(t, offer)); !reflect.DeepEqual(got, []string{"audio", "video", "audio-1", "video-2", "video-3"}) {
		t.Errorf("unexpected sections %v", got)
	}
}

// source-add / source-remove carry only the json-message: the audio and
// video contents are created for it. Simulcast groups map to SIM.
func TestApplyJSONSources_SourceAddWithoutContents(t *testing.T) {
	j := JingleIQ{Action: "source-add", SID: "s1", JSONMessage: &JSONMessage{Text: `{"sources":{"abcd1234":[[{"s":1},{"s":2},{"s":3}],[["s",1,2,3]],[]]}}`}}
	if err := applyJSONSources(&j); err != nil {
		t.Fatal(err)
	}
	if len(j.Content) != 1 || j.Content[0].Name != "video" || j.Content[0].Description == nil || j.Content[0].Description.Media != "video" {
		t.Fatalf("expected one video content, got %+v", j.Content)
	}
	d := j.Content[0].Description
	if len(d.Sources) != 3 || len(d.SSRCGroups) != 1 || d.SSRCGroups[0].Semantics != "SIM" || len(d.SSRCGroups[0].Sources) != 3 {
		t.Errorf("unexpected description %+v", d)
	}
}

func TestApplyJSONSources_Rejects(t *testing.T) {
	for name, text := range map[string]string{
		"not json":        `{`,
		"bad group":       `{"sources":{"a":[[],[["x",1,2]],[]]}}`,
		"ssrc not number": `{"sources":{"a":[[{"s":"one"}],[],[]]}}`,
	} {
		t.Run(name, func(t *testing.T) {
			j := JingleIQ{Action: "source-add", JSONMessage: &JSONMessage{Text: text}}
			if err := applyJSONSources(&j); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
	// Without a json-message nothing changes (XML sources).
	j := JingleIQ{Action: "source-add"}
	if err := applyJSONSources(&j); err != nil || len(j.Content) != 0 {
		t.Fatalf("expected a no-op, got %v %+v", err, j.Content)
	}
}
