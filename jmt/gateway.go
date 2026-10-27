package jmt

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/daimoc/jitsi-media-tools/internal/peer"
	"github.com/daimoc/jitsi-media-tools/internal/session"
	"github.com/daimoc/jitsi-media-tools/internal/whip"
)

// GatewayOptions describes a WHIP gateway. Zero values take the
// documented defaults.
type GatewayOptions struct {
	Listen   string // HTTP address; default ":8080"
	Secret   string // Bearer token every request must carry; required
	UDPPort  int    // One UDP port, on every interface, for all publishers' media; default 8189, -1 any free port
	PublicIP string // Address given to publishers behind NAT; "" gives the local addresses
	// TLSCert and TLSKey, both set, serve HTTPS; otherwise plain HTTP, for
	// TLS from a reverse proxy in front.
	TLSCert, TLSKey string
	MaxStreams      int // Streams at once; default 10

	// Applied to every meeting a stream joins.
	Insecure bool          // Skip TLS certificate verification of the meetings' servers
	WaitHost time.Duration // Guest wait for a host; default 30s, so the publisher's request does not time out
	Logger   *slog.Logger
}

// Gateway lets WHIP publishers such as OBS stream into any Jitsi meeting.
// A publisher posts its offer to /whip/<server[:port]>/<room>?name=<name>
// (optionally &jwt=<token> for the meeting) with the secret as its bearer
// token; the gateway joins the meeting as that participant and forwards
// the publisher's H.264 video and Opus audio unchanged. The participant
// leaves when the publisher stops (DELETE on the stream's address), after
// 10 s without media, or when the gateway closes.
type Gateway struct {
	o      GatewayOptions
	ln     net.Listener
	mux    peer.UDPMux
	h      *whip.Handler
	srv    *http.Server
	events *eventQueue[GatewayEvent]

	closeOnce sync.Once
	closed    chan struct{}
}

// GatewayStream is a stream on the gateway.
type GatewayStream struct {
	ID          string // As in the stream's address, /whip/session/<ID>
	MeetingURL  string // Without the token
	DisplayName string
	Started     time.Time
}

// GatewayEventKind is what a GatewayEvent reports.
type GatewayEventKind int

const (
	StreamStarted GatewayEventKind = iota
	StreamEnded                    // The publisher stopped, or the gateway closed
	StreamFailed                   // Refused, or ended by an error (Err)
)

func (k GatewayEventKind) String() string {
	switch k {
	case StreamStarted:
		return "started"
	case StreamEnded:
		return "ended"
	case StreamFailed:
		return "failed"
	}
	return "unknown"
}

// GatewayEvent is a stream starting, ending or failing.
type GatewayEvent struct {
	Kind   GatewayEventKind
	Stream GatewayStream // No ID when the stream was refused
	Err    error
}

// NewGateway checks the options and opens the HTTP and UDP ports.
func NewGateway(o GatewayOptions) (*Gateway, error) {
	if o.Secret == "" {
		return nil, errors.New("gateway: a secret is required")
	}
	if (o.TLSCert == "") != (o.TLSKey == "") {
		return nil, errors.New("gateway: HTTPS needs both a certificate and a key")
	}
	if o.Listen == "" {
		o.Listen = ":8080"
	}
	switch {
	case o.UDPPort == 0:
		o.UDPPort = 8189
	case o.UDPPort < 0:
		o.UDPPort = 0
	}
	if o.MaxStreams <= 0 {
		o.MaxStreams = 10
	}
	if o.WaitHost <= 0 {
		o.WaitHost = 30 * time.Second
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	var publicIPs []string
	if o.PublicIP != "" {
		if net.ParseIP(o.PublicIP) == nil {
			return nil, errors.New("gateway: the public IP is not an IP address")
		}
		publicIPs = []string{o.PublicIP}
	}
	ln, err := net.Listen("tcp", o.Listen)
	if err != nil {
		return nil, err
	}
	mux, err := peer.NewUDPMux(o.UDPPort, o.Logger)
	if err != nil {
		ln.Close()
		return nil, fmt.Errorf("gateway: UDP port %d: %w", o.UDPPort, err)
	}
	g := &Gateway{o: o, ln: ln, mux: mux, events: newEventQueue[GatewayEvent](), closed: make(chan struct{})}
	pub := session.NewGateway(session.GatewayConfig{
		Session:   session.Config{Insecure: o.Insecure, WaitHost: o.WaitHost, Logger: o.Logger},
		UDPMux:    g.mux,
		PublicIPs: publicIPs,
	})
	g.h = &whip.Handler{Secret: o.Secret, MaxStreams: o.MaxStreams, Publisher: pub, Logger: o.Logger, OnEvent: g.onEvent}
	g.srv = &http.Server{Handler: g.h, ReadHeaderTimeout: 10 * time.Second}
	return g, nil
}

func (g *Gateway) onEvent(e whip.Event) {
	g.events.push(GatewayEvent{Kind: GatewayEventKind(e.Kind), Stream: streamFrom(e.Stream), Err: e.Err})
}

func streamFrom(i whip.Info) GatewayStream {
	return GatewayStream{ID: i.ID, MeetingURL: i.MeetingURL, DisplayName: i.Name, Started: i.Started}
}

// Addr returns the HTTP address the gateway listens on.
func (g *Gateway) Addr() string { return g.ln.Addr().String() }

// Serve answers publishers until ctx ends or Close is called. It returns
// nil then, or the error that stopped the server.
func (g *Gateway) Serve(ctx context.Context) error {
	go func() {
		select {
		case <-ctx.Done():
			g.Close()
		case <-g.closed:
		}
	}()
	var err error
	if g.o.TLSCert != "" {
		err = g.srv.ServeTLS(g.ln, g.o.TLSCert, g.o.TLSKey)
	} else {
		err = g.srv.Serve(g.ln)
	}
	if errors.Is(err, http.ErrServerClosed) {
		<-g.closed
		return nil
	}
	g.Close()
	return err
}

// Streams returns the streams running now.
func (g *Gateway) Streams() []GatewayStream {
	var out []GatewayStream
	for _, i := range g.h.Streams() {
		out = append(out, streamFrom(i))
	}
	return out
}

// Events returns the gateway's events, in order; the channel is closed
// once the gateway is closed. Events are queued, never dropped.
func (g *Gateway) Events() <-chan GatewayEvent { return g.events.out }

// Close ends every stream, each participant leaving its meeting, and stops
// the server. It is idempotent.
func (g *Gateway) Close() error {
	g.closeOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		g.srv.Shutdown(ctx) // Stops accepting; requests being answered finish
		g.h.Close()
		g.mux.Close()
		g.events.close()
		close(g.closed)
	})
	return nil
}
