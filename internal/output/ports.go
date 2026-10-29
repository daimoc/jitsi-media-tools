// Package output runs what the session does with received media: the
// display window, recordings and SRT feeds for OBS (research R11). It knows
// nothing of signaling.
package output

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// GridName is the feed name of the grid in feeds.json.
const GridName = "grid"

// Ports assigns OBS feed ports: the grid on base, participants on base+1…
// in order of first selection, at most max of them; a name keeps its port
// for the session (FR-017).
type Ports struct {
	listenHost string
	urlHost    string
	base, max  int

	mu     sync.Mutex
	byName map[string]int // Lower-cased name -> port
	names  map[int]string // Port -> name as first seen
}

// NewPorts assigns ports on listenHost from base. URLs use urlHost, else
// listenHost, or this machine's outbound address when it listens on every
// interface.
func NewPorts(listenHost string, base, max int, urlHost string) *Ports {
	if urlHost == "" {
		urlHost = listenHost
	}
	if urlHost == "" || urlHost == "0.0.0.0" || urlHost == "::" {
		urlHost = outboundIP()
	}
	return &Ports{listenHost: listenHost, urlHost: urlHost, base: base, max: max, byName: map[string]int{}, names: map[int]string{}}
}

// outboundIP returns the address this machine reaches other hosts from
// (no packet is sent), or 127.0.0.1.
func outboundIP() string {
	c, err := net.Dial("udp", "192.0.2.1:9") // TEST-NET-1: routing lookup only
	if err != nil {
		return "127.0.0.1"
	}
	defer c.Close()
	return c.LocalAddr().(*net.UDPAddr).IP.String()
}

// Grid returns the grid's port.
func (p *Ports) Grid() int { return p.base }

// Assign returns the port of name, giving it the next free one the first
// time; false when every participant port is taken.
func (p *Ports) Assign(name string) (int, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	key := strings.ToLower(name)
	if port, ok := p.byName[key]; ok {
		return port, true
	}
	if len(p.byName) >= p.max {
		return 0, false
	}
	port := p.base + 1 + len(p.byName)
	p.byName[key] = port
	p.names[port] = name
	return port, true
}

// ListenURL is the SRT address ffmpeg listens on for port.
func (p *Ports) ListenURL(port int) string {
	host := p.listenHost
	if host == "" {
		host = "0.0.0.0"
	}
	return fmt.Sprintf("srt://%s?mode=listener&latency=120000&transtype=live", net.JoinHostPort(host, fmt.Sprint(port)))
}

// URL is the address to give OBS for port.
func (p *Ports) URL(port int) string {
	return "srt://" + net.JoinHostPort(p.urlHost, fmt.Sprint(port))
}

// Feeds returns name -> URL for every assigned feed, plus the grid when
// grid is set.
func (p *Ports) Feeds(grid bool) map[string]string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := map[string]string{}
	if grid {
		out[GridName] = p.URL(p.base)
	}
	for port, name := range p.names {
		out[name] = p.URL(port)
	}
	return out
}

// WriteFeeds writes the feeds as dir/feeds.json, sorted by name.
func WriteFeeds(dir string, feeds map[string]string) error {
	names := make([]string, 0, len(feeds))
	for n := range feeds {
		names = append(names, n)
	}
	sort.Strings(names)
	var b strings.Builder
	b.WriteString("{\n")
	for i, n := range names {
		k, _ := json.Marshal(n)
		v, _ := json.Marshal(feeds[n])
		fmt.Fprintf(&b, "  %s: %s", k, v)
		if i < len(names)-1 {
			b.WriteString(",")
		}
		b.WriteString("\n")
	}
	b.WriteString("}\n")
	return os.WriteFile(filepath.Join(dir, "feeds.json"), []byte(b.String()), 0o644)
}
