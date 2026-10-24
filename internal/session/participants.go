// Package session joins one Jitsi room as one participant and wires
// signaling, the WebRTC peer and the media processes together. It is the
// only package that sees both the signaling and the media side
// (constitution III).
package session

import (
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/daimoc/jitsi-media-tools/internal/signaling"
)

// Participant is another occupant of the room (data-model "Participant").
type Participant struct {
	ID         string // Endpoint id
	Name       string // Unique display name in the session
	AudioMuted bool
	VideoMuted bool
	Pinned     bool
	Selected   bool // Video received
	LastSpoke  time.Time
	order      int // Join order
	display    string
}

// EventKind is what happened to a participant.
type EventKind int

const (
	EventJoined EventKind = iota
	EventLeft
	EventChanged
)

// Event is a change in the room.
type Event struct {
	Kind        EventKind
	Participant Participant
}

// defaultName is the name of a participant without a display name.
const defaultName = "Participant"

// registry tracks the room's participants and their sources (research R9).
type registry struct {
	mu     sync.Mutex
	byID   map[string]*Participant
	pins   []string // Lower-cased
	srcs   map[uint32]signaling.RemoteSource
	joined int
}

func newRegistry() *registry {
	return &registry{byID: map[string]*Participant{}, srcs: map[uint32]signaling.RemoteSource{}}
}

// uniqueName returns base, or base (2), (3)... for the first one no other
// current participant has (case-insensitive).
func (r *registry) uniqueName(base, self string) string {
	taken := func(n string) bool {
		for id, p := range r.byID {
			if id != self && strings.EqualFold(p.Name, n) {
				return true
			}
		}
		return false
	}
	if !taken(base) {
		return base
	}
	for i := 2; ; i++ {
		if n := fmt.Sprintf("%s (%d)", base, i); !taken(n) {
			return n
		}
	}
}

func (r *registry) pinnedLocked(p *Participant) bool {
	for _, pin := range r.pins {
		if strings.EqualFold(p.Name, pin) || strings.EqualFold(p.display, pin) {
			return true
		}
	}
	return false
}

// occupant applies a presence and returns the resulting events.
func (r *registry) occupant(o signaling.Occupant) []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	p := r.byID[o.ID]
	if o.Left {
		if p == nil {
			return nil
		}
		delete(r.byID, o.ID)
		for ssrc, s := range r.srcs {
			if s.Owner == o.ID {
				delete(r.srcs, ssrc)
			}
		}
		return []Event{{EventLeft, *p}}
	}
	display := o.DisplayName
	if display == "" {
		display = defaultName
	}
	if p == nil {
		r.joined++
		p = &Participant{ID: o.ID, order: r.joined, display: display, AudioMuted: o.AudioMuted, VideoMuted: o.VideoMuted}
		p.Name = r.uniqueName(display, o.ID)
		p.Pinned = r.pinnedLocked(p)
		r.byID[o.ID] = p
		return []Event{{EventJoined, *p}}
	}
	before := *p
	if display != p.display {
		p.display = display
		p.Name = r.uniqueName(display, o.ID)
		p.Pinned = r.pinnedLocked(p)
	}
	p.AudioMuted, p.VideoMuted = o.AudioMuted, o.VideoMuted
	if *p == before {
		return nil
	}
	return []Event{{EventChanged, *p}}
}

// setPins sets the pinned display names and returns the participants whose
// pinned state changed.
func (r *registry) setPins(names []string) []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pins = nil
	for _, n := range names {
		r.pins = append(r.pins, strings.ToLower(strings.TrimSpace(n)))
	}
	var evs []Event
	for _, p := range r.sortedLocked() {
		if pinned := r.pinnedLocked(p); pinned != p.Pinned {
			p.Pinned = pinned
			evs = append(evs, Event{EventChanged, *p})
		}
	}
	return evs
}

// speaker records the dominant speaker at now; previous speakers (most
// recent first) get slightly earlier times, so they keep their order.
func (r *registry) speaker(id string, previous []string, now time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if p := r.byID[id]; p != nil {
		p.LastSpoke = now
	}
	for i, prev := range previous {
		if p := r.byID[prev]; p != nil && p.LastSpoke.Before(now) {
			t := now.Add(-time.Duration(i+1) * time.Millisecond)
			if t.After(p.LastSpoke) {
				p.LastSpoke = t
			}
		}
	}
}

// sources replaces the negotiated remote sources.
func (r *registry) sources(list []signaling.RemoteSource) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.srcs = make(map[uint32]signaling.RemoteSource, len(list))
	for _, s := range list {
		r.srcs[s.SSRC] = s
	}
}

// source returns the remote source of an SSRC.
func (r *registry) source(ssrc uint32) (signaling.RemoteSource, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.srcs[ssrc]
	return s, ok
}

// videoSource returns the name of a participant's video source ("" if none).
func (r *registry) videoSource(id string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	best := ""
	for _, s := range r.srcs {
		if s.Owner == id && s.Kind == "video" && (best == "" || s.Name < best) {
			best = s.Name
		}
	}
	return best
}

// get returns a copy of a participant (nil if absent).
func (r *registry) get(id string) *Participant {
	r.mu.Lock()
	defer r.mu.Unlock()
	if p := r.byID[id]; p != nil {
		c := *p
		return &c
	}
	return nil
}

// setSelected records whether a participant's video is received; it
// reports a change event, if any.
func (r *registry) setSelected(id string, selected bool) (Event, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p := r.byID[id]
	if p == nil || p.Selected == selected {
		return Event{}, false
	}
	p.Selected = selected
	return Event{EventChanged, *p}, true
}

// list returns copies of the participants in join order.
func (r *registry) list() []Participant {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []Participant
	for _, p := range r.sortedLocked() {
		out = append(out, *p)
	}
	return out
}

func (r *registry) sortedLocked() []*Participant {
	ps := make([]*Participant, 0, len(r.byID))
	for _, p := range r.byID {
		ps = append(ps, p)
	}
	slices.SortFunc(ps, func(a, b *Participant) int { return a.order - b.order })
	return ps
}
