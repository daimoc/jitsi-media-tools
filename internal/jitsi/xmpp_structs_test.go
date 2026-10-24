package jitsi

import (
	"encoding/xml"
	"strings"
	"testing"
)

func TestJingleReason_MarshalsConditionAsChildElement(t *testing.T) {
	jingle := &JingleIQ{
		Action: "session-terminate",
		SID:    "abc",
		Reason: &JingleReason{Condition: "success"},
	}

	out, err := xml.Marshal(jingle)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(out)

	// XEP-0166: <reason><success/></reason>
	if !strings.Contains(got, "<reason><success></success></reason>") {
		t.Errorf("expected <reason><success/></reason>, got %s", got)
	}
	if strings.Contains(got, "<Condition>") {
		t.Errorf("condition must not be serialised under the Go field name, got %s", got)
	}
}

func TestJingleReason_UnmarshalsConditionFromChildElement(t *testing.T) {
	in := `<jingle xmlns="urn:xmpp:jingle:1" action="session-terminate" sid="abc"><reason><decline/><text>bye</text></reason></jingle>`

	var jingle JingleIQ
	if err := xml.Unmarshal([]byte(in), &jingle); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if jingle.Reason == nil {
		t.Fatal("expected reason to be parsed")
	}
	if jingle.Reason.Condition != "decline" {
		t.Errorf("expected condition %q, got %q", "decline", jingle.Reason.Condition)
	}
	if jingle.Reason.Text != "bye" {
		t.Errorf("expected text %q, got %q", "bye", jingle.Reason.Text)
	}
}

func TestSource_JitsiAttributesRoundTrip(t *testing.T) {
	src := Source{SSRC: "42", Name: "abcd1234-v0", VideoType: "camera", SSRCInfo: &SSRCInfo{Owner: "room@muc/abcd1234"},
		Parameters: []Parameter{{Name: "msid", Value: "s t"}}}
	out, err := xml.Marshal(src)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(out)
	for _, want := range []string{`name="abcd1234-v0"`, `videoType="camera"`, `<ssrc-info xmlns="http://jitsi.org/jitmeet" owner="room@muc/abcd1234"></ssrc-info>`} {
		if !strings.Contains(got, want) {
			t.Errorf("expected %s in %s", want, got)
		}
	}
	var back Source
	if err := xml.Unmarshal(out, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.VideoType != "camera" || back.SSRCInfo == nil || back.SSRCInfo.Owner != "room@muc/abcd1234" {
		t.Errorf("round trip lost attributes: %+v", back)
	}
}
