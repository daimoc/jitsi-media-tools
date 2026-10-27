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
	return &Gateway{cfg: cfg}
}

// Publish checks the offer, joins the meeting, then answers the publisher.
// ctx bounds the join.
func (g *Gateway) Publish(ctx context.Context, r whip.Request) (whip.Stream, error) {
	kinds, err := peer.CheckOffer(r.Offer)
	if err != nil {
		return whip.Stream{}, &whip.Error{Status: http.StatusBadRequest, Msg: strings.TrimPrefix(err.Error(), peer.ErrUnsupportedOffer.Error()+": ")}
	}
	meeting, _, _ := strings.Cut(r.MeetingURL, "?")
	logger := g.cfg.Session.Logger.With(slog.String("meeting", meeting), slog.String("name", r.Name))
	fw := &Forwarded{}
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
	}, nil
}
