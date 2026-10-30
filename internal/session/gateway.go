package session

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/daimoc/jitsi-media-tools/internal/peer"
	"github.com/daimoc/jitsi-media-tools/internal/signaling"
	"github.com/daimoc/jitsi-media-tools/internal/whip"
)

// GatewayConfig describes a WHIP gateway (research R14).
type GatewayConfig struct {
	// Session is the template of every stream's session: Insecure,
	// WaitHost, Logger… Meeting, name and publish source are set per stream.
	Session     Config
	UDPMux      peer.UDPMux   // Nil: a port per stream
	PublicIPs   []string      // Given to publishers instead of local addresses
	IdleTimeout time.Duration // Without media from the publisher; 0 means 10 s
	// StatsInterval is how often each stream's statistics are measured and
	// sent to its meeting; 0 means 10 s, as Jitsi clients do.
	StatsInterval time.Duration
}

// Gateway joins each WHIP publisher to its meeting as a send-only
// participant forwarding its media. It implements whip.Publisher.
type Gateway struct {
	cfg GatewayConfig
}

var _ whip.Publisher = (*Gateway)(nil)

// NewGateway returns a gateway.
func NewGateway(cfg GatewayConfig) *Gateway {
	if cfg.Session.Logger == nil {
		cfg.Session.Logger = slog.Default()
	}
	if cfg.StatsInterval <= 0 {
		cfg.StatsInterval = 10 * time.Second
	}
	return &Gateway{cfg: cfg}
}

// Publish checks the offer, joins the meeting, then answers the publisher.
// ctx bounds the join.
func (g *Gateway) Publish(ctx context.Context, r whip.Request) (whip.Stream, error) {
	kinds, videoCodec, err := peer.CheckOffer(r.Offer)
	if err != nil {
		return whip.Stream{}, &whip.Error{Status: http.StatusBadRequest, Msg: strings.TrimPrefix(err.Error(), peer.ErrUnsupportedOffer.Error()+": ")}
	}
	meeting, _, _ := strings.Cut(r.MeetingURL, "?")
	logger := g.cfg.Session.Logger.With(slog.String("meeting", meeting), slog.String("name", r.Name))
	if videoCodec == peer.AV1Codec.MimeType {
		// The bridge forwards AV1 as it comes (research R14).
		logger.Info("Forwarding AV1 video: participants whose browser cannot decode AV1 see no video from this stream")
	}
	fw := &Forwarded{VideoCodec: videoCodec}
	cfg := g.cfg.Session
	cfg.MeetingURL, cfg.DisplayName, cfg.Logger, cfg.SendOnly = r.MeetingURL, r.Name, logger, true
	cfg.Publish = &Publish{Input: ForwardInput(fw), Audio: slices.Contains(kinds, peer.KindAudio), Video: slices.Contains(kinds, peer.KindVideo)}
	s, err := Start(ctx, cfg)
	if err != nil {
		return whip.Stream{}, err
	}
	ing, answer, err := peer.NewIngest(peer.IngestConfig{Logger: logger, UDPMux: g.cfg.UDPMux, PublicIPs: g.cfg.PublicIPs, IdleTimeout: g.cfg.IdleTimeout}, r.Offer)
	if err != nil {
		s.Close()
		if errors.Is(err, peer.ErrUnsupportedOffer) {
			return whip.Stream{}, &whip.Error{Status: http.StatusBadRequest, Msg: err.Error()}
		}
		return whip.Stream{}, err
	}
	fw.Attach(ing)
	stats := &streamStats{}
	go g.measure(s, ing, fw, stats, logger)

	done := make(chan struct{})
	var why error // Set before done closes
	go func() {
		select {
		case <-ing.Done():
			why = ing.Err()
		case <-s.Done():
			why = s.Err()
			if why == nil {
				why = errors.New("left the meeting")
			}
		}
		ing.Close()
		s.Close()
		close(done)
	}()
	var once sync.Once
	return whip.Stream{
		Answer: answer,
		End:    func() { once.Do(func() { ing.Close() }) },
		Done:   done,
		Err:    func() error { <-done; return why },
		Stats:  stats.get,
	}, nil
}

// streamStats holds a stream's latest statistics.
type streamStats struct {
	mu     sync.Mutex
	latest whip.StreamStats
}

func (s *streamStats) get() whip.StreamStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.latest
}

// measure updates a stream's statistics every StatsInterval and, once
// media flows to the meeting, sends them as the participant's own (FR-032,
// research R14), until the session ends.
func (g *Gateway) measure(s *Session, ing *peer.Ingest, fw *Forwarded, stats *streamStats, logger *slog.Logger) {
	tick := time.NewTicker(g.cfg.StatsInterval)
	defer tick.Stop()
	var w statsWindow
	w.next(ing.Stats(), 0, 0, time.Now())
	for {
		select {
		case <-s.Done():
			return
		case now := <-tick.C:
			st := w.next(ing.Stats(), s.peer.UploadLoss(peer.KindVideo), s.peer.UploadLoss(peer.KindAudio), now)
			st.RTT = s.peer.RTT()
			stats.mu.Lock()
			stats.latest = st
			stats.mu.Unlock()
			if !fw.connected() {
				continue
			}
			logger.Debug("Stream statistics", slog.Int("video_kbps", st.VideoKbps), slog.Int("audio_kbps", st.AudioKbps),
				slog.Float64("publisher_loss", st.PublisherLoss), slog.Float64("meeting_loss", st.MeetingLoss),
				slog.Int("quality", st.Quality), slog.Duration("rtt", st.RTT))
			s.sendEndpointStats(signaling.EndpointStats{
				UploadKbps: st.VideoKbps + st.AudioKbps, VideoUploadKbps: st.VideoKbps, AudioUploadKbps: st.AudioKbps,
				UploadLoss: st.MeetingLoss, TotalLoss: st.MeetingLoss, Quality: st.Quality, RTT: st.RTT,
			})
		}
	}
}

// statsWindow turns the ingest's running counts into one interval's
// statistics.
type statsWindow struct {
	prev peer.IngestStats
	at   time.Time
}

// next returns the statistics since the previous call: bitrates, loss from
// the publisher, and the loss to the meeting. The packets keep the
// publisher's sequence numbers, so the bridge's reported loss of video and
// audio (percent, weighted by packets) already includes the publisher's:
// the loss to the meeting is the larger of the two, the publisher's
// counting until the bridge reports.
func (w *statsWindow) next(cur peer.IngestStats, videoLoss, audioLoss float64, now time.Time) whip.StreamStats {
	prev, secs := w.prev, now.Sub(w.at).Seconds()
	w.prev, w.at = cur, now
	var st whip.StreamStats
	if secs <= 0 || prev == (peer.IngestStats{}) && cur == (peer.IngestStats{}) {
		st.Quality = quality(0)
		return st
	}
	kbps := func(b uint64) int { return int(float64(b) * 8 / secs / 1000) }
	dv, da := sub(cur.Video, prev.Video), sub(cur.Audio, prev.Audio)
	st.VideoKbps, st.AudioKbps = kbps(dv.Bytes), kbps(da.Bytes)
	received, lost := dv.Packets+da.Packets, dv.Lost+da.Lost
	if received+lost > 0 {
		st.PublisherLoss = 100 * float64(lost) / float64(received+lost)
	}
	var bridge float64
	if received > 0 {
		bridge = (videoLoss*float64(dv.Packets) + audioLoss*float64(da.Packets)) / float64(received)
	}
	st.MeetingLoss = max(st.PublisherLoss, bridge)
	st.Quality = quality(st.MeetingLoss)
	return st
}

// sub returns the counts of an interval; a counter that went back (a
// stream restarted) counts from zero.
func sub(cur, prev peer.KindStats) peer.KindStats {
	d := func(a, b uint64) uint64 {
		if a < b {
			return a
		}
		return a - b
	}
	return peer.KindStats{Packets: d(cur.Packets, prev.Packets), Bytes: d(cur.Bytes, prev.Bytes), Lost: d(cur.Lost, prev.Lost)}
}

// quality is lib-jitsi-meet's connection quality of a stream judged by its
// upload loss (percent): research R14.
func quality(loss float64) int {
	switch {
	case loss <= 2:
		return 100
	case loss <= 4:
		return 70
	case loss <= 6:
		return 50
	case loss <= 8:
		return 30
	case loss <= 12:
		return 10
	}
	return 0
}
