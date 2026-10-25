//go:build live

package live

import (
	"context"
	"math"
	"os/exec"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/daimoc/jitsi-media-tools/internal/compose"
	"github.com/daimoc/jitsi-media-tools/jmt"
)

// flashWatch matches the viewer's rising edges (bright tile, loud mix) to
// the publisher's flashes, sent at known times.
type flashWatch struct {
	mu           sync.Mutex
	sent         []time.Time // Flash starts, in order
	visible      bool        // Publisher's tile shows video
	bright, loud bool        // Current state, for edge detection
	video, audio []time.Duration
	avOffset     []time.Duration // Audio edge minus video edge, same flash
	videoAt      map[int]time.Time
	audioAt      map[int]time.Time
}

// flash returns the index of the latest flash sent before at, or -1.
func (w *flashWatch) flash(at time.Time) int {
	i := sort.Search(len(w.sent), func(i int) bool { return w.sent[i].After(at) }) - 1
	return i
}

func (w *flashWatch) grid(f jmt.VideoFrame) {
	r := compose.Layout(1, f.Width, f.Height)[0]
	v := f.Y[(r.Y+r.H/2)*f.Width+r.X+r.W/2]
	w.mu.Lock()
	defer w.mu.Unlock()
	w.visible = w.visible || v > 80 // Gray or white: neither black (16) nor a name card (40)
	b := v > 200
	if b && !w.bright {
		if i := w.flash(f.Time); i >= 0 && w.videoAt[i].IsZero() {
			w.videoAt[i] = f.Time
			w.video = append(w.video, f.Time.Sub(w.sent[i]))
			if a := w.audioAt[i]; !a.IsZero() {
				w.avOffset = append(w.avOffset, a.Sub(f.Time))
			}
		}
	}
	w.bright = b
}

func (w *flashWatch) mix(c jmt.AudioChunk) {
	var peak int16
	for _, s := range c.Samples {
		if s > peak {
			peak = s
		}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	l := peak > 3000
	if l && !w.loud {
		if i := w.flash(c.Time); i >= 0 && w.audioAt[i].IsZero() {
			w.audioAt[i] = c.Time
			w.audio = append(w.audio, c.Time.Sub(w.sent[i]))
			if v := w.videoAt[i]; !v.IsZero() {
				w.avOffset = append(w.avOffset, c.Time.Sub(v))
			}
		}
	}
	w.loud = l
}

func stats(d []time.Duration) (median, max time.Duration) {
	if len(d) == 0 {
		return 0, 0
	}
	s := append([]time.Duration(nil), d...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	return s[len(s)/2], s[len(s)-1]
}

// TestLive_Latency measures how long a flash of white video and a burst of
// tone written into a publisher's FrameSource take to reach a viewer's grid
// and mix: encode, bridge, receive, decode and compose, not the screen.
// The plan aims for a display under about 1 s.
func TestLive_Latency(t *testing.T) {
	meetingURL, insecure := liveURL(t)
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	const (
		w, h     = 640, 360
		flashes  = 6
		period   = 2 * time.Second
		flashLen = 300 * time.Millisecond
	)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	fw := &flashWatch{videoAt: map[int]time.Time{}, audioAt: map[int]time.Time{}}
	viewer, err := jmt.Join(ctx, jmt.Options{
		MeetingURL: meetingURL, DisplayName: "Viewer", Insecure: insecure, Logger: logger(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer viewer.Close()
	viewer.OnGrid(fw.grid)
	viewer.OnMixedAudio(fw.mix)

	src, source := jmt.FrameSource(w, h, true, true)
	pub, err := jmt.Join(ctx, jmt.Options{
		MeetingURL: meetingURL, DisplayName: "Publisher", Insecure: insecure, Logger: logger(),
		Publish: source,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer pub.Close()

	// Gray and white frames, silence and a 1 kHz tone.
	gray := jmt.VideoFrame{Width: w, Height: h, Y: fill(w*h, 128), U: fill(w*h/4, 128), V: fill(w*h/4, 128)}
	white := gray
	white.Y = fill(w*h, 235)
	silence := make([]int16, jmt.ChunkSamples)
	tone := make([]int16, jmt.ChunkSamples)
	for i := 0; i < jmt.ChunkFrames; i++ { // 1 kHz: 20 whole periods per chunk
		s := int16(10000 * math.Sin(2*math.Pi*1000*float64(i)/jmt.SampleRate))
		tone[2*i], tone[2*i+1] = s, s
	}

	var flashOn sync.Mutex
	on := false
	isOn := func() bool { flashOn.Lock(); defer flashOn.Unlock(); return on }
	setOn := func(v bool) { flashOn.Lock(); on = v; flashOn.Unlock() }

	stop := make(chan struct{})
	var wg sync.WaitGroup
	defer func() { close(stop); wg.Wait() }()
	wg.Add(2)
	go func() { // Video, 30 fps
		defer wg.Done()
		tk := time.NewTicker(time.Second / 30)
		defer tk.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tk.C:
				f := gray
				if isOn() {
					f = white
				}
				_ = src.WriteVideo(f)
			}
		}
	}()
	go func() { // Audio, 20 ms chunks
		defer wg.Done()
		tk := time.NewTicker(jmt.ChunkDuration)
		defer tk.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tk.C:
				c := silence
				if isOn() {
					c = tone
				}
				_ = src.WriteAudio(jmt.AudioChunk{Samples: c})
			}
		}
	}()

	// Wait for the publisher's video in the viewer's grid.
	start := time.Now()
	for {
		fw.mu.Lock()
		v := fw.visible
		fw.mu.Unlock()
		if v {
			break
		}
		if time.Since(start) > 15*time.Second {
			t.Fatal("publisher's video not in the grid after 15 s")
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Logf("publisher visible after %v", time.Since(start).Round(10*time.Millisecond))
	time.Sleep(time.Second) // Let the streams settle

	for i := 0; i < flashes; i++ {
		fw.mu.Lock()
		fw.sent = append(fw.sent, time.Now())
		fw.mu.Unlock()
		setOn(true)
		time.Sleep(flashLen)
		setOn(false)
		time.Sleep(period - flashLen)
	}

	fw.mu.Lock()
	defer fw.mu.Unlock()
	vm, vx := stats(fw.video)
	am, ax := stats(fw.audio)
	om, ox := stats(fw.avOffset)
	t.Logf("video: %d/%d flashes, median %v, max %v, all %v", len(fw.video), flashes, vm, vx, fw.video)
	t.Logf("audio: %d/%d flashes, median %v, max %v, all %v", len(fw.audio), flashes, am, ax, fw.audio)
	t.Logf("audio - video: median %v, max %v, all %v", om, ox, fw.avOffset)
	if len(fw.video) < flashes/2 || len(fw.audio) < flashes/2 {
		t.Fatalf("too few flashes seen: video %d, audio %d of %d", len(fw.video), len(fw.audio), flashes)
	}
	if vm > time.Second || am > time.Second {
		t.Errorf("median latency over 1 s: video %v, audio %v", vm, am)
	}
}

func fill(n int, v byte) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = v
	}
	return b
}
