// Package jmt joins a Jitsi Meet room as one participant, publishes media
// into it and plays what the other participants send.
//
// A session publishes one audio and video stream from a media file, capture
// devices, an OBS stream, or frames the program writes (FrameSource). It
// receives the other participants' media as decoded frames and samples, and
// can also show them in a tile-grid window, record them, and serve them as
// SRT feeds for OBS Media Sources.
//
// Media processing runs in ffmpeg (and ffplay for the window), which must
// be installed. Received video is bounded: the Selection.Limit most recent
// speakers (default 9) at low resolution, plus pinned participants.
package jmt
