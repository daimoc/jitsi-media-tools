package jitsi

import (
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/daimoc/jitsi-media-tools/internal/signaling"
)

// The bridge channel carries Colibri messages between a client and the
// bridge (JSON text, "colibriClass" naming the message). The bot tells the
// bridge what it receives (specs/005 contracts/colibri-messages.md):
//
//   - ReceiverVideoConstraints: the bridge forwards no video to a client
//     until it gets one. Without a receive cap every video source is asked
//     for; with a cap only the received ones, by source name.
//   - ReceiverAudioSubscription, with a cap only: the received audio
//     sources, by name, so the bridge stops sending the others. Without a
//     cap nothing is sent: the bridge forwards all audio by default.
//
// The bridge never acknowledges them; the bot keeps its own cap (004) so a
// bridge that ignores them changes nothing else.

// defaultVideoHeight is the height asked for each received video when the
// handler gives none: the height the bots send without simulcast (004
// research R1).
const defaultVideoHeight = 360

// defaultBridgeOpenTimeout is how long after opening the bridge channel a
// bot waits before logging that it is not open.
const defaultBridgeOpenTimeout = 10 * time.Second

// receivePrefs is what a bot receives, from its session sources.
type receivePrefs struct {
	capped       bool     // A receive cap is set
	audio        []string // Names of the received remote audio sources
	audioUnnamed bool     // A received audio source has no name
	video        []string // Names of the received remote video sources
	videoUnnamed bool     // A received video source has no name
	height       int      // Video height asked for (handler's RequestedVideoHeight); 0: defaultVideoHeight
	layers       []int    // Heights of the simulcast layers received (handler's VideoLayerHeights); nil without
}

// bridgeLayerHeights are the heights the bridge assumes for three simulcast
// layers, whatever their real size: those of Jitsi web (Jicofo signals no
// height). It picks the layer to forward by these.
var bridgeLayerHeights = []int{180, 360, 720}

// bridgeHeight returns the height to ask the bridge for so that it forwards
// the highest of layers (real heights, low to high) at most height lines
// high, or the lowest layer when none is. Without three layers it returns
// height.
func bridgeHeight(height int, layers []int) int {
	if len(layers) != len(bridgeLayerHeights) {
		return height
	}
	i := 0
	for j, h := range layers {
		if h <= height {
			i = j
		}
	}
	return bridgeLayerHeights[i]
}

type videoConstraints struct {
	MaxHeight int `json:"maxHeight"`
}

// videoConstraintsMessage builds the ReceiverVideoConstraints for prefs.
func videoConstraintsMessage(p receivePrefs) string {
	m := struct {
		Class       string                       `json:"colibriClass"`
		LastN       int                          `json:"lastN"`
		Default     videoConstraints             `json:"defaultConstraints"`
		Constraints *map[string]videoConstraints `json:"constraints,omitempty"` // Nil: omitted; empty: {}
		OnStage     []string                     `json:"onStageSources,omitempty"`
	}{Class: "ReceiverVideoConstraints", LastN: -1}
	height := p.height
	if height <= 0 {
		height = defaultVideoHeight
	}
	if len(p.layers) > 0 {
		// Simulcast: the bridge keeps sources neither on stage nor
		// selected at their lowest layer, so every received source is on
		// stage (006 T057).
		height = bridgeHeight(height, p.layers)
		if !p.videoUnnamed {
			m.OnStage = p.video
		}
	}
	m.Default.MaxHeight = height
	if p.capped && !p.videoUnnamed {
		// Only the listed sources: the others get maxHeight 0, i.e. nothing.
		m.Default.MaxHeight = 0
		constraints := make(map[string]videoConstraints, len(p.video))
		for _, name := range p.video {
			constraints[name] = videoConstraints{MaxHeight: height}
		}
		m.Constraints = &constraints
	}
	return marshalMessage(m)
}

// audioSubscriptionMessage builds the ReceiverAudioSubscription for prefs;
// false when none is sent (no cap, or a received source without a name).
func audioSubscriptionMessage(p receivePrefs) (string, bool) {
	if !p.capped || p.audioUnnamed {
		return "", false
	}
	list := p.audio
	if list == nil {
		list = []string{} // "list":[] (nothing received yet), not null
	}
	return marshalMessage(struct {
		Class string   `json:"colibriClass"`
		Mode  string   `json:"mode"`
		List  []string `json:"list"`
	}{"ReceiverAudioSubscription", "Include", list}), true
}

func marshalMessage(m any) string {
	b, err := json.Marshal(m)
	if err != nil {
		panic(err) // Plain structs of strings and ints
	}
	return string(b)
}

// bridgeChannel sends a session's receive preferences on the bridge channel:
// the latest ones when it opens or opens again, and every change while it
// is open (messages identical to the last one sent are skipped). It
// implements signaling.DataChannelHandler.
type bridgeChannel struct {
	logger      *slog.Logger
	openTimeout time.Duration

	mu        sync.Mutex
	dc        signaling.DataChannel // Nil until OpenDataChannel returned
	open      bool
	stopped   bool
	prefs     receivePrefs
	policy    *signaling.ReceivePolicy // When set, replaces prefs' video constraints
	onSpeaker func(id string, previous []string)
	lastSent  map[string]string // colibriClass -> last message sent; cleared on close
	logged    map[string]bool   // Fallback reasons already logged
	openTimer *time.Timer
}

func newBridgeChannel(logger *slog.Logger) *bridgeChannel {
	return &bridgeChannel{
		logger:      logger,
		openTimeout: defaultBridgeOpenTimeout,
		lastSent:    map[string]string{},
		logged:      map[string]bool{},
	}
}

// setChannel binds the data channel returned by OpenDataChannel and starts
// waiting for it to open.
func (b *bridgeChannel) setChannel(dc signaling.DataChannel) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.stopped {
		return
	}
	b.dc = dc
	if b.open {
		b.sendLocked()
		return
	}
	b.openTimer = time.AfterFunc(b.openTimeout, func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if !b.open && !b.stopped {
			b.logOnceLocked("not-open", "Bridge channel not open: video not requested, receive cap enforced by the bot only", slog.Duration("after", b.openTimeout))
		}
	})
}

// update records the current receive preferences and sends what changed if
// the channel is open.
func (b *bridgeChannel) update(p receivePrefs) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.prefs = p
	b.sendLocked()
}

// stop ends the bridge channel's work for a terminated session.
func (b *bridgeChannel) stop() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.stopped = true
	if b.openTimer != nil {
		b.openTimer.Stop()
	}
}

func (b *bridgeChannel) OnOpen() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.open = true
	if b.openTimer != nil {
		b.openTimer.Stop()
	}
	b.logger.Debug("Bridge channel open")
	b.sendLocked()
}

func (b *bridgeChannel) OnClose() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.open = false
	clear(b.lastSent)
	if !b.stopped {
		b.logOnceLocked("closed", "Bridge channel closed: preferences will be sent again if it reopens")
	}
}

// setPolicy replaces the video constraints sent (nil: back to prefs').
func (b *bridgeChannel) setPolicy(p *signaling.ReceivePolicy) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.policy = p
	b.sendLocked()
}

// OnMessage handles the bridge's dominant speaker changes and logs the
// class of the other messages.
func (b *bridgeChannel) OnMessage(text string) {
	var m struct {
		Class    string   `json:"colibriClass"`
		Speaker  string   `json:"dominantSpeakerEndpoint"`
		Previous []string `json:"previousSpeakers"`
	}
	if err := json.Unmarshal([]byte(text), &m); err != nil {
		b.logger.Debug("Ignoring an invalid bridge channel message", slog.String("error", err.Error()))
		return
	}
	b.logger.Debug("Bridge channel message", slog.String("colibri_class", m.Class))
	if m.Class == "DominantSpeakerEndpointChangeEvent" && m.Speaker != "" {
		b.mu.Lock()
		f := b.onSpeaker
		b.mu.Unlock()
		if f != nil {
			f(m.Speaker, m.Previous)
		}
	}
}

// policyConstraintsMessage builds the ReceiverVideoConstraints of a policy.
func policyConstraintsMessage(p *signaling.ReceivePolicy) string {
	m := struct {
		Class       string                      `json:"colibriClass"`
		LastN       int                         `json:"lastN"`
		Selected    []string                    `json:"selectedSources,omitempty"`
		OnStage     []string                    `json:"onStageSources,omitempty"`
		Default     videoConstraints            `json:"defaultConstraints"`
		Constraints map[string]videoConstraints `json:"constraints,omitempty"`
	}{Class: "ReceiverVideoConstraints", LastN: p.LastN, Selected: p.Selected, OnStage: p.OnStage,
		Default: videoConstraints{MaxHeight: p.DefaultMaxHeight}}
	if len(p.MaxHeights) > 0 {
		m.Constraints = map[string]videoConstraints{}
		for name, h := range p.MaxHeights {
			m.Constraints[name] = videoConstraints{MaxHeight: h}
		}
	}
	return marshalMessage(m)
}

// sendLocked sends the messages for the current preferences that differ from
// the last ones sent. Called with mu held.
func (b *bridgeChannel) sendLocked() {
	if !b.open || b.dc == nil || b.stopped {
		return
	}
	if b.prefs.capped && b.prefs.videoUnnamed {
		b.logOnceLocked("video-unnamed", "A received video source has no name: asking for every video")
	}
	video := videoConstraintsMessage(b.prefs)
	if b.policy != nil {
		video = policyConstraintsMessage(b.policy)
	}
	messages := []struct{ class, text string }{
		{"ReceiverVideoConstraints", video},
	}
	if text, ok := audioSubscriptionMessage(b.prefs); ok {
		messages = append(messages, struct{ class, text string }{"ReceiverAudioSubscription", text})
	} else if b.prefs.capped && b.prefs.audioUnnamed {
		b.logOnceLocked("audio-unnamed", "A received audio source has no name: no audio subscription, receive cap enforced by the bot only")
	}
	for _, m := range messages {
		if b.lastSent[m.class] == m.text {
			continue
		}
		if err := b.dc.SendText(m.text); err != nil {
			b.logOnceLocked("send", "Failed to send on the bridge channel", slog.String("colibri_class", m.class), slog.String("error", err.Error()))
			continue
		}
		b.lastSent[m.class] = m.text
		b.logger.Debug("Bridge channel message sent", slog.String("message", m.text))
	}
}

// logOnceLocked logs msg at Info the first time reason occurs.
func (b *bridgeChannel) logOnceLocked(reason, msg string, attrs ...any) {
	if b.logged[reason] {
		return
	}
	b.logged[reason] = true
	b.logger.Info(msg, attrs...)
}
