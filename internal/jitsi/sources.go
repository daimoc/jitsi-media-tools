package jitsi

import (
	"fmt"
	"strings"

	"github.com/pion/webrtc/v3"
)

// Jicofo describes a session Plan-B style: one Jingle content per media
// kind, listing every remote participant's sources. Pion follows Unified
// Plan and starts one receiver per SDP media section, so the offer given to
// the media side has one section per remote source (as lib-jitsi-meet does):
//
//   - the contents themselves (audio, video, data), without remote sources,
//     carry the bot's own tracks;
//   - each remote source gets a sendonly section, with a mid derived from
//     its content ("audio-1", "video-2", ...), bundled with the rest.
//
// Sections are never dropped or reused: a removed source leaves an inactive
// section without SSRC, so that mids and section order stay stable across
// renegotiations (Pion v3 does not rebind a receiver to a new SSRC).
// The bridge's own sources (owner "jvb") are left out: they carry no
// participant media.

// sessionSources is the remote description of one Jingle session. It is
// guarded by SignalingClient.mu.
type sessionSources struct {
	base      *JingleIQ       // The offer's contents, without remote sources
	contents  map[string]bool // Jingle content names
	sections  []sourceSection // One per remote source, in creation order
	byPrimary map[string]int  // Primary SSRC -> index in sections
	next      int             // Counter for section mids
	version   uint64          // o= session version of the last rendered offer
	changed   bool            // Sources changed since the last rendered offer

	// Receive cap (FR-015): at most limit active sections per content (0:
	// no cap); sources beyond it wait, oldest first, for an active one to go.
	limit   int
	height  int                        // Video height asked for each received video (0: default)
	layers  []int                      // Heights of the simulcast layers the remote bots send; nil without
	active  map[string]int             // Content -> sections with a source
	waiting map[string][]*remoteSource // Content -> sources beyond the cap
	queued  map[string]bool            // Primary SSRCs in waiting
}

// sourceSection is the SDP section of one remote source.
type sourceSection struct {
	content string // Jingle content (and media kind) it belongs to
	mid     string
	src     *remoteSource // Nil once removed
}

// remoteSource is a remote stream: its primary SSRC with its RTX, as Jingle
// sources and FID groups.
type remoteSource struct {
	owner   string // Occupant nick, from ssrc-info (empty if unknown)
	primary string
	sources []Source
	groups  []SSRCGroup
}

// newSessionSources builds the state from a session-initiate, offering at
// most limit remote sources per content (0: no limit).
func newSessionSources(offer *JingleIQ, limit, height int) *sessionSources {
	ss := &sessionSources{
		base:      &JingleIQ{Action: offer.Action, SID: offer.SID, Initiator: offer.Initiator, Groups: offer.Groups},
		contents:  map[string]bool{},
		byPrimary: map[string]int{},
		limit:     limit,
		height:    height,
		active:    map[string]int{},
		waiting:   map[string][]*remoteSource{},
		queued:    map[string]bool{},
	}
	for _, c := range offer.Content {
		ss.contents[c.Name] = true
		if c.Description != nil {
			d := *c.Description
			d.Sources, d.SSRCGroups = nil, nil
			c.Description = &d
		}
		ss.base.Content = append(ss.base.Content, c)
	}
	ss.add(offer)
	ss.changed = true
	return ss
}

// isContent reports whether mid is one of the session's Jingle contents (as
// opposed to a per-source section).
func (ss *sessionSources) isContent(mid string) bool {
	return ss.contents[mid]
}

// add records the sources of a session-initiate or source-add and returns
// how many new remote streams it offers (sources beyond the cap wait).
func (ss *sessionSources) add(j *JingleIQ) int {
	added := 0
	for _, c := range j.Content {
		if c.Description == nil || !ss.contents[c.Name] {
			continue
		}
		for _, rs := range splitSources(c.Description) {
			if _, exists := ss.byPrimary[rs.primary]; exists || ss.queued[rs.primary] {
				continue
			}
			if ss.limit > 0 && ss.active[c.Name] >= ss.limit {
				ss.waiting[c.Name] = append(ss.waiting[c.Name], rs)
				ss.queued[rs.primary] = true
				continue
			}
			ss.addSection(c.Name, rs)
			added++
		}
	}
	return added
}

// addSection gives a remote source its own section.
func (ss *sessionSources) addSection(content string, rs *remoteSource) {
	ss.next++
	ss.byPrimary[rs.primary] = len(ss.sections)
	ss.sections = append(ss.sections, sourceSection{content: content, mid: fmt.Sprintf("%s-%d", content, ss.next), src: rs})
	ss.active[content]++
	ss.changed = true
}

// removeSection makes section i inactive and promotes the oldest waiting
// source of its content.
func (ss *sessionSources) removeSection(i int) {
	content := ss.sections[i].content
	ss.sections[i].src = nil
	ss.active[content]--
	ss.changed = true
	if w := ss.waiting[content]; len(w) > 0 {
		ss.waiting[content] = w[1:]
		delete(ss.queued, w[0].primary)
		ss.addSection(content, w[0])
	}
}

// unqueue drops the waiting sources for which drop returns true.
func (ss *sessionSources) unqueue(drop func(*remoteSource) bool) {
	for content, w := range ss.waiting {
		kept := w[:0]
		for _, rs := range w {
			if drop(rs) {
				delete(ss.queued, rs.primary)
				continue
			}
			kept = append(kept, rs)
		}
		ss.waiting[content] = kept
	}
}

// remove forgets the remote streams whose primary SSRC is listed in a
// source-remove and returns how many it removed.
func (ss *sessionSources) remove(j *JingleIQ) int {
	removed := 0
	for _, c := range j.Content {
		if c.Description == nil {
			continue
		}
		for _, src := range c.Description.Sources {
			ssrc := src.SSRC
			ss.unqueue(func(rs *remoteSource) bool { return rs.primary == ssrc })
			i, ok := ss.byPrimary[ssrc]
			if !ok || ss.sections[i].src == nil {
				continue
			}
			ss.removeSection(i)
			removed++
		}
	}
	return removed
}

// removeOwner forgets the remote streams owned by an occupant (nick) and
// returns how many it removed.
func (ss *sessionSources) removeOwner(nick string) int {
	ss.unqueue(func(rs *remoteSource) bool { return rs.owner == nick })
	removed := 0
	for i := range ss.sections {
		if src := ss.sections[i].src; src != nil && src.owner == nick {
			ss.removeSection(i)
			removed++
		}
	}
	return removed
}

// receivedNames returns the names of the primary sources of the active
// sections of content, in section order (at most limit), and whether one of
// them has no name (it is left out of names).
func (ss *sessionSources) receivedNames(content string) (names []string, unnamed bool) {
	for _, sec := range ss.sections {
		if sec.content != content || sec.src == nil {
			continue
		}
		name := ""
		for _, src := range sec.src.sources {
			if src.SSRC == sec.src.primary {
				name = src.Name
				break
			}
		}
		if name == "" {
			unnamed = true
			continue
		}
		names = append(names, name)
	}
	return names, unnamed
}

// receivePrefs returns what the session receives, for the bridge channel.
func (ss *sessionSources) receivePrefs() receivePrefs {
	audio, audioUnnamed := ss.receivedNames("audio")
	video, videoUnnamed := ss.receivedNames("video")
	return receivePrefs{capped: ss.limit > 0, audio: audio, audioUnnamed: audioUnnamed, video: video, videoUnnamed: videoUnnamed, height: ss.height, layers: ss.layers}
}

// offer renders the current state as an SDP offer, with a new session
// version, and clears changed.
func (ss *sessionSources) offer() (*webrtc.SessionDescription, error) {
	j := &JingleIQ{Action: ss.base.Action, SID: ss.base.SID, Initiator: ss.base.Initiator}
	j.Content = append(j.Content, ss.base.Content...)

	byName := map[string]JingleContent{}
	for _, c := range ss.base.Content {
		byName[c.Name] = c
	}
	for _, sec := range ss.sections {
		main := byName[sec.content]
		desc := *main.Description
		c := JingleContent{Creator: "initiator", Name: sec.mid, Senders: "none", Description: &desc}
		if sec.src != nil {
			c.Senders = "initiator" // The remote side sends: sendonly in the offer
			desc.Sources, desc.SSRCGroups = sec.src.sources, sec.src.groups
		}
		if main.Transport != nil {
			// Bundled: same ICE/DTLS parameters, candidates only once.
			tr := *main.Transport
			tr.Candidates, tr.SctpMap = nil, nil
			c.Transport = &tr
		}
		j.Content = append(j.Content, c)
	}

	bundle := JingleGroup{Semantics: "BUNDLE"}
	for _, c := range j.Content {
		bundle.Contents = append(bundle.Contents, JingleGroupEntry{Name: c.Name})
	}
	j.Groups = []JingleGroup{bundle}
	for _, g := range ss.base.Groups {
		if g.Semantics != "BUNDLE" {
			j.Groups = append(j.Groups, g)
		}
	}

	ss.version++
	ss.changed = false
	return renderSDP(j, ss.version)
}

// splitSources groups the sources of a description into remote streams: a
// primary SSRC with its RTX (FID group). For simulcast (SIM group) only the
// first layer is kept, as the bridge forwards the selected layer on it. The
// bridge's own sources are skipped.
func splitSources(desc *JingleDescription) []*remoteSource {
	bySSRC := map[string]Source{}
	for _, s := range desc.Sources {
		bySSRC[s.SSRC] = s
	}
	secondary := map[string]bool{} // RTX and non-first simulcast layers
	rtx := map[string][]string{}   // Primary -> RTX SSRCs
	for _, g := range desc.SSRCGroups {
		if len(g.Sources) < 2 {
			continue
		}
		switch g.Semantics {
		case "FID":
			primary := g.Sources[0].SSRC
			for _, s := range g.Sources[1:] {
				secondary[s.SSRC] = true
				rtx[primary] = append(rtx[primary], s.SSRC)
			}
		case "SIM":
			for _, s := range g.Sources[1:] {
				secondary[s.SSRC] = true
			}
		}
	}
	// RTX of a dropped simulcast layer is dropped too.
	for primary, rs := range rtx {
		if secondary[primary] {
			for _, r := range rs {
				secondary[r] = true
			}
		}
	}

	var out []*remoteSource
	for _, s := range desc.Sources {
		if secondary[s.SSRC] || isBridgeSource(s) {
			continue
		}
		rs := &remoteSource{owner: sourceOwner(s), primary: s.SSRC, sources: []Source{s}}
		if len(rtx[s.SSRC]) > 0 {
			g := SSRCGroup{Semantics: "FID", Sources: []SSRCGroupID{{SSRC: s.SSRC}}}
			for _, r := range rtx[s.SSRC] {
				if src, ok := bySSRC[r]; ok {
					rs.sources = append(rs.sources, src)
					g.Sources = append(g.Sources, SSRCGroupID{SSRC: r})
				}
			}
			if len(g.Sources) > 1 {
				rs.groups = []SSRCGroup{g}
			}
		}
		out = append(out, rs)
	}
	return out
}

// isBridgeSource reports whether a source is the bridge's own (owner "jvb",
// named "jvb-a0" / "jvb-v0" by Jicofo) rather than a participant's.
func isBridgeSource(s Source) bool {
	return (s.SSRCInfo != nil && s.SSRCInfo.Owner == "jvb") || strings.HasPrefix(s.Name, "jvb-")
}

// sourceOwner returns the occupant nick owning a source: Jicofo sets the
// ssrc-info owner to the nick, or to the occupant JID.
func sourceOwner(s Source) string {
	if s.SSRCInfo == nil {
		return ""
	}
	owner := s.SSRCInfo.Owner
	if i := strings.LastIndex(owner, "/"); i >= 0 {
		owner = owner[i+1:]
	}
	return owner
}
