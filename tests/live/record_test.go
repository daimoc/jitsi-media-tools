//go:build live

package live

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/daimoc/jitsi-media-tools/jmt"
)

// TestLive_Record checks US6 against the local Jitsi: per participant, the
// publisher's file holds their video (not a name card) and audio; mixed,
// one file holds the grid. Both play once the sessions are closed.
func TestLive_Record(t *testing.T) {
	meetingURL, insecure := liveURL(t)
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	meetingURL += "-record"
	clip := filepath.Join(t.TempDir(), "clip.webm")
	if out, err := exec.Command(ff, "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc2=size=640x360:rate=30", "-f", "lavfi", "-i", "sine",
		"-t", "3", "-c:v", "libvpx", "-c:a", "libopus", clip).CombinedOutput(); err != nil {
		t.Fatalf("making clip: %v: %s", err, out)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	dir := t.TempDir()
	var recorders []*jmt.Session
	for i, mode := range []jmt.RecordMode{jmt.RecordPerParticipant, jmt.RecordMixed} {
		r, err := jmt.Join(ctx, jmt.Options{MeetingURL: meetingURL, DisplayName: []string{"Recorder", "Mixer"}[i], Insecure: insecure, Logger: logger(),
			Record: &jmt.RecordOptions{Dir: dir, Mode: mode}})
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		recorders = append(recorders, r)
	}
	pub, err := jmt.Join(ctx, jmt.Options{MeetingURL: meetingURL, DisplayName: "Publisher", Insecure: insecure, Logger: logger(),
		Publish: jmt.WithSize(jmt.FileSource(clip, true), 640, 360)})
	if err != nil {
		t.Fatal(err)
	}
	defer pub.Close()

	paths := map[string]string{} // Participant ("" for mixed) -> file
	for deadline := time.Now().Add(15 * time.Second); (paths["Publisher"] == "" || paths[""] == "") && time.Now().Before(deadline); {
		for _, r := range recorders {
			select {
			case e := <-r.Events():
				if e.Kind == jmt.RecordingStarted && (e.Participant.Name == "Publisher" || e.Participant.Name == "") {
					paths[e.Participant.Name] = e.Path
				}
			case <-time.After(50 * time.Millisecond):
			}
		}
	}
	if paths["Publisher"] == "" || paths[""] == "" {
		t.Fatalf("recordings %v, want the publisher's and the mixed one", paths)
	}
	time.Sleep(5 * time.Second)
	for _, r := range recorders {
		r.Close()
	}

	for who, path := range paths {
		out, err := exec.Command(ff, "-hide_banner", "-i", path, "-f", "null", "-").CombinedOutput()
		if err != nil || !strings.Contains(string(out), "Video: h264") || !strings.Contains(string(out), "Audio: aac") {
			t.Errorf("%s (%q): %v\n%s", path, who, err, out)
			continue
		}
		t.Logf("%s plays", filepath.Base(path))
	}
	// A frame from 3 s into the publisher's file, as grey levels: the test
	// pattern is bright and varied, a name card dark grey.
	out, err := exec.Command(ff, "-hide_banner", "-loglevel", "error", "-ss", "3", "-i", paths["Publisher"],
		"-frames:v", "1", "-f", "rawvideo", "-pix_fmt", "gray", "-").Output()
	if err != nil || len(out) != 640*360 {
		t.Fatalf("frame of %d bytes from the publisher's file: %v", len(out), err)
	}
	var sum int
	for _, v := range out {
		sum += int(v)
	}
	if mean := sum / len(out); mean < 70 {
		t.Errorf("mean brightness %d: a name card or black, not the publisher's video", mean)
	} else {
		t.Logf("mean brightness %d", mean)
	}
}
