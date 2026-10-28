package jitsi

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/daimoc/jitsi-media-tools/internal/signaling"
)

// Adapter implements the PlatformAdapter interface for Jitsi.
type Adapter struct {
	logger *slog.Logger
	config JingleConfig

	clients map[string]*SignalingClient
	mu      sync.Mutex
}

// JingleConfig holds the configuration for the Jitsi adapter.
// It matches the schema defined in specs/003-xmpp-signaling/contracts/configuration.yaml.
type JingleConfig struct {
	XMPPDomain        string `json:"xmpp_domain"`
	MUCDomain         string `json:"muc_domain"`
	RoomName          string `json:"room_name"`
	WebSocketURL      string `json:"websocket_url"`
	FocusJID          string `json:"focus_jid"`
	ConnectTimeout    string `json:"connect_timeout"`
	KeepaliveInterval string `json:"keepalive_interval"`
	Debug             bool   `json:"debug"` // Log all XMPP traffic to stdout
	// Insecure skips TLS certificate verification (self-signed servers).
	Insecure bool `json:"insecure"`
	// NoBridgeChannel leaves the bridge channel closed, so no receive
	// preferences are sent (the behavior before feature 005); for comparison
	// runs.
	NoBridgeChannel bool `json:"no_bridge_channel"`

	// Token authentication (Prosody mod_auth_token): either a ready-made
	// JWT shared by all bots, or an app id and secret from which each bot
	// signs its own token (see jwt.go).
	JWT          string `json:"jwt"`
	JWTAppID     string `json:"jwt_app_id"`
	JWTAppSecret string `json:"jwt_app_secret"`
	JWTAudience  string `json:"jwt_audience"`
	JWTSubject   string `json:"jwt_subject"`
	JWTTTL       string `json:"jwt_ttl"`
	JWTModerator bool   `json:"jwt_moderator"`

	connectTimeout    time.Duration // Parsed ConnectTimeout
	keepaliveInterval time.Duration // Parsed KeepaliveInterval
	jwtTTL            time.Duration // Parsed JWTTTL
}

// Adapter implements signaling.PlatformAdapter.
var _ signaling.PlatformAdapter = (*Adapter)(nil)

// NewAdapter creates a new Jitsi adapter. Call ParseConfig before Connect.
func NewAdapter(logger *slog.Logger) *Adapter {
	if logger == nil {
		logger = slog.Default()
	}
	return &Adapter{
		logger:  logger,
		clients: make(map[string]*SignalingClient),
	}
}

// ParseConfig validates and extracts the platform configuration.
func (a *Adapter) ParseConfig(config map[string]interface{}) error {
	// Convert map to JSON bytes then unmarshal into struct
	// This is a common way to handle generic map decoding in Go
	data, err := json.Marshal(config)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	if err := json.Unmarshal(data, &a.config); err != nil {
		return fmt.Errorf("failed to parse jitsi config: %w", err)
	}

	// Validate required fields
	if a.config.XMPPDomain == "" {
		return fmt.Errorf("xmpp_domain is required")
	}
	if a.config.RoomName == "" {
		return fmt.Errorf("room_name is required")
	}

	// Set defaults
	if a.config.MUCDomain == "" {
		a.config.MUCDomain = fmt.Sprintf("conference.%s", a.config.XMPPDomain)
	}
	if a.config.WebSocketURL == "" {
		a.config.WebSocketURL = fmt.Sprintf("wss://%s/xmpp-websocket", a.config.XMPPDomain)
	}
	if a.config.FocusJID == "" {
		a.config.FocusJID = fmt.Sprintf("focus.%s", a.config.XMPPDomain)
	}
	if a.config.ConnectTimeout == "" {
		a.config.ConnectTimeout = "10s"
	}
	if a.config.KeepaliveInterval == "" {
		a.config.KeepaliveInterval = "30s"
	}

	if a.config.connectTimeout, err = parsePositiveDuration("connect_timeout", a.config.ConnectTimeout); err != nil {
		return err
	}
	if a.config.keepaliveInterval, err = parsePositiveDuration("keepalive_interval", a.config.KeepaliveInterval); err != nil {
		return err
	}
	if err := a.config.parseJWT(); err != nil {
		return err
	}

	a.logger.Info("Jitsi configuration parsed successfully",
		slog.String("xmpp_domain", a.config.XMPPDomain),
		slog.String("room", a.config.RoomName),
		slog.String("muc_domain", a.config.MUCDomain),
		slog.String("websocket_url", a.config.WebSocketURL),
		slog.String("focus_jid", a.config.FocusJID),
		slog.String("jwt_mode", a.config.jwtMode()))

	return nil
}

// parseJWT validates the token authentication settings and sets their
// defaults.
func (c *JingleConfig) parseJWT() error {
	if c.JWT != "" && (c.JWTAppID != "" || c.JWTAppSecret != "") {
		return fmt.Errorf("jwt and jwt_app_id/jwt_app_secret are mutually exclusive")
	}
	if (c.JWTAppID == "") != (c.JWTAppSecret == "") {
		return fmt.Errorf("jwt_app_id and jwt_app_secret must be set together")
	}
	if c.JWTAudience == "" {
		c.JWTAudience = "jitsi"
	}
	if c.JWTSubject == "" {
		c.JWTSubject = c.XMPPDomain
	}
	if c.JWTTTL == "" {
		c.JWTTTL = "1h"
	}
	var err error
	c.jwtTTL, err = parsePositiveDuration("jwt_ttl", c.JWTTTL)
	return err
}

func parsePositiveDuration(name, value string) (time.Duration, error) {
	d, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("invalid %s %q: %w", name, value, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("%s must be positive, got %q", name, value)
	}
	return d, nil
}

// Connect connects a peer to the Jitsi meeting: XMPP connection, conference
// request to the focus, and MUC join. It blocks until the room is joined, the
// join fails, connect_timeout elapses, or ctx is cancelled. On error the
// client is closed and forgotten.
func (a *Adapter) Connect(ctx context.Context, peerID string, handler signaling.SignalingHandler) error {
	logger := a.logger.With(slog.String("peer_id", peerID))
	logger.Info("Jitsi adapter: Connecting peer")

	ctx, cancel := context.WithTimeout(ctx, a.config.connectTimeout)
	defer cancel()

	token, err := a.config.tokenFor(peerID)
	if err != nil {
		return fmt.Errorf("jitsi join failed: %w", err)
	}
	client := NewSignalingClient(logger, a.config)
	client.token = token
	// A concurrent Disconnect aborts the join (pending IQs are not failed
	// by mellium when the stream closes).
	stop := context.AfterFunc(client.closedCtx, cancel)
	defer stop()
	client.SetHandler(handler)
	client.SetOnLost(func(err error) {
		if a.forget(peerID, client) {
			client.Disconnect()
		}
		handler.OnSignalingLost(err)
	})

	// Register before connecting so that a concurrent Disconnect closes it.
	a.mu.Lock()
	if _, exists := a.clients[peerID]; exists {
		a.mu.Unlock()
		return fmt.Errorf("peer %s is already connected", peerID)
	}
	a.clients[peerID] = client
	a.mu.Unlock()

	err = client.Connect(ctx)
	if err == nil {
		err = client.JoinMUC(ctx, peerID)
	}
	if err == nil {
		err = client.WaitJoined(ctx)
	}
	if err != nil {
		a.forget(peerID, client)
		client.Disconnect()
		return fmt.Errorf("jitsi join failed: %w", err)
	}
	return nil
}

// forget removes client from the registry if it is still registered for
// peerID, and reports whether it did.
func (a *Adapter) forget(peerID string, client *SignalingClient) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.clients[peerID] != client {
		return false
	}
	delete(a.clients, peerID)
	return true
}

// Disconnect terminates the peer's Jingle sessions, leaves the room and
// closes the connection.
func (a *Adapter) Disconnect(peerID string) error {
	a.logger.Info("Jitsi adapter: Disconnecting peer", slog.String("peer_id", peerID))

	a.mu.Lock()
	client, ok := a.clients[peerID]
	delete(a.clients, peerID)
	a.mu.Unlock()

	if !ok {
		return nil // Already disconnected
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client.TerminateSessions(ctx)

	if err := client.Disconnect(); err != nil {
		a.logger.Error("Failed to disconnect XMPP client", slog.String("peer_id", peerID), slog.String("error", err.Error()))
		return err
	}
	return nil
}

// SendEndpointStats sends a peer's connection statistics to the meeting.
func (a *Adapter) SendEndpointStats(peerID string, st signaling.EndpointStats) {
	a.mu.Lock()
	client := a.clients[peerID]
	a.mu.Unlock()
	if client != nil {
		client.SendEndpointStats(st)
	}
}

// SetReceivePolicy sets which video the bridge forwards to a connected peer
// (see SignalingClient.SetReceivePolicy). It does nothing for an unknown
// peer.
func (a *Adapter) SetReceivePolicy(peerID string, p *signaling.ReceivePolicy) {
	a.mu.Lock()
	client := a.clients[peerID]
	a.mu.Unlock()
	if client != nil {
		client.SetReceivePolicy(p)
	}
}
