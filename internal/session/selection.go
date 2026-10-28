package session

import (
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/daimoc/jitsi-media-tools/internal/signaling"
)

// Selection is which video the session receives (data-model "Selection").
type Selection struct {
	Limit     int      // Videos at once; default 9, at most 25
	Height    int      // 180 (default), 360 or 720
	Pins      []string // Display names always received
	PinHeight int      // Height of pinned videos; default 720
	Budget    int      // Received video in bits/s; 0 for none (FR-033)
}

const maxVideoLimit = 25

var allowedHeights = []int{180, 360, 720}

func (sel Selection) withDefaults() Selection {
	if sel.Limit == 0 {
		sel.Limit = 9
	}
	if sel.Height == 0 {
		sel.Height = 180
	}
	if sel.PinHeight == 0 {
		sel.PinHeight = 720
	}
	return sel
}

func (sel Selection) validate() error {
	if sel.Limit < 1 || sel.Limit > maxVideoLimit {
		return fmt.Errorf("video limit %d: want 1 to %d", sel.Limit, maxVideoLimit)
	}
	if !slices.Contains(allowedHeights, sel.Height) {
		return fmt.Errorf("video height %d: want 180, 360 or 720", sel.Height)
	}
	if !slices.Contains(allowedHeights, sel.PinHeight) {
		return fmt.Errorf("pinned video height %d: want 180, 360 or 720", sel.PinHeight)
	}
	if sel.Budget != 0 && sel.Budget < minBudget {
		return fmt.Errorf("video budget %d bit/s: want at least %d", sel.Budget, minBudget)
	}
	return nil
}

// policy computes what to ask the bridge for (research R8): the Limit most
// recent speakers, always including the pinned participants (the first
// Limit of them), pinned ones at PinHeight. It also returns the pinned
// participants left out.
func (sel Selection) policy(parts []Participant, videoSource func(id string) string) (*signaling.ReceivePolicy, []string) {
	p := &signaling.ReceivePolicy{LastN: sel.Limit, DefaultMaxHeight: sel.Height}
	var dropped []string
	for _, part := range parts {
		if !part.Pinned {
			continue
		}
		name := videoSource(part.ID)
		if name == "" {
			continue
		}
		if len(p.Selected) >= sel.Limit {
			dropped = append(dropped, part.Name)
			continue
		}
		p.Selected = append(p.Selected, name)
		if sel.PinHeight != sel.Height {
			if p.MaxHeights == nil {
				p.MaxHeights = map[string]int{}
			}
			p.MaxHeights[name] = sel.PinHeight
		}
	}
	p.OnStage = slices.Clone(p.Selected)
	return p, dropped
}

// applySelection sends the current policy to the bridge.
func (s *Session) applySelection() {
	if s.sig == nil {
		return
	}
	if s.cfg.SendOnly {
		s.sig.SetReceivePolicy(s.cfg.DisplayName, &signaling.ReceivePolicy{LastN: 0})
		return
	}
	parts := s.reg.list()
	s.mu.Lock()
	sel := s.cfg.Selection
	s.mu.Unlock()
	asked := sel
	if sel.Budget > 0 {
		room := roomCounts(sel, parts, s.reg.videoSource)
		s.mu.Lock()
		asked = s.budgetedLocked(sel, room)
		s.mu.Unlock()
	}
	p, dropped := asked.policy(parts, s.reg.videoSource)
	if len(dropped) > 0 {
		s.warnOnce("pins", "More pinned participants than the video limit; not pinned: "+strings.Join(dropped, ", "))
	}
	p.AssumedBandwidth = sel.Budget
	s.sig.SetReceivePolicy(s.cfg.DisplayName, p)
}

// sendEndpointStats reports the participant's connection statistics to
// the meeting.
func (s *Session) sendEndpointStats(st signaling.EndpointStats) {
	if s.sig != nil {
		s.sig.SendEndpointStats(s.cfg.DisplayName, st)
	}
}

// SetSelection changes the selection at any time.
func (s *Session) SetSelection(sel Selection) error {
	sel = sel.withDefaults()
	if err := sel.validate(); err != nil {
		return err
	}
	s.mu.Lock()
	s.cfg.Selection = sel
	s.budget, s.budgetStarted = nil, false
	if sel.Budget > 0 {
		s.budget = newBudgetCtl(sel.Budget)
	}
	s.mu.Unlock()
	s.emit(s.reg.setPins(sel.Pins)...)
	s.applySelection()
	s.media.selectionChanged()
	return nil
}

// warnOnce logs a warning once per key.
func (s *Session) warnOnce(key, msg string) {
	s.mu.Lock()
	if s.warned == nil {
		s.warned = map[string]bool{}
	}
	done := s.warned[key]
	s.warned[key] = true
	s.mu.Unlock()
	if !done {
		s.logger.Warn(msg, slog.String("key", key))
	}
}
