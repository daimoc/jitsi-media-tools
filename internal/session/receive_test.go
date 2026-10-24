package session

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/pion/webrtc/v3"
	pionmedia "github.com/pion/webrtc/v3/pkg/media"
	"github.com/pion/webrtc/v3/pkg/media/ivfreader"
	"github.com/pion/webrtc/v3/pkg/media/oggreader"

	"github.com/daimoc/jitsi-media-tools/internal/compose"
	"github.com/daimoc/jitsi-media-tools/internal/ffmpeg"
	"github.com/daimoc/jitsi-media-tools/internal/ffmpeg/ffmpegtest"
	"github.com/daimoc/jitsi-media-tools/internal/signaling"
)

// sendingBridge makes the fake bridge send a participant's VP8 and Opus,
// encoded by ffmpeg, once connected.
type sendingBridge struct {
	t          *testing.T
	f          *fakeSignal
	ivf, ogg   string
	owner      string
	stop       chan struct{}
	stopOnce   sync.Once
	sendErrors chan error
}

func newSendingBridge(t *testing.T, f *fakeSignal, owner string) *sendingBridge {
	bin := ffmpegtest.Require(t)
	dir := t.TempDir()
	b := &sendingBridge{t: t, f: f, owner: owner, ivf: filepath.Join(dir, "v.ivf"), ogg: filepath.Join(dir, "a.ogg"),
		stop: make(chan struct{}), sendErrors: make(chan error, 2)}
	run := func(args ...string) {
		if out, err := exec.Command(bin, append([]string{"-hide_banner", "-loglevel", "error"}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("ffmpeg: %v: %s", err, out)
		}
	}
	run("-f", "lavfi", "-i", "testsrc2=size=320x180:rate=30:duration=4", "-c:v", "libvpx", "-deadline", "realtime", "-g", "30", "-f", "ivf", b.ivf)
	run("-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration=4", "-c:a", "libopus", "-frame_duration", "20", "-page_duration", "20000", "-f", "ogg", b.ogg)
	t.Cleanup(func() { b.stopOnce.Do(func() { close(b.stop) }) })
	return b
}

func (b *sendingBridge) setup(bridge *webrtc.PeerConnection) {
	v, err := webrtc.NewTrackLocalStaticSample(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8}, "video", "alice")
	if err != nil {
		b.t.Fatal(err)
	}
	a, err := webrtc.NewTrackLocalStaticSample(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2}, "audio", "alice")
	if err != nil {
		b.t.Fatal(err)
	}
	vs, err := bridge.AddTrack(v)
	if err != nil {
		b.t.Fatal(err)
	}
	as, err := bridge.AddTrack(a)
	if err != nil {
		b.t.Fatal(err)
	}
	b.f.sources = []signaling.RemoteSource{
		{SSRC: uint32(vs.GetParameters().Encodings[0].SSRC), Kind: "video", Name: b.owner + "-v0", Owner: b.owner},
		{SSRC: uint32(as.GetParameters().Encodings[0].SSRC), Kind: "audio", Name: b.owner + "-a0", Owner: b.owner},
	}
	var once sync.Once
	bridge.OnConnectionStateChange(func(st webrtc.PeerConnectionState) {
		if st == webrtc.PeerConnectionStateConnected {
			once.Do(func() {
				go b.sendVideo(v)
				go b.sendAudio(a)
			})
		}
	})
}

func (b *sendingBridge) sendVideo(tr *webrtc.TrackLocalStaticSample) {
	f, err := os.Open(b.ivf)
	if err != nil {
		b.sendErrors <- err
		return
	}
	defer f.Close()
	r, _, err := ivfreader.NewWith(f)
	if err != nil {
		b.sendErrors <- err
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
			b.sendErrors <- err
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

func (b *sendingBridge) sendAudio(tr *webrtc.TrackLocalStaticSample) {
	f, err := os.Open(b.ogg)
	if err != nil {
		b.sendErrors <- err
		return
	}
	defer f.Close()
	r, _, err := oggreader.NewWith(f)
	if err != nil {
		b.sendErrors <- err
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
			b.sendErrors <- err
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

// watcher records what the grid and mix hooks see.
type watcher struct {
	mu       sync.Mutex
	video    bool // A grid with a video tile (neither black nor a card)
	audible  bool
	gridSeen int
}

func (w *watcher) grid(f *compose.Frame, _ time.Time) {
	r := compose.Layout(1, f.W, f.H)[0]
	v := f.Y[(r.Y+r.H/3)*f.W+r.X+r.W/2]
	w.mu.Lock()
	defer w.mu.Unlock()
	w.gridSeen++
	if v != 16 && v != 40 {
		w.video = true
	}
}

func (w *watcher) mix(s []int16, _ time.Time) {
	for _, v := range s {
		if v > 1000 || v < -1000 {
			w.mu.Lock()
			w.audible = true
			w.mu.Unlock()
			return
		}
	}
}

func (w *watcher) state() (video, audible bool, grids int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.video, w.audible, w.gridSeen
}

func TestReceive_GridShowsVideoAndMixIsAudible(t *testing.T) {
	f := &fakeSignal{occupants: []signaling.Occupant{{ID: "alice1", DisplayName: "Alice"}}}
	b := newSendingBridge(t, f, "alice1")
	f.setup = b.setup
	useFake(t, f)
	var w watcher
	var col collected
	s, err := Start(context.Background(), col.config(Config{
		MeetingURL: "https://meet.test/Room", DisplayName: "Viewer",
		OnGrid: w.grid, OnMix: w.mix,
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if video, audible, _ := w.state(); video && audible {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	video, audible, grids := w.state()
	if !video || !audible {
		select {
		case err := <-b.sendErrors:
			t.Fatalf("bridge: %v", err)
		default:
		}
		t.Fatalf("after 5 s: video %v, audible %v (%d grids)", video, audible, grids)
	}
	if p := s.Participants(); len(p) != 1 || !p[0].Selected {
		t.Errorf("participants %+v, want Alice selected", p)
	}
	before := ffmpeg.Running()
	if before == 0 {
		t.Error("no decoder running")
	}
	s.Close()
	if n := ffmpeg.Running(); n != 0 {
		t.Errorf("%d processes after Close", n)
	}
}

func TestReceive_NoConsumerStartsNoDecoder(t *testing.T) {
	f := &fakeSignal{occupants: []signaling.Occupant{{ID: "alice1", DisplayName: "Alice"}}}
	b := newSendingBridge(t, f, "alice1")
	f.setup = b.setup
	useFake(t, f)
	var col collected
	s, err := Start(context.Background(), col.config(Config{MeetingURL: "https://meet.test/Room", DisplayName: "Viewer"}))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	time.Sleep(1500 * time.Millisecond)
	if n := ffmpeg.Running(); n != 0 {
		t.Errorf("%d processes running with nothing consuming", n)
	}
}
