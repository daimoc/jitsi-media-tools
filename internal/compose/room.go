package compose

import (
	"slices"
	"sync"
	"time"
)

// videoStale is how long a participant's last frame stays on their tile
// after their video stops (the bridge left them out); then their tile
// shows the name card.
const videoStale = 2 * time.Second

// Room holds what the grid and the mix are built from: the participants,
// the latest video frame of each and their audio. It is safe for
// concurrent use: decoders push, the clocks tick.
type Room struct {
	mu    sync.Mutex
	w, h  int
	limit int
	parts map[string]*member
	order int
	mixer *Mixer
	grid  *Frame
}

type member struct {
	tile  Tile
	frame *Frame
	at    time.Time // When frame arrived
}

// NewRoom returns an empty room drawing a w×h grid of at most limit tiles.
func NewRoom(w, h, limit int) *Room {
	return &Room{w: w, h: h, limit: limit, parts: map[string]*member{}, mixer: NewMixer(), grid: NewFrame(w, h)}
}

// SetLimit changes the number of tiles.
func (r *Room) SetLimit(n int) {
	r.mu.Lock()
	r.limit = n
	r.mu.Unlock()
}

// SetParticipant adds a participant or updates their name, pin and last
// time they spoke.
func (r *Room) SetParticipant(id, name string, pinned bool, lastSpoke time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	m := r.parts[id]
	if m == nil {
		r.order++
		m = &member{tile: Tile{ID: id, Order: r.order}}
		r.parts[id] = m
	}
	m.tile.Name, m.tile.Pinned, m.tile.LastSpoke = name, pinned, lastSpoke
}

// Remove forgets a participant, their video and their audio.
func (r *Room) Remove(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.parts, id)
	r.mixer.Remove(id)
}

// PushVideo makes f, which the room keeps, the participant's latest frame.
func (r *Room) PushVideo(id string, f *Frame, now time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if m := r.parts[id]; m != nil {
		m.frame, m.at = f, now
	}
}

// PushAudio adds decoded samples (48 kHz stereo interleaved) of a
// participant.
func (r *Room) PushAudio(id string, s []int16) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.mixer.Push(id, s)
}

// Tiles returns the grid's tiles at now, in order: at most limit, those
// with video first, then name cards (FR-015).
func (r *Room) Tiles(now time.Time) []Tile {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.tilesLocked(now)
}

func (r *Room) tilesLocked(now time.Time) []Tile {
	all := make([]Tile, 0, len(r.parts))
	for _, m := range r.parts {
		t := m.tile
		if m.frame != nil && now.Sub(m.at) <= videoStale {
			t.Frame = m.frame
		}
		all = append(all, t)
	}
	SortTiles(all)
	if len(all) <= r.limit {
		return all
	}
	// Video first, keeping the grid order within each group.
	slices.SortStableFunc(all, func(a, b Tile) int {
		switch {
		case a.Frame != nil && b.Frame == nil:
			return -1
		case a.Frame == nil && b.Frame != nil:
			return 1
		}
		return 0
	})
	shown := all[:r.limit]
	SortTiles(shown)
	return shown
}

// VideoTick draws the grid at now. The frame is the room's own and valid
// until the next tick.
func (r *Room) VideoTick(now time.Time) *Frame {
	r.mu.Lock()
	defer r.mu.Unlock()
	DrawGrid(r.grid, r.tilesLocked(now))
	return r.grid
}

// AudioTick takes the next 20 ms of every participant into mix
// (ChunkSamples); each, when set, gets each participant's chunk.
func (r *Room) AudioTick(mix []int16, each func(id string, chunk []int16)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.mixer.Tick(mix, each)
}
