package output

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPorts_AssignStableAndCapped(t *testing.T) {
	p := NewPorts("127.0.0.1", 9000, 2, "")
	if p.Grid() != 9000 {
		t.Errorf("grid %d", p.Grid())
	}
	a, _ := p.Assign("Alice")
	b, _ := p.Assign("Bob")
	if a != 9001 || b != 9002 {
		t.Errorf("ports %d %d", a, b)
	}
	if again, _ := p.Assign("alice"); again != 9001 {
		t.Errorf("Alice again %d, want 9001", again)
	}
	if _, ok := p.Assign("Carol"); ok {
		t.Error("third feed beyond MaxFeeds 2")
	}
	if u := p.URL(9001); u != "srt://127.0.0.1:9001" {
		t.Errorf("URL %s", u)
	}
	if l := p.ListenURL(9001); !strings.HasPrefix(l, "srt://127.0.0.1:9001?mode=listener") {
		t.Errorf("listen URL %s", l)
	}
	feeds := p.Feeds(true)
	if feeds["grid"] != "srt://127.0.0.1:9000" || feeds["Alice"] != "srt://127.0.0.1:9001" || len(feeds) != 3 {
		t.Errorf("feeds %v", feeds)
	}
	dir := t.TempDir()
	if err := WriteFeeds(dir, feeds); err != nil {
		t.Fatal(err)
	}
	b2, _ := os.ReadFile(filepath.Join(dir, "feeds.json"))
	var back map[string]string
	if err := json.Unmarshal(b2, &back); err != nil || back["Bob"] != "srt://127.0.0.1:9002" {
		t.Errorf("feeds.json %s (%v)", b2, err)
	}
}

func TestPorts_AnyInterfaceUsesAnAddress(t *testing.T) {
	p := NewPorts("0.0.0.0", 9000, 1, "")
	if u := p.URL(9000); strings.Contains(u, "0.0.0.0") {
		t.Errorf("URL %s should name a reachable address", u)
	}
}

// In a container the feeds listen on its own address, but OBS reaches them
// at the host's.
func TestPorts_URLHostOverrides(t *testing.T) {
	p := NewPorts("0.0.0.0", 9000, 1, "192.168.1.20")
	if u := p.URL(9000); u != "srt://192.168.1.20:9000" {
		t.Errorf("URL %s, want srt://192.168.1.20:9000", u)
	}
	if u := p.ListenURL(9000); !strings.HasPrefix(u, "srt://0.0.0.0:9000?") {
		t.Errorf("ListenURL %s should still listen on 0.0.0.0", u)
	}
}
