# Contract: public library `jmt`

Import: `github.com/daimoc/jitsi-media-tools/jmt`. Only this package is public
(Principle I). No type here exposes RTP, SDP, codecs or Pion (Principle II).
Signatures are the contract; doc comments and names may be refined in implementation,
but any change of shape is recorded here first.

```go
package jmt

// Options describes one session. Zero values take the documented defaults.
type Options struct {
    MeetingURL  string // https://host[/tenant]/Room[?jwt=...]; required
    DisplayName string // required

    // Server overrides; empty fields come from the server's config.js.
    XMPPDomain, MUCDomain, AnonymousDomain, WebSocketURL, FocusJID string
    Insecure bool // skip TLS certificate verification

    Token       string // ready-made JWT
    AppID       string // sign a JWT per session with AppSecret
    AppSecret   string
    Audience, Subject string
    TokenTTL    time.Duration
    Moderator   bool

    WaitHost time.Duration // guest wait for a host; default 5m

    FFmpeg, FFplay string // executables; default from PATH

    Selection Selection
    Publish   Source  // nil: publish nothing
    Display   *DisplayOptions
    Record    *RecordOptions
    OBS       *OBSOptions

    Logger *slog.Logger
}

type Selection struct {
    Limit     int      // default 9, max 25
    Height    int      // 180 (default), 360, 720
    Pins      []string // display names
    PinHeight int      // default 720
}

type DisplayOptions struct{ Title string }

type RecordMode int
const (
    RecordPerParticipant RecordMode = iota
    RecordMixed
)
type RecordOptions struct {
    Dir  string
    Mode RecordMode
}

type OBSOptions struct {
    Host     string // address to listen on; default "0.0.0.0"
    BasePort int    // default 9000 (grid); participants from BasePort+1
    MaxFeeds int    // default 16
    Grid     bool
}

// Source is a publish source; build one with the constructors below.
type Source interface{ isSource() }

func FileSource(path string, loop bool) Source
func DeviceSource(video, audio string) Source             // names from ListDevices; "" omits
func OBSSource(protocol, listenAddr string) Source         // "srt" or "rtmp", e.g. ":9100"
func FrameSource(width, height int, audio, video bool) (*Publisher, Source)

type Publisher struct{ /* unexported */ }
func (p *Publisher) WriteVideo(f VideoFrame) error // f.Width/Height must match
func (p *Publisher) WriteAudio(c AudioChunk) error
func (p *Publisher) Close() error

type Device struct {
    Kind string // "video" or "audio"
    Name string // what DeviceSource takes
    Description string
}
func ListDevices(ctx context.Context, ffmpeg string) ([]Device, error)

type Participant struct {
    ID, Name               string
    AudioMuted, VideoMuted bool
    Pinned, Selected       bool
}

type VideoFrame struct {
    ParticipantID string
    Width, Height int
    Y, U, V       []byte // yuv420p planes, tightly packed
    Time          time.Time
}

type AudioChunk struct {
    ParticipantID string  // "" for the room mix
    Samples       []int16 // 960 frames, 48 kHz stereo interleaved (1920 values)
    Time          time.Time
}

type EventKind int
const (
    ParticipantJoined EventKind = iota
    ParticipantLeft
    ParticipantChanged
    WaitingForHost
    FeedReady // OBS feed address available
)
type Event struct {
    Kind        EventKind
    Participant Participant
    FeedURL     string // FeedReady only; "" participant id means the grid
}

// Join connects and joins the room; it returns once joined (or failed),
// after waiting for a host if needed.
func Join(ctx context.Context, o Options) (*Session, error)

type Session struct{ /* unexported */ }

func (s *Session) Events() <-chan Event              // closed when the session ends
func (s *Session) Participants() []Participant
func (s *Session) OnVideo(f func(VideoFrame))         // per participant, selected only
func (s *Session) OnAudio(f func(AudioChunk))         // per participant
func (s *Session) OnMixedAudio(f func(AudioChunk))
func (s *Session) OnGrid(f func(VideoFrame))          // composed grid, 30 fps
func (s *Session) SetSelection(sel Selection) error
func (s *Session) FeedURLs() map[string]string        // unique name ("" = grid) → srt URL
func (s *Session) Close() error                       // leaves the room, stops all helpers
func (s *Session) Wait() error                        // blocks until closed; nil after Close
```

Behavior rules:

- Callbacks run on session goroutines and must not block; slow callbacks lose frames
  (the session drops, never stalls).
- Frame buffers passed to callbacks are valid only during the call (copy to keep).
- `Join` fails before contacting the server when ffmpeg (or a needed ffmpeg part, or
  ffplay for Display) is missing (FR-023).
- `Close` is idempotent; after it returns no helper process is running (SC-008).
