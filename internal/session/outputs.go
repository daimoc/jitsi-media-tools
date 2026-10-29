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
	URLHost  string // Address in the feed URLs; default Host or the outbound one
	BasePort int    // The grid's port, participants after it; default 9000
	MaxFeeds int    // Participant feeds; default 16
	Grid     bool   // Also serve the grid
}

// RecordOptions records the meeting into Dir (data-model "Record"): one
// file per participant who sends media, or one of the grid and the mix.
type RecordOptions struct {
	Dir   string
	Mixed bool
}

// Tile stream sizes: a participant's, larger when pinned.
const (
	feedWidth, feedHeight             = 640, 360
	pinnedFeedWidth, pinnedFeedHeight = 1280, 720
)

// outputs serves the OBS feeds and writes the recordings: the grid's, and
// the tile stream of each participant whose media was received. A feed
// keeps its port, and a recording its file, for the participant's name
// for the whole session (FR-016, FR-017, research R11). Tile streams are
// drawn on the receiver's clocks: the latest frame or the name card, and
// the participant's audio or silence.
type outputs struct {
	r       *receiver
	obs     *OBSOptions       // Nil without feeds
	rec     *RecordOptions    // Nil without recording
	ports   *output.Ports     // Nil without feeds
	started time.Time         // In the recordings' names
	grid    *output.Feed      // Nil without the grid feed
	mixed   *output.Recording // Nil without the mixed recording

	syncMu sync.Mutex // One sync at a time: it opens outputs outside mu
	mu     sync.Mutex
	byName map[string]*tile  // Lower-cased name
	urls   map[string]string // Name ("" for the grid) -> URL
	sent   map[string]bool   // Participant ids whose audio came
	unseen bool              // Audio came from an id without a recording
}

// tile is one participant's outputs; id is "" while they are away.
type tile struct {
	name  string
	id    string
	feed  *output.Feed      // Nil without a feed
	rec   *output.Recording // Nil without a recording
	frame *compose.Frame
	heard bool // Audio this tick
}

func newOutputs(r *receiver, obs *OBSOptions, rec *RecordOptions) *outputs {
	o := &outputs{r: r, rec: rec, byName: map[string]*tile{}, urls: map[string]string{}, sent: map[string]bool{}}
	if obs != nil {
		c := *obs
		if c.BasePort == 0 {
			c.BasePort = 9000
		}
		if c.MaxFeeds == 0 {
			c.MaxFeeds = 16
		}
		o.obs, o.ports = &c, output.NewPorts(c.Host, c.BasePort, c.MaxFeeds, c.URLHost)
	}
	return o
}

// perParticipant reports whether each participant is recorded.
func (o *outputs) perParticipant() bool { return o.rec != nil && !o.rec.Mixed }

func (o *outputs) start() {
	s := o.r.m.s
	o.started = time.Now()
	if o.rec != nil && o.rec.Mixed {
		if r, ev := o.record(output.MixedName, Participant{}, gridWidth, gridHeight); r != nil {
			o.mixed = r
			s.emit(ev)
		}
	}
	if o.obs == nil || !o.obs.Grid {
		return
	}
	g, url := o.open(output.GridName, o.ports.Grid(), gridWidth, gridHeight)
	if g == nil {
		return
	}
	o.mu.Lock()
	o.grid = g
	o.urls[""] = url
	o.mu.Unlock()
	o.writeFeeds()
	s.emit(Event{Kind: EventFeedReady, FeedURL: url})
}

// open starts the feed served on port.
func (o *outputs) open(name string, port, w, h int) (*output.Feed, string) {
	s := o.r.m.s
	feed, err := output.StartFeed(s.ctx, output.FeedConfig{FFmpeg: o.r.m.tools.ffmpeg, Name: name,
		ListenURL: o.ports.ListenURL(port), Width: w, Height: h, Logger: s.logger})
	if err != nil {
		s.logger.Warn("OBS feed not started", slog.String("feed", name), slog.String("error", err.Error()))
		return nil, ""
	}
	url := o.ports.URL(port)
	s.logger.Info("OBS feed", slog.String("feed", name), slog.String("url", url))
	return feed, url
}

// record starts the recording of name (output.MixedName for the grid).
func (o *outputs) record(name string, p Participant, w, h int) (*output.Recording, Event) {
	s := o.r.m.s
	path := output.RecordPath(o.rec.Dir, s.room, o.started, name)
	r, err := output.StartRecording(s.ctx, output.RecordConfig{FFmpeg: o.r.m.tools.ffmpeg, Name: name,
		Path: path, Width: w, Height: h, Logger: s.logger})
	if err != nil {
		s.logger.Warn("Recording not started", slog.String("recording", name), slog.String("error", err.Error()))
		return nil, Event{}
	}
	s.logger.Info("Recording", slog.String("recording", name), slog.String("file", path))
	return r, Event{Kind: EventRecordingStarted, Participant: p, Path: path}
}

// writeFeeds keeps feeds.json in the record directory up to date.
func (o *outputs) writeFeeds() {
	if o.rec == nil || o.ports == nil {
		return
	}
	if err := output.WriteFeeds(o.rec.Dir, o.ports.Feeds(o.obs.Grid)); err != nil {
		o.r.m.s.logger.Warn("feeds.json not written", slog.String("error", err.Error()))
	}
}

// sync opens a feed for each participant whose video is received, and a
// recording for each whose video or audio is; and follows who holds each
// name.
func (o *outputs) sync(parts []Participant) {
	o.syncMu.Lock()
	defer o.syncMu.Unlock()
	present := map[string]string{} // Lower-cased name -> id
	var opened []Event
	feedsChanged := false
	for _, p := range parts {
		key := strings.ToLower(p.Name)
		present[key] = p.ID
		o.mu.Lock()
		t, sent := o.byName[key], o.sent[p.ID]
		o.mu.Unlock()
		wantFeed := o.obs != nil && p.Selected && (t == nil || t.feed == nil)
		wantRec := o.perParticipant() && (p.Selected || sent) && (t == nil || t.rec == nil)
		if !wantFeed && !wantRec {
			continue
		}
		if t == nil {
			w, h := feedWidth, feedHeight
			if p.Pinned {
				w, h = pinnedFeedWidth, pinnedFeedHeight
			}
			t = &tile{name: p.Name, frame: compose.NewFrame(w, h)}
		}
		var feed *output.Feed
		var url string
		if wantFeed {
			if port, ok := o.ports.Assign(p.Name); ok {
				feed, url = o.open(p.Name, port, t.frame.W, t.frame.H)
			} else {
				o.r.m.s.warnOnce("feeds", "More participants than OBS feeds; raise the feed cap to serve them all")
			}
		}
		var rec *output.Recording
		if wantRec {
			var ev Event
			if rec, ev = o.record(p.Name, p, t.frame.W, t.frame.H); rec != nil {
				opened = append(opened, ev)
			}
		}
		o.mu.Lock()
		if feed != nil {
			t.feed = feed
			o.urls[p.Name] = url
			feedsChanged = true
			opened = append(opened, Event{Kind: EventFeedReady, Participant: p, FeedURL: url})
		}
		if rec != nil {
			t.rec = rec
		}
		if t.feed != nil || t.rec != nil {
			o.byName[key] = t
		}
		o.mu.Unlock()
	}
	o.mu.Lock()
	for key, t := range o.byName {
		t.id = present[key]
	}
	o.mu.Unlock()
	if feedsChanged {
		o.writeFeeds()
	}
	o.r.m.s.emit(opened...)
}

// wantsGrid reports whether a grid output needs the grid drawn.
func (o *outputs) wantsGrid() bool { return o != nil && (o.grid != nil || o.mixed != nil) }

// gridVideo writes the grid frame of the tick at now.
func (o *outputs) gridVideo(g *compose.Frame, now time.Time) {
	b := g.Bytes(make([]byte, 0, compose.Size(g.W, g.H)))
	if o.grid != nil {
		o.grid.WriteVideo(b, now)
	}
	if o.mixed != nil {
		o.mixed.WriteVideo(b, now) // Outputs only read b
	}
}

// videoTick writes each participant's tile, after opening the recordings
// of participants first heard. It draws without holding mu: the room
// calls audio, which takes mu, with its own lock held.
func (o *outputs) videoTick(now time.Time) {
	type draw struct {
		id, name string
		feed     *output.Feed
		rec      *output.Recording
		frame    *compose.Frame // Only this clock draws on it
	}
	o.mu.Lock()
	unseen := o.unseen
	o.unseen = false
	o.mu.Unlock()
	if unseen {
		o.sync(o.r.m.s.reg.list())
	}
	o.mu.Lock()
	all := make([]draw, 0, len(o.byName))
	for _, t := range o.byName {
		all = append(all, draw{t.id, t.name, t.feed, t.rec, t.frame})
	}
	o.mu.Unlock()
	for _, d := range all {
		o.r.room.DrawTile(d.frame, d.id, d.name, now)
		b := d.frame.Bytes(make([]byte, 0, compose.Size(d.frame.W, d.frame.H)))
		if d.feed != nil {
			d.feed.WriteVideo(b, now)
		}
		if d.rec != nil {
			d.rec.WriteVideo(b, now)
		}
	}
}

// audio writes a participant's 20 ms chunk of the tick at now to their
// outputs.
func (o *outputs) audio(id string, chunk []int16, now time.Time) {
	if o == nil || id == "" {
		return
	}
	b := pcmBytes(chunk)
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.perParticipant() && !o.sent[id] {
		o.sent[id], o.unseen = true, true
	}
	for _, t := range o.byName {
		if t.id == id {
			t.write(b, now)
			t.heard = true
		}
	}
}

// write writes an audio chunk made at now to the tile's outputs.
func (t *tile) write(b []byte, now time.Time) {
	if t.feed != nil {
		t.feed.WriteAudio(b, now)
	}
	if t.rec != nil {
		t.rec.WriteAudio(b, now)
	}
}

// audioTick completes the 20 ms tick at now: silence for the tiles that
// got no audio, and the mix for the grid.
func (o *outputs) audioTick(mix []int16, now time.Time) {
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, t := range o.byName {
		if !t.heard {
			t.write(make([]byte, len(mix)*2), now)
		}
		t.heard = false
	}
	if o.grid == nil && o.mixed == nil {
		return
	}
	b := pcmBytes(mix)
	if o.grid != nil {
		o.grid.WriteAudio(b, now)
	}
	if o.mixed != nil {
		o.mixed.WriteAudio(b, now)
	}
}

// URLs returns name ("" for the grid) -> address of the feeds served.
func (o *outputs) URLs() map[string]string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return maps.Clone(o.urls)
}

// stop ends the feeds and finishes the recordings, all at once: each
// recording may take a moment to write its end.
func (o *outputs) stop() {
	var stops []func()
	o.mu.Lock()
	for _, t := range o.byName {
		if t.feed != nil {
			stops = append(stops, t.feed.Stop)
		}
		if t.rec != nil {
			stops = append(stops, t.rec.Stop)
		}
	}
	if o.grid != nil {
		stops = append(stops, o.grid.Stop)
	}
	if o.mixed != nil {
		stops = append(stops, o.mixed.Stop)
	}
	o.mu.Unlock()
	var wg sync.WaitGroup
	for _, stop := range stops {
		wg.Add(1)
		go func() {
			defer wg.Done()
			stop()
		}()
	}
	wg.Wait()
}

// FeedURLs returns the OBS feed addresses: unique name ("" for the grid) ->
// srt URL.
func (s *Session) FeedURLs() map[string]string {
	if s.media.recv == nil || s.media.recv.out == nil {
		return map[string]string{}
	}
	return s.media.recv.out.URLs()
}
