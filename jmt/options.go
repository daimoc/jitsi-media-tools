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
	BasePort int    // Default 9000
	MaxFeeds int    // Default 16
	Grid     bool
}
