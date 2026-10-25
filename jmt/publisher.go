package jmt

import "github.com/daimoc/jitsi-media-tools/internal/session"

// Publisher writes a program's own media into the session it publishes in
// (FrameSource). Its methods are safe for concurrent use.
type Publisher struct{ f *session.Frames }

// FrameSource returns a source publishing what the program writes into the
// returned Publisher: video frames of width×height (rounded down to even)
// and audio chunks, written in real time (video at 30 fps). Frames written
// before the session joins, or faster than it encodes, are dropped oldest
// first; while nothing comes, the session sends black and silence.
func FrameSource(width, height int, audio, video bool) (*Publisher, Source) {
	f := session.NewFrames(width, height, audio, video)
	return &Publisher{f}, frameSource{f}
}

// WriteVideo publishes a frame; its size must be the source's. The frame
// is copied.
func (p *Publisher) WriteVideo(f VideoFrame) error {
	return p.f.WriteVideo(f.Width, f.Height, f.Y, f.U, f.V)
}

// WriteAudio publishes 48 kHz stereo interleaved samples, usually one
// 20 ms chunk (ChunkSamples). They are copied.
func (p *Publisher) WriteAudio(c AudioChunk) error { return p.f.WriteAudio(c.Samples) }

// Close stops publishing what the program writes; the session goes on with
// black and silence. Writes then fail.
func (p *Publisher) Close() error {
	p.f.Close()
	return nil
}

type frameSource struct{ f *session.Frames }

func (s frameSource) publish() *session.Publish {
	return &session.Publish{Input: session.FramesInput(s.f), Audio: s.f.Audio, Video: s.f.Video}
}
