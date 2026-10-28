package sessiontest

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/pion/webrtc/v3"
	pionmedia "github.com/pion/webrtc/v3/pkg/media"
	"github.com/pion/webrtc/v3/pkg/media/ivfreader"
	"github.com/pion/webrtc/v3/pkg/media/oggreader"

	"github.com/daimoc/jitsi-media-tools/internal/ffmpeg/ffmpegtest"
	"github.com/daimoc/jitsi-media-tools/internal/signaling"
)

// Sender makes the fake bridge send one participant's VP8 (a 320x180 test
// pattern) and Opus (a 440 Hz tone), 4 s of each encoded by ffmpeg, once
// connected. It skips the test when ffmpeg is missing.
type Sender struct {
	f        *Signal
	owner    string
	ivf, ogg string
	stop     chan struct{}
	stopOnce sync.Once
	// Errors gets reading failures of the encoded media.
	Errors chan error
	// AudioOnly sends no video; set it before the session starts.
	AudioOnly bool
}

// NewSender encodes the media and makes f's bridge send it for owner.
func NewSender(f *Signal, owner string) *Sender {
	t := f.T
	bin := ffmpegtest.Require(t)
	dir := t.TempDir()
	b := &Sender{f: f, owner: owner, ivf: filepath.Join(dir, "v.ivf"), ogg: filepath.Join(dir, "a.ogg"),
		stop: make(chan struct{}), Errors: make(chan error, 2)}
	run := func(args ...string) {
		if out, err := exec.Command(bin, append([]string{"-hide_banner", "-loglevel", "error"}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("ffmpeg: %v: %s", err, out)
		}
	}
	run("-f", "lavfi", "-i", "testsrc2=size=320x180:rate=30:duration=4", "-c:v", "libvpx", "-deadline", "realtime", "-g", "30", "-f", "ivf", b.ivf)
	run("-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration=4", "-c:a", "libopus", "-frame_duration", "20", "-page_duration", "20000", "-f", "ogg", b.ogg)
	t.Cleanup(func() { b.stopOnce.Do(func() { close(b.stop) }) })
	f.Setup = b.setup
	return b
}

func (b *Sender) setup(bridge *webrtc.PeerConnection) {
	t := b.f.T
	a, err := webrtc.NewTrackLocalStaticSample(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2}, "audio", b.owner)
	if err != nil {
		t.Fatal(err)
	}
	as, err := bridge.AddTrack(a)
	if err != nil {
		t.Fatal(err)
	}
	b.f.Sources = []signaling.RemoteSource{
		{SSRC: uint32(as.GetParameters().Encodings[0].SSRC), Kind: "audio", Name: b.owner + "-a0", Owner: b.owner},
	}
	var v *webrtc.TrackLocalStaticSample
	if !b.AudioOnly {
		if v, err = webrtc.NewTrackLocalStaticSample(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8}, "video", b.owner); err != nil {
			t.Fatal(err)
		}
		vs, err := bridge.AddTrack(v)
		if err != nil {
			t.Fatal(err)
		}
		b.f.Sources = append(b.f.Sources, signaling.RemoteSource{SSRC: uint32(vs.GetParameters().Encodings[0].SSRC), Kind: "video", Name: b.owner + "-v0", Owner: b.owner})
	}
	var once sync.Once
	bridge.OnConnectionStateChange(func(st webrtc.PeerConnectionState) {
		if st == webrtc.PeerConnectionStateConnected {
			once.Do(func() {
				if v != nil {
					go b.sendVideo(v)
				}
				go b.sendAudio(a)
			})
		}
	})
}

func (b *Sender) sendVideo(tr *webrtc.TrackLocalStaticSample) {
	f, err := os.Open(b.ivf)
	if err != nil {
		b.Errors <- err
		return
	}
	defer f.Close()
	r, _, err := ivfreader.NewWith(f)
	if err != nil {
		b.Errors <- err
		return
	}
	tick := time.NewTicker(time.Second / 30)
	defer tick.Stop()
	for {
		frame, _, err := r.ParseNextFrame()
		if errors.Is(err, io.EOF) {
			return
		}
		if err != nil {
			b.Errors <- err
			return
		}
		select {
		case <-b.stop:
			return
		case <-tick.C:
		}
		if err := tr.WriteSample(pionmedia.Sample{Data: frame, Duration: time.Second / 30}); err != nil {
			return
		}
	}
}

func (b *Sender) sendAudio(tr *webrtc.TrackLocalStaticSample) {
	f, err := os.Open(b.ogg)
	if err != nil {
		b.Errors <- err
		return
	}
	defer f.Close()
	r, _, err := oggreader.NewWith(f)
	if err != nil {
		b.Errors <- err
		return
	}
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		page, _, err := r.ParseNextPage()
		if errors.Is(err, io.EOF) {
			return
		}
		if err != nil {
			b.Errors <- err
			return
		}
		if len(page) >= 8 && (string(page[:8]) == "OpusHead" || string(page[:8]) == "OpusTags") {
			continue
		}
		select {
		case <-b.stop:
			return
		case <-tick.C:
		}
		if err := tr.WriteSample(pionmedia.Sample{Data: page, Duration: 20 * time.Millisecond}); err != nil {
			return
		}
	}
}
