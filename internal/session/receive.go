package session

import (
	"encoding/binary"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/pion/rtp"

	"github.com/daimoc/jitsi-media-tools/internal/compose"
	"github.com/daimoc/jitsi-media-tools/internal/ffmpeg"
	"github.com/daimoc/jitsi-media-tools/internal/output"
	"github.com/daimoc/jitsi-media-tools/internal/peer"
	"github.com/daimoc/jitsi-media-tools/internal/rtpmedia"
)

// receiveNeeds are the ffmpeg parts receiving uses; VP9 and H.264 are
// optional (a participant sending them gets a name card, with a warning).
var receiveNeeds = []string{"decoder:vp8", "decoder:opus"}

// Grid size and receive timing.
const (
	gridWidth, gridHeight = 1280, 720
	// pipeIdle is how long a track goes without packets before its decoder
	// stops (muted, or left out by the bridge): idle tracks cost nothing.
	pipeIdle = 10 * time.Second
	// restartDelay is how long a track waits before replacing a decoder
	// that failed.
	restartDelay = time.Second
)

// DisplayOptions shows the room in a window (needs ffplay).
type DisplayOptions struct{ Title string }

// receives reports whether the session can decode what it receives. Even
// then, a kind is decoded only while something consumes it.
func (c *Config) receives() bool {
	return c.Receive || c.Display != nil || c.OnGrid != nil || c.OnMix != nil || c.OBS != nil || c.Record != nil
}

// receiver decodes the received tracks into the room and drives the
// outputs from its clocks (research R7, R10).
type receiver struct {
	m       *media
	room    *compose.Room
	display *output.Display
	out     *outputs // Nil without OBS feeds or recording
	sinks   sinks

	mu    sync.Mutex
	known map[string]bool // Tile ids in the room: participants and screen shares
}

// screenTile is the tile id of a participant's screen share.
func screenTile(id string) string { return id + "#screen" }

func newReceiver(m *media) *receiver {
	r := &receiver{m: m, room: compose.NewRoom(gridWidth, gridHeight, m.s.cfg.Selection.Limit), known: map[string]bool{}}
	if c := m.s.cfg; c.OBS != nil || c.Record != nil {
		r.out = newOutputs(r, c.OBS, c.Record)
	}
	r.sinks.ctx = m.s.ctx
	if f := m.s.cfg.OnGrid; f != nil {
		setVideoSink(r.sinks.ctx, &r.sinks.grid, func(_ string, g *compose.Frame, at time.Time) { f(g, at) })
	}
	if f := m.s.cfg.OnMix; f != nil {
		setAudioSink(r.sinks.ctx, &r.sinks.mix, func(_ string, s []int16, at time.Time) { f(s, at) })
	}
	return r
}

// wants reports whether something consumes received media of a kind.
func (r *receiver) wants(kind string) bool {
	if r.m.s.cfg.Display != nil || r.out != nil { // Set before any track; the outputs come later
		return true
	}
	if kind == peer.KindVideo {
		return r.sinks.video.Load() != nil || r.sinks.grid.Load() != nil
	}
	return r.sinks.audio.Load() != nil || r.sinks.mix.Load() != nil
}

// start opens the outputs and starts the clocks, once joined.
func (r *receiver) start() error {
	s := r.m.s
	if d := s.cfg.Display; d != nil {
		title := d.Title
		if title == "" {
			title = s.room
		}
		disp, err := output.StartDisplay(s.ctx, output.DisplayConfig{
			FFplay: r.m.tools.ffplay, Title: title,
			Width: gridWidth, Height: gridHeight, Logger: s.logger,
		})
		if err != nil {
			return fmt.Errorf("display: %w", err)
		}
		r.display = disp
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			select {
			case <-disp.Done():
			case <-s.ctx.Done():
				return
			}
			// The window was closed: the user is done with the session.
			s.logger.Info("Display closed; leaving", slog.Any("reason", disp.Err()))
			go s.Close()
		}()
	}
	if r.out != nil {
		r.out.start()
	}
	r.sync()
	s.wg.Add(2)
	go func() {
		defer s.wg.Done()
		compose.Clock(s.ctx, compose.VideoInterval, r.videoTick)
	}()
	go func() {
		defer s.wg.Done()
		mix := make([]int16, compose.ChunkSamples)
		compose.Clock(s.ctx, compose.AudioInterval, func(now time.Time) { r.audioTick(mix, now) })
	}()
	return nil
}

func (r *receiver) videoTick(now time.Time) {
	if r.out != nil {
		r.out.videoTick(now)
	}
	if r.display == nil && r.sinks.grid.Load() == nil && !r.out.wantsGrid() {
		return // Nobody looks at the grid: do not draw it
	}
	g := r.room.VideoTick(now)
	if r.out.wantsGrid() {
		r.out.gridVideo(g)
	}
	if r.display != nil {
		r.display.WriteVideo(g.Bytes(make([]byte, 0, compose.Size(g.W, g.H))))
	}
	deliverVideo(&r.sinks.grid, "", g, now)
}

func (r *receiver) audioTick(mix []int16, now time.Time) {
	var each func(string, []int16)
	if r.sinks.audio.Load() != nil || r.out != nil {
		each = func(id string, chunk []int16) {
			deliverAudio(&r.sinks.audio, id, chunk, now)
			r.out.audio(id, chunk)
		}
	}
	r.room.AudioTick(mix, each)
	if r.out != nil {
		r.out.audioTick(mix)
	}
	if r.display != nil {
		r.display.WriteAudio(pcmBytes(mix))
	}
	deliverAudio(&r.sinks.mix, "", mix, now)
}

// sync makes the room's tiles follow the registry: one per participant,
// and one per screen share.
func (r *receiver) sync() {
	parts, screens := r.m.s.reg.list(), r.m.s.reg.screens()
	r.mu.Lock()
	defer r.mu.Unlock()
	now := map[string]bool{}
	for _, p := range parts {
		now[p.ID] = true
		r.room.SetParticipant(p.ID, p.Name, p.Pinned, p.LastSpoke)
		if screens[p.ID] {
			id := screenTile(p.ID)
			now[id] = true
			r.room.SetParticipant(id, p.Name+" (screen)", p.Pinned, p.LastSpoke)
		}
	}
	for id := range r.known {
		if !now[id] {
			r.room.Remove(id)
		}
	}
	r.known = now
	if r.out != nil {
		r.out.sync(parts)
	}
}

func (r *receiver) setLimit(n int) { r.room.SetLimit(n) }

func (r *receiver) stop() {
	r.sinks.stop()
	if r.out != nil {
		r.out.stop()
	}
	if r.display != nil {
		r.display.Stop()
	}
}

// track decodes one received track until it ends.
func (r *receiver) track(t *peer.RemoteTrack) {
	p := &pipe{r: r, t: t}
	stopWatch := make(chan struct{})
	defer close(stopWatch)
	go p.watch(stopWatch)
	defer p.stopDecoder()
	for {
		pkt, err := t.ReadRTP()
		if err != nil {
			r.m.s.logger.Debug("Track ended", slog.String("kind", t.Kind), slog.Uint64("ssrc", uint64(t.SSRC)), slog.String("error", err.Error()))
			return
		}
		p.push(pkt)
		if t.Kind == peer.KindVideo && p.owner != "" {
			r.m.s.meter.add(p.owner, pkt.MarshalSize())
		}
	}
}

// drain reads a track that is not decoded until it ends.
func drain(t *peer.RemoteTrack) {
	for {
		if _, err := t.ReadRTP(); err != nil {
			return
		}
	}
}

// pipe is one received track's decoding: RTP → receiver → ffmpeg decoder →
// the room. The decoder exists while packets come.
type pipe struct {
	r     *receiver
	t     *peer.RemoteTrack
	owner string
	tile  string // Room tile the video goes to: the owner's, or their screen share's
	// The codec being decoded: a sender may switch codec (a browser sends
	// AV1 until it reads our codec list), and the decoder follows.
	mime   string
	format string // "" when the codec cannot be decoded

	mu     sync.Mutex
	dec    *ffmpeg.Decoder
	rx     *rtpmedia.Receiver
	last   time.Time // Last packet
	failed time.Time // Last decoder failure
}

// push hands a packet to the decoder, starting one when needed.
func (p *pipe) push(pkt *rtp.Packet) {
	s := p.r.m.s
	if p.owner == "" {
		src, ok := s.reg.source(p.t.SSRC)
		if !ok || src.Owner == "" {
			return // Not announced yet: nobody to show it for
		}
		p.owner, p.tile = src.Owner, src.Owner
		if src.Desktop {
			p.tile = screenTile(src.Owner)
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	p.last = now
	if mime := p.t.Codec(); mime != p.mime {
		s.logger.Debug("Track codec", slog.String("owner", p.owner), slog.String("codec", mime))
		p.stopLocked()
		p.mime = mime
		var err error
		if p.format, err = rtpmedia.Format(mime); err != nil {
			s.warnOnce("codec:"+mime, "Cannot decode "+mime+"; the sender's tile shows a name card until they switch codec")
		}
	}
	if p.format == "" {
		return
	}
	if !p.r.wants(p.t.Kind) {
		p.stopLocked() // Nothing consumes it (any more)
		return
	}
	if p.dec != nil && p.t.Kind == peer.KindVideo && p.dec.Height() != p.height() {
		p.stopLocked() // Pinned or unpinned, or new heights: restart at the new height
	}
	if p.dec == nil {
		if now.Sub(p.failed) < restartDelay || !p.startLocked() {
			return
		}
	}
	if err := p.rx.Push(pkt); err != nil {
		s.logger.Debug("Decoder input failed", slog.String("kind", p.t.Kind), slog.String("error", err.Error()))
		p.failed = now
		p.stopLocked()
	}
}

// startLocked starts a decoder for the track.
func (p *pipe) startLocked() bool {
	s := p.r.m.s
	cfg := ffmpeg.DecoderConfig{Bin: p.r.m.tools.ffmpeg, Format: p.format, Logger: s.logger}
	var (
		dec *ffmpeg.Decoder
		err error
	)
	if p.t.Kind == peer.KindVideo {
		cfg.Height = p.height()
		dec, err = ffmpeg.StartVideoDecoder(s.ctx, cfg)
	} else {
		dec, err = ffmpeg.StartAudioDecoder(s.ctx, cfg)
	}
	if err == nil {
		p.rx, err = rtpmedia.NewReceiver(p.mime, dec)
	}
	if err != nil {
		s.warnOnce("decoder:"+p.mime, "Cannot start a decoder for "+p.mime+": "+err.Error())
		if dec != nil {
			dec.Stop()
		}
		p.failed = time.Now()
		return false
	}
	ssrc := p.t.SSRC
	p.rx.NeedKeyframe = func() { s.peer.RequestKeyframe(ssrc) }
	p.dec = dec
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		p.consume(dec)
	}()
	return true
}

// height is the decoded height of the owner's video: the pinned height
// for a pinned participant, else the selection's height.
func (p *pipe) height() int {
	s := p.r.m.s
	s.mu.Lock()
	sel := s.cfg.Selection
	s.mu.Unlock()
	if part := s.reg.get(p.owner); part != nil && part.Pinned {
		return sel.PinHeight
	}
	return sel.Height
}

// consume moves decoded frames into the room until the decoder ends.
func (p *pipe) consume(dec *ffmpeg.Decoder) {
	s := p.r.m.s
	if p.t.Kind == peer.KindVideo {
		first := true
		for buf := range dec.Frames() {
			f, now := compose.FrameFrom(dec.Width(), dec.Height(), buf), time.Now()
			p.r.room.PushVideo(p.tile, f, now)
			if p.tile != p.owner {
				continue // A screen share: its own tile only
			}
			deliverVideo(&p.r.sinks.video, p.owner, f, now)
			if first {
				first = false
				p.selected(true)
			}
		}
		if p.tile == p.owner {
			p.selected(false)
		}
	} else {
		for buf := range dec.Frames() {
			p.r.room.PushAudio(p.owner, pcmSamples(buf))
		}
	}
	<-dec.Done()
	if err := dec.Err(); err != nil && s.ctx.Err() == nil {
		s.logger.Debug("Decoder exited", slog.String("kind", p.t.Kind), slog.String("error", err.Error()))
	}
}

// selected records whether the owner's video is being received.
func (p *pipe) selected(on bool) {
	if e, ok := p.r.m.s.reg.setSelected(p.owner, on); ok {
		p.r.m.s.emit(e)
		if on && p.r.out != nil {
			p.r.out.sync(p.r.m.s.reg.list())
		}
	}
}

// watch stops the decoder of a track that went idle.
func (p *pipe) watch(stop <-chan struct{}) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			p.mu.Lock()
			if p.dec != nil && time.Since(p.last) > pipeIdle {
				p.stopLocked()
			}
			p.mu.Unlock()
		}
	}
}

func (p *pipe) stopDecoder() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stopLocked()
}

func (p *pipe) stopLocked() {
	if p.dec != nil {
		p.dec.Stop()
		p.dec, p.rx = nil, nil
	}
}

// pcmSamples reads s16le samples.
func pcmSamples(b []byte) []int16 {
	s := make([]int16, len(b)/2)
	for i := range s {
		s[i] = int16(binary.LittleEndian.Uint16(b[2*i:]))
	}
	return s
}

// pcmBytes writes samples as s16le.
func pcmBytes(s []int16) []byte {
	b := make([]byte, 2*len(s))
	for i, v := range s {
		binary.LittleEndian.PutUint16(b[2*i:], uint16(v))
	}
	return b
}
