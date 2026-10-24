package compose

import "math"

// Audio format: 48 kHz stereo, 20 ms chunks.
const (
	SampleRate   = 48000
	Channels     = 2
	ChunkFrames  = 960
	ChunkSamples = ChunkFrames * Channels
)

// Jitter buffers.
const (
	jitterTarget = 3 * ChunkSamples // 60 ms: playback starts when this much is buffered
	jitterMax    = 6 * ChunkSamples // 120 ms: older samples are dropped beyond it
)

// Jitter smooths one participant's decoded audio for the mixer's fixed
// 20 ms tick: it starts playing once 60 ms are buffered, drops the oldest
// samples past 120 ms, and plays silence (and waits for 60 ms again) when it
// runs dry.
type Jitter struct {
	buf     []int16
	playing bool
}

// Push adds decoded samples (interleaved stereo).
func (j *Jitter) Push(s []int16) {
	j.buf = append(j.buf, s...)
	if over := len(j.buf) - jitterMax; over > 0 {
		over += over % Channels
		j.buf = append(j.buf[:0], j.buf[over:]...)
	}
	if len(j.buf) >= jitterTarget {
		j.playing = true
	}
}

// Pop fills out (ChunkSamples) with the next 20 ms, or silence; it reports
// whether it had audio.
func (j *Jitter) Pop(out []int16) bool {
	if !j.playing || len(j.buf) < len(out) {
		clear(out)
		j.playing = false
		return false
	}
	copy(out, j.buf)
	j.buf = append(j.buf[:0], j.buf[len(out):]...)
	return true
}

// Mixer mixes participants' audio on a 20 ms tick.
type Mixer struct {
	sources map[string]*Jitter
	chunk   []int16
	acc     []int32
}

// NewMixer returns an empty mixer.
func NewMixer() *Mixer {
	return &Mixer{sources: map[string]*Jitter{}, chunk: make([]int16, ChunkSamples), acc: make([]int32, ChunkSamples)}
}

// Push adds decoded samples of participant id.
func (m *Mixer) Push(id string, s []int16) {
	j := m.sources[id]
	if j == nil {
		j = &Jitter{}
		m.sources[id] = j
	}
	j.Push(s)
}

// Remove forgets a participant.
func (m *Mixer) Remove(id string) { delete(m.sources, id) }

// Tick takes 20 ms from every participant: each one's chunk goes to each
// (when it had audio), and the saturated sum is written to mix
// (ChunkSamples).
func (m *Mixer) Tick(mix []int16, each func(id string, chunk []int16)) {
	clear(m.acc)
	for id, j := range m.sources {
		if !j.Pop(m.chunk) {
			continue
		}
		if each != nil {
			each(id, m.chunk)
		}
		for i, v := range m.chunk {
			m.acc[i] += int32(v)
		}
	}
	for i, v := range m.acc {
		mix[i] = int16(max(math.MinInt16, min(math.MaxInt16, v)))
	}
}
