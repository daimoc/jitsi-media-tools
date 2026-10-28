package jmt

import "time"

// Participant is another person in the room.
type Participant struct {
	ID         string // Endpoint id, stable while they stay
	Name       string // Display name, made unique in the session
	AudioMuted bool
	VideoMuted bool
	Pinned     bool
	Selected   bool // Their video is received
}

// VideoFrame is a decoded video frame in yuv420p: Y is Width×Height bytes,
// U and V (Width/2)×(Height/2) each.
type VideoFrame struct {
	ParticipantID string // "" for the grid
	Width, Height int
	Y, U, V       []byte
	Time          time.Time
}

// AudioChunk is 20 ms of 48 kHz stereo audio: 960 frames, 1920 interleaved
// int16 samples.
type AudioChunk struct {
	ParticipantID string // "" for the room mix
	Samples       []int16
	Time          time.Time
}

// Audio format of AudioChunk.
const (
	SampleRate    = 48000
	Channels      = 2
	ChunkFrames   = 960
	ChunkSamples  = ChunkFrames * Channels
	ChunkDuration = 20 * time.Millisecond
)

// EventKind is what an Event reports.
type EventKind int

const (
	ParticipantJoined EventKind = iota
	ParticipantLeft
	ParticipantChanged
	WaitingForHost
	FeedReady        // An OBS feed address is available
	RecordingStarted // A recording's file is being written
)

func (k EventKind) String() string {
	switch k {
	case ParticipantJoined:
		return "joined"
	case ParticipantLeft:
		return "left"
	case ParticipantChanged:
		return "changed"
	case WaitingForHost:
		return "waiting for host"
	case FeedReady:
		return "feed ready"
	case RecordingStarted:
		return "recording started"
	}
	return "unknown"
}

// Event is something that happened in the session.
type Event struct {
	Kind        EventKind
	Participant Participant
	FeedURL     string // FeedReady only; Participant is empty for the grid feed
	Path        string // RecordingStarted only; Participant is empty for the mixed recording
}
