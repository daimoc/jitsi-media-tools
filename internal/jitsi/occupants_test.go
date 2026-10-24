package jitsi

import (
	"encoding/xml"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/daimoc/jitsi-media-tools/internal/signaling"
)

func decodePresence(t *testing.T, file string) mucPresence {
	t.Helper()
	b, err := os.ReadFile("testdata/presence/" + file)
	if err != nil {
		t.Fatal(err)
	}
	var p mucPresence
	if err := xml.Unmarshal(b, &p); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPresence_Occupant(t *testing.T) {
	p := decodePresence(t, "alice.xml")
	got := p.occupant("abcd1234")
	// SourceInfo wins over the legacy <audiomuted>false</audiomuted>.
	want := signaling.Occupant{ID: "abcd1234", DisplayName: "Alice", AudioMuted: true, VideoMuted: false}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
	p = decodePresence(t, "legacy-left.xml")
	got = p.occupant("beef0001")
	want = signaling.Occupant{ID: "beef0001", DisplayName: "Bob", AudioMuted: true, Left: true}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestPolicyConstraintsMessage(t *testing.T) {
	got := policyConstraintsMessage(&signaling.ReceivePolicy{
		LastN: 9, Selected: []string{"abcd1234-v0"}, OnStage: []string{"abcd1234-v0"},
		DefaultMaxHeight: 180, MaxHeights: map[string]int{"abcd1234-v0": 720},
	})
	want := `{"colibriClass":"ReceiverVideoConstraints","lastN":9,"selectedSources":["abcd1234-v0"],"onStageSources":["abcd1234-v0"],"defaultConstraints":{"maxHeight":180},"constraints":{"abcd1234-v0":{"maxHeight":720}}}`
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	got = policyConstraintsMessage(&signaling.ReceivePolicy{LastN: 4, DefaultMaxHeight: 360})
	want = `{"colibriClass":"ReceiverVideoConstraints","lastN":4,"defaultConstraints":{"maxHeight":360}}`
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

type sentTexts struct {
	mu   sync.Mutex
	msgs []string
}

func (s *sentTexts) SendText(m string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.msgs = append(s.msgs, m)
	return nil
}

func TestBridgeChannel_PolicyAndDominantSpeaker(t *testing.T) {
	b := newBridgeChannel(quietLogger)
	var speaker string
	var previous []string
	b.onSpeaker = func(id string, prev []string) { speaker, previous = id, prev }
	dc := &sentTexts{}
	b.setChannel(dc)
	b.OnOpen()
	b.setPolicy(&signaling.ReceivePolicy{LastN: 9, DefaultMaxHeight: 180})
	dc.mu.Lock()
	last := dc.msgs[len(dc.msgs)-1]
	dc.mu.Unlock()
	if !strings.Contains(last, `"lastN":9`) {
		t.Errorf("last message %s, want the policy", last)
	}
	b.OnMessage(`{"colibriClass":"DominantSpeakerEndpointChangeEvent","dominantSpeakerEndpoint":"abcd1234","previousSpeakers":["beef0001"]}`)
	if speaker != "abcd1234" || !reflect.DeepEqual(previous, []string{"beef0001"}) {
		t.Errorf("speaker %q previous %v", speaker, previous)
	}
	b.stop()
}

func TestSessionSources_RemoteSources(t *testing.T) {
	ss := &sessionSources{sections: []sourceSection{
		{content: "video", src: &remoteSource{owner: "abcd1234", primary: "111", sources: []Source{{SSRC: "111", Name: "abcd1234-v0"}}}},
		{content: "audio", src: &remoteSource{primary: "222", sources: []Source{{SSRC: "222", Name: "beef0001-a0"}}}},
		{content: "audio", src: nil},
	}}
	got := ss.remoteSources()
	want := []signaling.RemoteSource{
		{SSRC: 111, Kind: "video", Name: "abcd1234-v0", Owner: "abcd1234"},
		{SSRC: 222, Kind: "audio", Name: "beef0001-a0", Owner: "beef0001"}, // Owner from the name
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
}
