package jmt

import (
	"context"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pion/webrtc/v3"

	"github.com/daimoc/jitsi-media-tools/internal/ffmpeg/ffmpegtest"
	"github.com/daimoc/jitsi-media-tools/internal/session"
	"github.com/daimoc/jitsi-media-tools/internal/session/sessiontest"
)

// sent is what the fake bridge receives from the session: video frames
// (distinct RTP timestamps) and Opus packets that are not silence.
type sent struct {
	mu          sync.Mutex
	videoMime   string
	videoFrames int
	voice       int
}

func (c *sent) setup(bridge *webrtc.PeerConnection) {
	bridge.OnTrack(func(tr *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		var last uint32
		for {
			p, _, err := tr.ReadRTP()
			if err != nil {
				return
			}
			c.mu.Lock()
			if tr.Kind() == webrtc.RTPCodecTypeVideo {
				c.videoMime = tr.Codec().MimeType
				if p.Timestamp != last {
					c.videoFrames++
					last = p.Timestamp
				}
			} else if len(p.Payload) > 3 { // The silence placeholder is 3 bytes
				c.voice++
			}
			c.mu.Unlock()
		}
	})
}

func (c *sent) get() (mime string, frames, voice int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.videoMime, c.videoFrames, c.voice
}

func TestFrameSource_WrongSize(t *testing.T) {
	pub, _ := FrameSource(640, 360, true, true)
	defer pub.Close()
	err := pub.WriteVideo(VideoFrame{Width: 320, Height: 180, Y: make([]byte, 320*180), U: make([]byte, 320*180/4), V: make([]byte, 320*180/4)})
	if err == nil || !strings.Contains(err.Error(), "640x360") {
		t.Errorf("WriteVideo of 320x180 to a 640x360 source: %v, want an error naming 640x360", err)
	}
	if err := pub.WriteVideo(VideoFrame{Width: 640, Height: 360, Y: make([]byte, 10)}); err == nil {
		t.Error("WriteVideo with short planes: no error")
	}
	if err := pub.WriteAudio(AudioChunk{Samples: make([]int16, 7)}); err == nil {
		t.Error("WriteAudio of an odd sample count: no error")
	}
}

func TestFrameSource_PublishesWrittenFrames(t *testing.T) {
	ffmpegtest.Require(t)
	c := &sent{}
	f := &sessiontest.Signal{T: t, Setup: c.setup}
	t.Cleanup(session.UseTestServer(f, sessiontest.FetchMeeting))
	pub, src := FrameSource(640, 360, true, true)
	s, err := Join(context.Background(), Options{MeetingURL: "https://meet.test/Room", DisplayName: "Frames", Publish: src, Logger: quiet})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// 2 s of a moving bar and a 440 Hz tone, written in real time.
	const w, h = 640, 360
	frame := VideoFrame{Width: w, Height: h, Y: make([]byte, w*h), U: make([]byte, w*h/4), V: make([]byte, w*h/4)}
	chunk := AudioChunk{Samples: make([]int16, ChunkSamples)}
	start := time.Now()
	for i := 0; i < 100; i++ { // 100 chunks of 20 ms
		for j := 0; j < ChunkFrames; j++ {
			v := int16(8000 * math.Sin(2*math.Pi*440*float64(i*ChunkFrames+j)/SampleRate))
			chunk.Samples[2*j], chunk.Samples[2*j+1] = v, v
		}
		if err := pub.WriteAudio(chunk); err != nil {
			t.Fatal(err)
		}
		if i%5 < 3 { // 3 frames per 100 ms
			for k := range frame.Y {
				frame.Y[k] = byte(16 + (k%w+i*8)%220)
			}
			if err := pub.WriteVideo(frame); err != nil {
				t.Fatal(err)
			}
		}
		time.Sleep(time.Until(start.Add(time.Duration(i+1) * ChunkDuration)))
	}
	mime, frames, voice := c.get()
	if mime != webrtc.MimeTypeVP8 {
		t.Errorf("video sent as %q, want VP8", mime)
	}
	// Placeholders alone give 5 black frames a second and silence.
	if frames < 30 {
		t.Errorf("%d video frames in 2 s, want about 60", frames)
	}
	if voice < 50 {
		t.Errorf("%d non-silent audio packets in 2 s, want about 100", voice)
	}
	if err := pub.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	if err := pub.WriteVideo(frame); err == nil {
		t.Error("WriteVideo after Close: no error")
	}
}
