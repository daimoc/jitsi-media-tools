package session

import (
	"testing"
	"time"

	"github.com/daimoc/jitsi-media-tools/internal/signaling"
)

func kinds(evs []Event) []EventKind {
	var k []EventKind
	for _, e := range evs {
		k = append(k, e.Kind)
	}
	return k
}

func TestRegistry_UniqueNamesAndRejoin(t *testing.T) {
	r := newRegistry()
	r.occupant(signaling.Occupant{ID: "a", DisplayName: "Alice"})
	r.occupant(signaling.Occupant{ID: "b", DisplayName: "alice"}) // Case differs: still a duplicate
	r.occupant(signaling.Occupant{ID: "c", DisplayName: ""})
	if got := r.get("a").Name; got != "Alice" {
		t.Errorf("a: %q", got)
	}
	if got := r.get("b").Name; got != "alice (2)" {
		t.Errorf("b: %q, want alice (2)", got)
	}
	if got := r.get("c").Name; got != "Participant" {
		t.Errorf("c: %q, want Participant", got)
	}
	// Alice leaves and rejoins under a new endpoint id: her name comes back.
	evs := r.occupant(signaling.Occupant{ID: "a", Left: true})
	if len(evs) != 1 || evs[0].Kind != EventLeft || evs[0].Participant.Name != "Alice" {
		t.Errorf("leave events %+v", evs)
	}
	evs = r.occupant(signaling.Occupant{ID: "a2", DisplayName: "Alice"})
	if len(evs) != 1 || evs[0].Kind != EventJoined || evs[0].Participant.Name != "Alice" {
		t.Errorf("rejoin events %+v", evs)
	}
}

func TestRegistry_ChangesAndRepeats(t *testing.T) {
	r := newRegistry()
	r.occupant(signaling.Occupant{ID: "a", DisplayName: "Alice"})
	if evs := r.occupant(signaling.Occupant{ID: "a", DisplayName: "Alice"}); len(evs) != 0 {
		t.Errorf("repeated presence gave %v", kinds(evs))
	}
	evs := r.occupant(signaling.Occupant{ID: "a", DisplayName: "Alice B", VideoMuted: true})
	if len(evs) != 1 || evs[0].Kind != EventChanged || evs[0].Participant.Name != "Alice B" || !evs[0].Participant.VideoMuted {
		t.Errorf("change events %+v", evs)
	}
	if evs := r.occupant(signaling.Occupant{ID: "zz", Left: true}); len(evs) != 0 {
		t.Errorf("unknown leave gave %v", kinds(evs))
	}
}

func TestRegistry_PinsSpeakersSources(t *testing.T) {
	r := newRegistry()
	r.occupant(signaling.Occupant{ID: "a", DisplayName: "Alice"})
	r.occupant(signaling.Occupant{ID: "b", DisplayName: "Bob"})
	evs := r.setPins([]string{"bob", "Carol"})
	if len(evs) != 1 || evs[0].Participant.ID != "b" || !evs[0].Participant.Pinned {
		t.Errorf("pin events %+v", evs)
	}
	// Carol joins later: the pin applies.
	evs = r.occupant(signaling.Occupant{ID: "c", DisplayName: "CAROL"})
	if !evs[0].Participant.Pinned {
		t.Errorf("Carol not pinned on join: %+v", evs)
	}
	now := time.Unix(100, 0)
	r.speaker("a", []string{"b"}, now)
	if !r.get("a").LastSpoke.Equal(now) || !r.get("b").LastSpoke.Before(now) || r.get("b").LastSpoke.IsZero() {
		t.Errorf("LastSpoke a %v b %v", r.get("a").LastSpoke, r.get("b").LastSpoke)
	}
	r.sources([]signaling.RemoteSource{{SSRC: 5, Kind: "video", Name: "a-v0", Owner: "a"}})
	if src, ok := r.source(5); !ok || src.Owner != "a" {
		t.Errorf("source 5: %+v %v", src, ok)
	}
	if name := r.videoSource("a"); name != "a-v0" {
		t.Errorf("video source of a: %q", name)
	}
}

func TestRegistry_ScreensAndCamera(t *testing.T) {
	r := newRegistry()
	r.occupant(signaling.Occupant{ID: "a", DisplayName: "Alice"})
	r.sources([]signaling.RemoteSource{
		{SSRC: 5, Kind: "video", Name: "a-v0", Owner: "a"},
		{SSRC: 6, Kind: "video", Name: "a-v1", Owner: "a", Desktop: true},
		{SSRC: 7, Kind: "video", Name: "b-v0", Owner: "b"},
	})
	if got := r.screens(); len(got) != 1 || !got["a"] {
		t.Errorf("screens %v, want a", got)
	}
	// The share stops: the source stays negotiated, muted in presence.
	r.occupant(signaling.Occupant{ID: "a", DisplayName: "Alice", SourceMuted: map[string]bool{"a-v0": false, "a-v1": true}})
	if got := r.screens(); len(got) != 0 {
		t.Errorf("screens after the share %v, want none", got)
	}
	// It starts again.
	r.occupant(signaling.Occupant{ID: "a", DisplayName: "Alice", SourceMuted: map[string]bool{"a-v0": false, "a-v1": false}})
	if got := r.screens(); !got["a"] {
		t.Errorf("screens after sharing again %v, want a", got)
	}
	// A presence announcing sources but not this one: no share.
	r.occupant(signaling.Occupant{ID: "a", DisplayName: "Alice", SourceMuted: map[string]bool{"a-v0": false}})
	if got := r.screens(); len(got) != 0 {
		t.Errorf("screens without the source in presence %v, want none", got)
	}
	// The pinned source stays the camera, even when the screen sorts first.
	r.sources([]signaling.RemoteSource{
		{SSRC: 6, Kind: "video", Name: "a-v0", Owner: "a", Desktop: true},
		{SSRC: 5, Kind: "video", Name: "a-v1", Owner: "a"},
	})
	if name := r.videoSource("a"); name != "a-v1" {
		t.Errorf("video source of a: %q, want the camera a-v1", name)
	}
}
