// Package jmt joins a Jitsi Meet room as one participant, publishes media
// into it and plays what the other participants send.
//
// A session publishes one audio and video stream from a media file, capture
// devices, an OBS stream, or frames the program writes (FrameSource). It
// receives the other participants' media as decoded frames and samples, and
// can also show them in a tile-grid window, record them, and serve them as
// SRT feeds for OBS Media Sources.
//
// Received media reaches the program through callbacks set on the
// Session: OnVideo (each selected participant's decoded video), OnGrid (the
// composed tile grid), OnAudio (each participant's audio) and OnMixedAudio
// (the room's mix). Video is yuv420p (VideoFrame) and audio 20 ms chunks of
// 48 kHz stereo int16 (AudioChunk). Each callback runs on its own goroutine:
// a slow one loses frames and delays nothing else. Buffers are valid during
// the call only; copy what you keep. A kind of media is decoded only while
// something consumes it.
//
// Participants come and go as Events (joined, left, changed), delivered in
// order and never dropped.
//
// Media processing runs in ffmpeg (and ffplay for the window), which must
// be installed. Received video is bounded: the Selection.Limit most recent
// speakers (default 9) at low resolution, plus pinned participants.
package jmt
