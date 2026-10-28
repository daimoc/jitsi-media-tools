package session

import (
	"log/slog"
	"slices"
	"sync"
	"time"
)

// Received-video budget (FR-033, research R8): the session asks for a
// reduced selection whose estimated bitrate fits the budget, and corrects
// its estimates with the bitrate measured.
const (
	minBudget    = 100_000 // bits/s
	budgetWindow = 2 * time.Second
	upMargin     = 0.85             // Move up only to a level estimated within this share of the budget
	upAfter      = 10 * time.Second // Move up only this long after moving down
	overWindows  = 2                // Windows over budget before moving down
)

// layerRate is the typical bitrate of a simulcast layer, by height.
var layerRate = map[int]float64{180: 200e3, 360: 700e3, 720: 2.5e6}

// level is a reduced selection: the unpinned videos asked for and their
// height, and the pinned videos' height.
type level struct{ others, height, pinHeight int }

// counts are pinned and unpinned videos: in the room, or arriving.
type counts struct{ pins, others int }

// ladder lists the reduced selections of sel with pins pinned videos, from
// the configured one down: unpinned height to 180, then unpinned count one
// by one (keeping one video when nothing is pinned), then pinned height.
func ladder(sel Selection, pins int) []level {
	l := level{max(sel.Limit-pins, 0), sel.Height, sel.PinHeight}
	out := []level{l}
	for _, h := range heightsBelow(sel.Height) {
		l.height = h
		out = append(out, l)
	}
	least := 0
	if pins == 0 {
		least = 1
	}
	for l.others > least {
		l.others--
		out = append(out, l)
	}
	if pins > 0 {
		for _, h := range heightsBelow(sel.PinHeight) {
			l.pinHeight = h
			out = append(out, l)
		}
	}
	return out
}

// heightsBelow returns the allowed heights below h, highest first.
func heightsBelow(h int) []int {
	var out []int
	for _, a := range slices.Backward(allowedHeights) {
		if a < h {
			out = append(out, a)
		}
	}
	return out
}

// cost estimates the bitrate of level l for videos c.
func (l level) cost(c counts) float64 {
	return float64(c.pins)*layerRate[l.pinHeight] + float64(min(l.others, c.others))*layerRate[l.height]
}

// budgetCtl picks the level of the ladder to ask for.
type budgetCtl struct {
	budget   float64
	level    int
	scale    float64 // Measured over estimated bitrate
	over     int     // Consecutive windows over budget
	lastDown time.Time
}

func newBudgetCtl(budget int) *budgetCtl { return &budgetCtl{budget: float64(budget), scale: 1} }

// fit moves down, without waiting for a measure, when the room changed so
// that the level's estimate no longer fits (someone joined, say).
func (c *budgetCtl) fit(now time.Time, lad []level, room counts) {
	c.level = min(c.level, len(lad)-1)
	if i := c.fitting(lad, room, c.level, c.budget); i > c.level {
		c.level, c.lastDown = i, now
	}
}

// fitting returns the most generous level from first on whose estimate is
// within limit, or the last one.
func (c *budgetCtl) fitting(lad []level, room counts, first int, limit float64) int {
	for i := first; i < len(lad); i++ {
		if c.scale*lad[i].cost(room) <= limit {
			return i
		}
	}
	return len(lad) - 1
}

// step takes a window's measured bitrate of the videos arriving, and
// reports whether the level changed.
func (c *budgetCtl) step(now time.Time, lad []level, room, arriving counts, measured float64) bool {
	c.level = min(c.level, len(lad)-1) // The ladder shrinks with the pins
	if est := lad[c.level].cost(arriving); est > 0 && measured > 0 {
		c.scale = measured / est
	}
	if measured > c.budget {
		c.over++
	} else {
		c.over = 0
	}
	switch {
	case c.over >= overWindows && c.level < len(lad)-1:
		c.level = c.fitting(lad, room, c.level+1, c.budget)
		c.over, c.lastDown = 0, now
		return true
	case c.over == 0 && c.level > 0 && now.Sub(c.lastDown) >= upAfter:
		if i := c.fitting(lad, room, 0, upMargin*c.budget); i < c.level {
			c.level = i
			return true
		}
	}
	return false
}

// atBottom reports whether the controller is at the last level and over
// budget there.
func (c *budgetCtl) atBottom(lad []level) bool { return c.level == len(lad)-1 && c.over > 0 }

// videoMeter counts the received video bytes per participant.
type videoMeter struct {
	mu    sync.Mutex
	bytes map[string]int
}

func (v *videoMeter) add(owner string, n int) {
	v.mu.Lock()
	if v.bytes == nil {
		v.bytes = map[string]int{}
	}
	v.bytes[owner] += n
	v.mu.Unlock()
}

// take returns the counts since the last call.
func (v *videoMeter) take() map[string]int {
	v.mu.Lock()
	defer v.mu.Unlock()
	b := v.bytes
	v.bytes = nil
	return b
}

// roomCounts are the pinned videos the policy asks for (at most the
// limit) and the other participants who may send video.
func roomCounts(sel Selection, parts []Participant, videoSource func(id string) string) counts {
	var c counts
	for _, p := range parts {
		switch {
		case p.Pinned && videoSource(p.ID) != "" && c.pins < sel.Limit:
			c.pins++
		case !p.Pinned && !p.VideoMuted:
			c.others++
		}
	}
	return c
}

// budgetedLocked returns the selection to ask the bridge for: sel reduced
// to the budget's level, room being its videos. Called with s.mu held.
func (s *Session) budgetedLocked(sel Selection, room counts) Selection {
	if s.budget == nil {
		return sel
	}
	lad := ladder(sel, room.pins)
	s.budget.fit(time.Now(), lad, room)
	s.budgetStarted = true
	l := lad[s.budget.level]
	sel.Limit, sel.Height, sel.PinHeight = room.pins+l.others, l.height, l.pinHeight
	return sel
}

// runBudget measures the received video every window and moves the
// selection within the budget, until the session ends.
func (s *Session) runBudget() {
	t := time.NewTicker(budgetWindow)
	defer t.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case now := <-t.C:
			s.budgetTick(now)
		}
	}
}

func (s *Session) budgetTick(now time.Time) {
	bytes := s.meter.take()
	parts := s.reg.list()
	s.mu.Lock()
	sel := s.cfg.Selection
	s.mu.Unlock()
	room := roomCounts(sel, parts, s.reg.videoSource) // Not under s.mu: the registry has its own lock
	lad := ladder(sel, room.pins)
	s.mu.Lock()
	if s.budget == nil || !s.budgetStarted {
		s.mu.Unlock()
		return
	}
	var arriving counts
	total := 0
	for _, p := range parts {
		if n := bytes[p.ID]; n > 0 {
			total += n
			if p.Pinned {
				arriving.pins++
			} else {
				arriving.others++
			}
		}
	}
	measured := float64(total*8) / budgetWindow.Seconds()
	moved := s.budget.step(now, lad, room, arriving, measured)
	bottom := s.budget.atBottom(lad)
	l := lad[s.budget.level]
	s.mu.Unlock()
	s.logger.Debug("Video budget window", slog.Int("kbps", int(measured/1000)), slog.Int("arriving_pinned", arriving.pins),
		slog.Int("arriving_others", arriving.others), slog.Int("meter_owners", len(bytes)), slog.Int("level", s.budgetLevel()))
	if bottom && room.pins > 0 {
		s.warnOnce("budget", "The video budget cannot cover the pinned participants, even at 180p")
	}
	if moved {
		s.logger.Info("Video budget", slog.Int("kbps", int(measured/1000)), slog.Int("videos", room.pins+l.others),
			slog.Int("height", l.height), slog.Int("pinned_height", l.pinHeight))
		s.applySelection()
	}
}

// budgetLevel returns the controller's level, for logs.
func (s *Session) budgetLevel() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.budget == nil {
		return 0
	}
	return s.budget.level
}
