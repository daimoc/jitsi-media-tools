package jmt

import "github.com/daimoc/jitsi-media-tools/internal/session"

// Source is what the session publishes; build one with FileSource,
// DeviceSource or FrameSource.
type Source interface {
	publish() *session.Publish
}

type fileSource struct {
	path string
	loop bool
}

// FileSource publishes a media file's first audio and video streams, once
// (the session then ends) or looped.
func FileSource(path string, loop bool) Source { return fileSource{path, loop} }

func (f fileSource) publish() *session.Publish {
	return &session.Publish{Input: session.FileInput(f.path, f.loop), Audio: true, Video: true}
}

type deviceSource struct{ video, audio string }

// DeviceSource publishes a camera and a microphone, named as ListDevices
// names them (case does not matter); "" omits one. An unknown name fails
// Join before joining, listing the devices there are.
func DeviceSource(video, audio string) Source { return deviceSource{video, audio} }

func (d deviceSource) publish() *session.Publish {
	return &session.Publish{Input: session.DeviceInput(d.video, d.audio)}
}

type sizedSource struct {
	Source
	w, h int
}

// WithSize sets the size of a source's published video (default 1280x720);
// the picture is scaled to fit and padded.
func WithSize(src Source, width, height int) Source { return sizedSource{src, width, height} }

func (s sizedSource) publish() *session.Publish {
	p := s.Source.publish()
	p.Width, p.Height = s.w, s.h
	return p
}
