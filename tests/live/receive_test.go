//go:build live

package live

import (
	"context"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/daimoc/jitsi-media-tools/internal/compose"
	"github.com/daimoc/jitsi-media-tools/internal/session"
	"github.com/daimoc/jitsi-media-tools/jmt"
)

// gridWatch records whether the viewer's grid shows the publisher's tile
// with video, and whether its mix is audible.
type gridWatch struct {
	mu             sync.Mutex
	video, audible bool
}

func (w *gridWatch) grid(f *compose.Frame, _ time.Time) {
	r := compose.Layout(1, f.W, f.H)[0]
	v := f.Y[(r.Y+r.H/3)*f.W+r.X+r.W/2]
	if v != 16 && v != 40 { // Neither black nor a name card
		w.mu.Lock()
		w.video = true
		w.mu.Unlock()
	}
}

func (w *gridWatch) mix(s []int16, _ time.Time) {
	for _, v := range s {
		if v > 1000 || v < -1000 {
			w.mu.Lock()
			w.audible = true
			w.mu.Unlock()
			return
		}
	}
}

func (w *gridWatch) ok() (bool, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.video, w.audible
}

// TestLive_ReceiveGrid checks SC-002: a participant who joins appears in
// the viewer's grid with video, and their audio is in the mix, within 5 s.
func TestLive_ReceiveGrid(t *testing.T) {
	meetingURL, insecure := liveURL(t)
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	clip := filepath.Join(t.TempDir(), "clip.webm")
	if out, err := exec.Command(ff, "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc2=size=640x360:rate=30", "-f", "lavfi", "-i", "sine",
		"-t", "3", "-c:v", "libvpx", "-c:a", "libopus", clip).CombinedOutput(); err != nil {
		t.Fatalf("making clip: %v: %s", err, out)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	var w gridWatch
	viewer, err := session.Start(ctx, session.Config{
		MeetingURL: meetingURL, DisplayName: "Viewer", Insecure: insecure, Logger: logger(),
		OnGrid: w.grid, OnMix: w.mix,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer viewer.Close()

	start := time.Now()
	pub, err := jmt.Join(ctx, jmt.Options{
		MeetingURL: meetingURL, DisplayName: "Publisher", Insecure: insecure, Logger: logger(),
		Publish: jmt.WithSize(jmt.FileSource(clip, true), 640, 360),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer pub.Close()

	for time.Since(start) < 5*time.Second {
		video, audible := w.ok()
		if video && audible {
			var named bool
			for _, p := range viewer.Participants() {
				named = named || p.Name == "Publisher" && p.Selected
			}
			if named {
				t.Logf("Publisher shown and heard after %v", time.Since(start).Round(100*time.Millisecond))
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	video, audible := w.ok()
	t.Fatalf("after 5 s: video %v, audible %v, participants %+v", video, audible, viewer.Participants())
}
