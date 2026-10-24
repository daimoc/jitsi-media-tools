// Package rtpmedia moves encoded media between byte streams (IVF, Ogg,
// Annex B, as ffmpeg reads and writes them) and RTP. It does not decode or
// encode (constitution IV) and knows nothing of signaling.
package rtpmedia

import "time"

// Clock rates of the media the session sends and receives.
const (
	VideoClockRate = 90000
	OpusClockRate  = 48000
	OpusFrame      = 20 * time.Millisecond
	OpusSamples    = OpusClockRate * 20 / 1000 // RTP ticks per 20 ms packet
)

// Frame is one encoded video frame or audio packet.
type Frame struct {
	Data     []byte
	Duration time.Duration // Time until the next frame
	Samples  uint32        // Duration in RTP clock ticks
	Keyframe bool          // Video only
}

// isVP8Keyframe reports whether a VP8 frame is a keyframe (RFC 6386 9.1:
// bit 0 of the frame tag is 0 for keyframes).
func isVP8Keyframe(frame []byte) bool {
	return len(frame) > 0 && frame[0]&0x01 == 0
}
