package jmt

import (
	"log/slog"
	"time"
)

// Options describes one session. Zero values take the documented defaults.
type Options struct {
	MeetingURL  string // https://host[/tenant]/Room[?jwt=...]; required
	DisplayName string // Required

	// Server overrides; empty fields come from the server's config.js.
	XMPPDomain, MUCDomain, AnonymousDomain, WebSocketURL, FocusJID string
	// Insecure skips TLS certificate verification (self-signed servers).
	Insecure bool

	Token     string // Ready-made JWT
	AppID     string // Sign a JWT per session with AppSecret
	AppSecret string
	Audience  string // JWT "aud"; default "jitsi"
	Subject   string // JWT "sub"; default the XMPP domain
	TokenTTL  time.Duration
	Moderator bool

	WaitHost time.Duration // Guest wait for a host; default 5m
	// OnWaitingForHost, when set, is called during Join once the guest
	// starts waiting for a host (Events only delivers WaitingForHost after
	// Join returns).
	OnWaitingForHost func()

	FFmpeg, FFplay string // Executables; default from PATH

	Selection Selection
	Publish   Source // Nil: publish nothing
	Display   *DisplayOptions
	Record    *RecordOptions
	OBS       *OBSOptions

	Logger *slog.Logger
}

// Selection is which participants' video the session receives: the Limit
// most recent speakers, always including the pinned display names.
type Selection struct {
	Limit     int      // Default 9, max 25
	Height    int      // 180 (default), 360 or 720
	Pins      []string // Display names (case-insensitive)
	PinHeight int      // Default 720
	// Budget caps the received video, in bits/s (0: none; else at least
	// 100000). Unpinned participants' resolution goes first, then their
	// number, then pinned participants' resolution; a pinned participant
	// is never dropped. Bridges that let receivers set their bandwidth
	// also enforce it.
	Budget int
}

// DisplayOptions shows the room in a tile-grid window (needs ffplay).
type DisplayOptions struct{ Title string }

// RecordMode is how the session records.
type RecordMode int

const (
	RecordPerParticipant RecordMode = iota // One file per participant
	RecordMixed                            // One file of the grid and mixed audio
)

// RecordOptions records the meeting into Dir.
type RecordOptions struct {
	Dir  string
	Mode RecordMode
}

// OBSOptions serves SRT feeds for OBS Media Sources: the grid on BasePort
// (when Grid), participants on the following ports.
type OBSOptions struct {
	Host     string // Address to listen on; default "0.0.0.0"
	URLHost  string // Address OBS reaches the feeds at (e.g. the host of a container); default Host, or this machine's outbound address
	BasePort int    // Default 9000
	MaxFeeds int    // Default 16
	Grid     bool
}
