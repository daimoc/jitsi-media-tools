package jitsi

import (
	"bytes"
	"encoding/xml"
	"strings"
	"testing"

	"mellium.im/xmlstream"
	"mellium.im/xmpp/jid"
)

func TestMUCJoinPresence_HasMUCChildElement(t *testing.T) {
	var buf bytes.Buffer
	enc := xml.NewEncoder(&buf)
	if _, err := xmlstream.Copy(enc, mucJoinPresence("room@conference.example.org/nick", "bot <1>", nil)); err != nil {
		t.Fatalf("encode: %v", err)
	}
	if err := enc.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	got := buf.String()

	if !strings.Contains(got, `to="room@conference.example.org/nick"`) {
		t.Errorf("expected presence addressed to the room JID, got %s", got)
	}
	// XEP-0045 §7.2.2: the join request carries an <x xmlns='http://jabber.org/protocol/muc'/> child.
	if !strings.Contains(got, `<x xmlns="http://jabber.org/protocol/muc"></x>`) {
		t.Errorf("expected MUC <x/> child element, got %s", got)
	}
	if strings.Contains(got, `x=""`) {
		t.Errorf("MUC <x/> must be a child element, not an attribute, got %s", got)
	}
	// XEP-0172 display name, escaped.
	if !strings.Contains(got, `<nick xmlns="http://jabber.org/protocol/nick">bot &lt;1&gt;</nick>`) {
		t.Errorf("expected the display name in a <nick/> child, got %s", got)
	}
}

// TestMUCJoinPresence_AnnouncesCodecs checks the codec list lib-jitsi-meet
// reads to choose the codec everyone can receive: without it, browsers
// prefer AV1, which the tool does not receive.
func TestMUCJoinPresence_AnnouncesCodecs(t *testing.T) {
	var buf bytes.Buffer
	enc := xml.NewEncoder(&buf)
	if _, err := xmlstream.Copy(enc, mucJoinPresence("room@muc/nick", "bot", nil)); err != nil {
		t.Fatal(err)
	}
	enc.Flush()
	if got := buf.String(); !strings.Contains(got, `<jitsi_participant_codecList>vp8,vp9,h264</jitsi_participant_codecList>`) {
		t.Errorf("no codec list in %s", got)
	}
}

// TestSourcesPresence_ReceiveOnly checks that a participant sending nothing
// still announces an (empty) SourceInfo: lib-jitsi-meet reads the codec
// list only from participants whose presence has one.
func TestSourcesPresence_ReceiveOnly(t *testing.T) {
	var buf bytes.Buffer
	enc := xml.NewEncoder(&buf)
	if _, err := xmlstream.Copy(enc, mucJoinPresence("room@muc/nick", "bot", sourcesPresence("nick", nil))); err != nil {
		t.Fatal(err)
	}
	enc.Flush()
	got := buf.String()
	if !strings.Contains(got, "<SourceInfo>{}</SourceInfo>") {
		t.Errorf("no empty SourceInfo in %s", got)
	}
	if strings.Contains(got, "muted") {
		t.Errorf("mute state for no sources in %s", got)
	}
}

func TestOccupantNick(t *testing.T) {
	for local, want := range map[string]string{
		"7835b072-6f68-4d2f-a046-6735e07d6c74@meet.jitsi/res": "7835b072", // lib-jitsi-meet's choice
		"abc_defgh@meet.jitsi/res":                            "abc_defg",
	} {
		if got := occupantNick(jid.MustParse(local)); got != want {
			t.Errorf("occupantNick(%s) = %q, want %q", local, got, want)
		}
	}
	// Localparts that would be rejected by Prosody fall back to a random nick.
	for _, local := range []string{"short@meet.jitsi", "_underscore@meet.jitsi", "anon-1234@meet.jitsi", "meet.jitsi"} {
		got := occupantNick(jid.MustParse(local))
		if !validNick.MatchString(got) || len(got) != 8 {
			t.Errorf("occupantNick(%s) = %q, want a valid 8-character nick", local, got)
		}
	}
}
