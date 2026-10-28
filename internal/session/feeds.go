package session

import (
	"log/slog"
	"maps"
	"strings"
	"sync"
	"time"

	"github.com/daimoc/jitsi-media-tools/internal/compose"
	"github.com/daimoc/jitsi-media-tools/internal/ffmpeg"
	"github.com/daimoc/jitsi-media-tools/internal/output"
)

// feedNeeds are the ffmpeg parts the OBS feeds use.
var feedNeeds = []string{ffmpeg.NeedSRT, ffmpeg.NeedH264Encoder, ffmpeg.NeedAACEncoder}

// OBSOptions serves SRT feeds for OBS Media Sources (data-model "OBSFeeds").
type OBSOptions struct {
	Host     string // Address to listen on; default every interface
	BasePort int    // The grid's port, participants after it; default 9000
	MaxFeeds int    // Participant feeds; default 16
	Grid     bool   // Also serve the grid
}

// Feed sizes: a participant's tile stream, larger when pinned.
const (
	feedWidth, feedHeight             = 640, 360
	pinnedFeedWidth, pinnedFeedHeight = 1280, 720
)

// feeds serves the grid and the tile stream of each participant whose
// video was received at least once, each on a port the participant's name
// keeps for the session (FR-017, research R11). Tile streams are drawn on
// the receiver's clocks: the latest frame or the name card, and the
// participant's audio or silence.
type feeds struct {
	r     *receiver
	o     OBSOptions
	ports *output.Ports
	grid  *output.Feed // Nil without the grid feed

	mu     sync.Mutex
	byName map[string]*tileFeed // Lower-cased name
	urls   map[string]string    // Name ("" for the grid) -> URL
}

// tileFeed is one participant's feed; id is "" while they are away.
type tileFeed struct {
	name  string
	id    string
	feed  *output.Feed
	frame *compose.Frame
	heard bool // Audio this tick
}

func newFeeds(r *receiver, o OBSOptions) *feeds {
	if o.BasePort == 0 {
		o.BasePort = 9000
	}
	if o.MaxFeeds == 0 {
		o.MaxFeeds = 16
	}
	return &feeds{r: r, o: o, ports: output.NewPorts(o.Host, o.BasePort, o.MaxFeeds),
		byName: map[string]*tileFeed{}, urls: map[string]string{}}
}

func (f *feeds) start() {
	if !f.o.Grid {
		return
	}
	g, url := f.open(output.GridName, f.ports.Grid(), gridWidth, gridHeight)
	if g == nil {
		return
	}
	f.mu.Lock()
	f.grid = g
	f.urls[""] = url
	f.mu.Unlock()
	f.r.m.s.emit(Event{Kind: EventFeedReady, FeedURL: url})
}

// open starts the feed served on port.
func (f *feeds) open(name string, port, w, h int) (*output.Feed, string) {
	s := f.r.m.s
	feed, err := output.StartFeed(s.ctx, output.FeedConfig{FFmpeg: f.r.m.tools.ffmpeg, Name: name,
		ListenURL: f.ports.ListenURL(port), Width: w, Height: h, Logger: s.logger})
	if err != nil {
		s.logger.Warn("OBS feed not started", slog.String("feed", name), slog.String("error", err.Error()))
		return nil, ""
	}
	url := f.ports.URL(port)
	s.logger.Info("OBS feed", slog.String("feed", name), slog.String("url", url))
	return feed, url
}

// sync opens a feed for each participant whose video is received, and
// follows who holds each feed's name.
func (f *feeds) sync(parts []Participant) {
	present := map[string]string{} // Lower-cased name -> id
	var opened []Event
	for _, p := range parts {
		key := strings.ToLower(p.Name)
		present[key] = p.ID
		f.mu.Lock()
		_, has := f.byName[key]
		f.mu.Unlock()
		if has || !p.Selected {
			continue
		}
		port, ok := f.ports.Assign(p.Name)
		if !ok {
			f.r.m.s.warnOnce("feeds", "More participants than OBS feeds; raise the feed cap to serve them all")
			continue
		}
		w, h := feedWidth, feedHeight
		if p.Pinned {
			w, h = pinnedFeedWidth, pinnedFeedHeight
		}
		feed, url := f.open(p.Name, port, w, h)
		if feed == nil {
			continue
		}
		f.mu.Lock()
		f.byName[key] = &tileFeed{name: p.Name, feed: feed, frame: compose.NewFrame(w, h)}
		f.urls[p.Name] = url
		f.mu.Unlock()
		opened = append(opened, Event{Kind: EventFeedReady, Participant: p, FeedURL: url})
	}
	f.mu.Lock()
	for key, t := range f.byName {
		t.id = present[key]
	}
	f.mu.Unlock()
	f.r.m.s.emit(opened...)
}

// wantsGrid reports whether the grid feed needs the grid drawn.
func (f *feeds) wantsGrid() bool { return f != nil && f.grid != nil }

// videoTick writes each participant's tile. It draws without holding mu:
// the room calls audio, which takes mu, with its own lock held.
func (f *feeds) videoTick(now time.Time) {
	type draw struct {
		id, name string
		feed     *output.Feed
		frame    *compose.Frame // Only this clock draws on it
	}
	f.mu.Lock()
	all := make([]draw, 0, len(f.byName))
	for _, t := range f.byName {
		all = append(all, draw{t.id, t.name, t.feed, t.frame})
	}
	f.mu.Unlock()
	for _, d := range all {
		f.r.room.DrawTile(d.frame, d.id, d.name, now)
		d.feed.WriteVideo(d.frame.Bytes(make([]byte, 0, compose.Size(d.frame.W, d.frame.H))))
	}
}

// audio writes a participant's 20 ms chunk to their feed.
func (f *feeds) audio(id string, chunk []int16) {
	if f == nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, t := range f.byName {
		if t.id == id && id != "" {
			t.feed.WriteAudio(pcmBytes(chunk))
			t.heard = true
		}
	}
}

// audioTick completes a 20 ms tick: silence for the feeds that got no
// audio, and the mix for the grid.
func (f *feeds) audioTick(mix []int16) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, t := range f.byName {
		if !t.heard {
			t.feed.WriteAudio(make([]byte, len(mix)*2))
		}
		t.heard = false
	}
	if f.grid != nil {
		f.grid.WriteAudio(pcmBytes(mix))
	}
}

// URLs returns name ("" for the grid) -> address of the feeds served.
func (f *feeds) URLs() map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return maps.Clone(f.urls)
}

func (f *feeds) stop() {
	f.mu.Lock()
	all := make([]*output.Feed, 0, len(f.byName)+1)
	for _, t := range f.byName {
		all = append(all, t.feed)
	}
	if f.grid != nil {
		all = append(all, f.grid)
	}
	f.mu.Unlock()
	for _, feed := range all {
		feed.Stop()
	}
}

// FeedURLs returns the OBS feed addresses: unique name ("" for the grid) ->
// srt URL.
func (s *Session) FeedURLs() map[string]string {
	if s.media.recv == nil || s.media.recv.feeds == nil {
		return map[string]string{}
	}
	return s.media.recv.feeds.URLs()
}
