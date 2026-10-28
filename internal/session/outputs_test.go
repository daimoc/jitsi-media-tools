package session

import (
	"context"
	"fmt"
	"math/rand"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/daimoc/jitsi-media-tools/internal/ffmpeg"
	"github.com/daimoc/jitsi-media-tools/internal/ffmpeg/ffmpegtest"
	"github.com/daimoc/jitsi-media-tools/internal/output"
	"github.com/daimoc/jitsi-media-tools/internal/session/sessiontest"
	"github.com/daimoc/jitsi-media-tools/internal/signaling"
)

// freePorts returns a base port whose n UDP ports on loopback are free now.
func freePorts(t *testing.T, n int) int {
	t.Helper()
	for try := 0; try < 50; try++ {
		base := 20000 + rand.Intn(30000)
		var held []net.PacketConn
		ok := true
		for i := 0; i < n && ok; i++ {
			c, err := net.ListenPacket("udp", fmt.Sprintf("127.0.0.1:%d", base+i))
			if err != nil {
				ok = false
				break
			}
			held = append(held, c)
		}
		for _, c := range held {
			c.Close()
		}
		if ok {
			return base
		}
	}
	t.Fatal("no free ports")
	return 0
}

// readFeed reads an SRT feed for a second as OBS would and returns
// ffmpeg's report of the streams.
func readFeed(t *testing.T, bin, url string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "-hide_banner", "-probesize", "500000", "-analyzeduration", "500000",
		"-i", url+"?mode=caller&latency=120000", "-t", "1", "-f", "null", "-").CombinedOutput()
	if err != nil {
		t.Fatalf("reading %s: %v\n%s", url, err, out)
	}
	return string(out)
}

func feedEvents(c *collected) map[string]string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := map[string]string{}
	for _, e := range c.events {
		if e.Kind == EventFeedReady {
			out[e.Participant.Name] = e.FeedURL
		}
	}
	return out
}

// TestFeeds_ParticipantAndGrid checks T050: a participant whose video is
// received gets an SRT feed of their tile, the grid gets one, and a
// participant who leaves and comes back keeps their address (FR-017).
func TestFeeds_ParticipantAndGrid(t *testing.T) {
	bin := ffmpegtest.Require(t)
	caps, err := ffmpeg.Probe(context.Background(), bin)
	if err != nil {
		t.Fatal(err)
	}
	if err := caps.Missing(ffmpeg.NeedSRT, ffmpeg.NeedH264Encoder, ffmpeg.NeedAACEncoder); err != nil {
		t.Skip(err)
	}
	f := &fakeSignal{Occupants: []signaling.Occupant{{ID: "alice1", DisplayName: "Alice"}}}
	useFake(t, f)
	sessiontest.NewSender(f, "alice1")
	base := freePorts(t, 5)
	var col collected
	s, err := Start(context.Background(), col.config(Config{
		MeetingURL: "https://meet.test/Room", DisplayName: "Feeds",
		OBS: &OBSOptions{Host: "127.0.0.1", BasePort: base, MaxFeeds: 4, Grid: true},
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	grid, alice := fmt.Sprintf("srt://127.0.0.1:%d", base), fmt.Sprintf("srt://127.0.0.1:%d", base+1)
	for deadline := time.Now().Add(5 * time.Second); len(feedEvents(&col)) < 2 && time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
	}
	if ev := feedEvents(&col); ev[""] != grid || ev["Alice"] != alice {
		t.Fatalf("feed events %v, want the grid on %s and Alice on %s", ev, grid, alice)
	}
	if urls := s.FeedURLs(); urls[""] != grid || urls["Alice"] != alice || len(urls) != 2 {
		t.Errorf("FeedURLs() = %v", urls)
	}

	report := readFeed(t, bin, alice)
	if !strings.Contains(report, "Video: h264") || !strings.Contains(report, "640x360") || !strings.Contains(report, "Audio: aac") {
		t.Fatalf("Alice's feed:\n%s", report)
	}
	if report := readFeed(t, bin, grid); !strings.Contains(report, "1280x720") {
		t.Fatalf("grid feed:\n%s", report)
	}

	// Alice leaves, then comes back with a new endpoint: same address,
	// which shows her name card in between.
	f.Room().OnOccupant(signaling.Occupant{ID: "alice1", Left: true})
	if report := readFeed(t, bin, alice); !strings.Contains(report, "640x360") {
		t.Fatalf("Alice's feed while she is away:\n%s", report)
	}
	f.Room().OnOccupant(signaling.Occupant{ID: "alice2", DisplayName: "Alice"})
	if urls := s.FeedURLs(); urls["Alice"] != alice || len(urls) != 2 {
		t.Errorf("after Alice came back: %v, want her address kept", urls)
	}

	s.Close()
	if n := ffmpeg.Running(); n != 0 {
		t.Errorf("%d processes after Close", n)
	}
}

func recordEvents(c *collected) map[string]string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := map[string]string{}
	for _, e := range c.events {
		if e.Kind == EventRecordingStarted {
			out[e.Participant.Name] = e.Path
		}
	}
	return out
}

// decodes reports ffmpeg's view of a recording, failing when it does not
// decode.
func decodes(t *testing.T, bin, path string) string {
	t.Helper()
	out, err := exec.Command(bin, "-hide_banner", "-i", path, "-f", "null", "-").CombinedOutput()
	if err != nil {
		t.Fatalf("decoding %s: %v\n%s", path, err, out)
	}
	return string(out)
}

// TestRecording_Modes checks T058-T059 in the session: per participant, a
// participant who sends media gets a file named after them, with feeds.json
// beside it when OBS is on; mixed, one file of the grid. Both play once the
// session is closed.
func TestRecording_Modes(t *testing.T) {
	bin := ffmpegtest.Require(t)
	caps, err := ffmpeg.Probe(context.Background(), bin)
	if err != nil {
		t.Fatal(err)
	}
	if err := caps.Missing(append(feedNeeds, output.RecordNeeds...)...); err != nil {
		t.Skip(err)
	}
	for _, mixed := range []bool{false, true} {
		dir := t.TempDir()
		f := &fakeSignal{Occupants: []signaling.Occupant{{ID: "alice1", DisplayName: "Alice"}}}
		useFake(t, f)
		sessiontest.NewSender(f, "alice1")
		cfg := Config{MeetingURL: "https://meet.test/Room", DisplayName: "Rec", Record: &RecordOptions{Dir: dir, Mixed: mixed}}
		if !mixed {
			cfg.OBS = &OBSOptions{Host: "127.0.0.1", BasePort: freePorts(t, 2), MaxFeeds: 1}
		}
		var col collected
		s, err := Start(context.Background(), col.config(cfg))
		if err != nil {
			t.Fatal(err)
		}
		want := "Alice"
		if mixed {
			want = ""
		}
		for deadline := time.Now().Add(5 * time.Second); recordEvents(&col)[want] == "" && time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		}
		path := recordEvents(&col)[want]
		if path == "" || len(recordEvents(&col)) != 1 {
			t.Fatalf("mixed %v: recording events %v, want one for %q", mixed, recordEvents(&col), want)
		}
		name := "Alice"
		if mixed {
			name = output.MixedName
		}
		if !strings.HasPrefix(filepath.Base(path), s.Room()+"-") || !strings.HasSuffix(path, "-"+name+".mkv") || filepath.Dir(path) != dir {
			t.Errorf("recording at %s", path)
		}
		time.Sleep(2 * time.Second)
		s.Close()
		if n := ffmpeg.Running(); n != 0 {
			t.Errorf("%d processes after Close", n)
		}
		report := decodes(t, bin, path)
		size := "640x360"
		if mixed {
			size = "1280x720"
		}
		if !strings.Contains(report, "Video: h264") || !strings.Contains(report, size) || !strings.Contains(report, "Audio: aac") {
			t.Errorf("mixed %v: %s:\n%s", mixed, path, report)
		}
		feeds, err := os.ReadFile(filepath.Join(dir, "feeds.json"))
		if mixed {
			if err == nil {
				t.Errorf("feeds.json written without OBS: %s", feeds)
			}
		} else if err != nil || !strings.Contains(string(feeds), `"Alice": "srt://127.0.0.1:`) {
			t.Errorf("feeds.json: %s, %v", feeds, err)
		}
	}
}

// A participant who sends only audio is recorded too, over their name card.
func TestRecording_AudioOnly(t *testing.T) {
	bin := ffmpegtest.Require(t)
	caps, err := ffmpeg.Probe(context.Background(), bin)
	if err != nil {
		t.Fatal(err)
	}
	if err := caps.Missing(output.RecordNeeds...); err != nil {
		t.Skip(err)
	}
	f := &fakeSignal{Occupants: []signaling.Occupant{{ID: "bob1", DisplayName: "Bob"}}}
	useFake(t, f)
	sessiontest.NewSender(f, "bob1").AudioOnly = true
	var col collected
	s, err := Start(context.Background(), col.config(Config{MeetingURL: "https://meet.test/Room", DisplayName: "Rec", Record: &RecordOptions{Dir: t.TempDir()}}))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for deadline := time.Now().Add(5 * time.Second); recordEvents(&col)["Bob"] == "" && time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
	}
	path := recordEvents(&col)["Bob"]
	if path == "" {
		t.Fatalf("no recording for an audio-only participant: %v", recordEvents(&col))
	}
	time.Sleep(time.Second)
	s.Close()
	if report := decodes(t, bin, path); !strings.Contains(report, "Video: h264") || !strings.Contains(report, "Audio: aac") {
		t.Errorf("%s:\n%s", path, report)
	}
}
