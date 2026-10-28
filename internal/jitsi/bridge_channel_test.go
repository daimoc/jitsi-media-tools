package jitsi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/daimoc/jitsi-media-tools/internal/signaling"
)

// logBuffer is a concurrency-safe log destination.
type logBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *logBuffer) count(substr string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.Count(b.buf.String(), substr)
}

func newLogBuffer() (*logBuffer, *slog.Logger) {
	b := &logBuffer{}
	return b, slog.New(slog.NewTextHandler(b, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// jsonEqual compares two JSON documents by value.
func jsonEqual(t *testing.T, got, want string) {
	t.Helper()
	var g, w any
	if err := json.Unmarshal([]byte(got), &g); err != nil {
		t.Fatalf("invalid JSON %q: %v", got, err)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("invalid expected JSON %q: %v", want, err)
	}
	if !reflect.DeepEqual(g, w) {
		t.Errorf("got %s\nwant %s", got, want)
	}
}

func TestVideoConstraintsMessage(t *testing.T) {
	for _, tc := range []struct {
		name  string
		prefs receivePrefs
		want  string
	}{
		{"no cap", receivePrefs{video: []string{"a-v0"}},
			`{"colibriClass":"ReceiverVideoConstraints","lastN":-1,"defaultConstraints":{"maxHeight":360}}`},
		{"cap", receivePrefs{capped: true, video: []string{"a-v0", "b-v0"}},
			`{"colibriClass":"ReceiverVideoConstraints","lastN":-1,"defaultConstraints":{"maxHeight":0},"constraints":{"a-v0":{"maxHeight":360},"b-v0":{"maxHeight":360}}}`},
		{"cap, nothing received", receivePrefs{capped: true},
			`{"colibriClass":"ReceiverVideoConstraints","lastN":-1,"defaultConstraints":{"maxHeight":0},"constraints":{}}`},
		{"no cap, height 90", receivePrefs{video: []string{"a-v0"}, height: 90},
			`{"colibriClass":"ReceiverVideoConstraints","lastN":-1,"defaultConstraints":{"maxHeight":90}}`},
		{"cap, height 720", receivePrefs{capped: true, video: []string{"a-v0"}, height: 720},
			`{"colibriClass":"ReceiverVideoConstraints","lastN":-1,"defaultConstraints":{"maxHeight":0},"constraints":{"a-v0":{"maxHeight":720}}}`},
		// Simulcast: received sources on stage, height translated to the
		// bridge's assumed layer heights.
		{"simulcast 360, height 360", receivePrefs{video: []string{"a-v0"}, height: 360, layers: []int{90, 180, 360}},
			`{"colibriClass":"ReceiverVideoConstraints","lastN":-1,"defaultConstraints":{"maxHeight":720},"onStageSources":["a-v0"]}`},
		{"simulcast 360, cap, height 90", receivePrefs{capped: true, video: []string{"a-v0", "b-v0"}, height: 90, layers: []int{90, 180, 360}},
			`{"colibriClass":"ReceiverVideoConstraints","lastN":-1,"defaultConstraints":{"maxHeight":0},"constraints":{"a-v0":{"maxHeight":180},"b-v0":{"maxHeight":180}},"onStageSources":["a-v0","b-v0"]}`},
		{"simulcast 720, height 360", receivePrefs{video: []string{"a-v0"}, height: 360, layers: []int{180, 360, 720}},
			`{"colibriClass":"ReceiverVideoConstraints","lastN":-1,"defaultConstraints":{"maxHeight":360},"onStageSources":["a-v0"]}`},
		{"simulcast, unnamed source", receivePrefs{video: []string{"a-v0"}, videoUnnamed: true, height: 180, layers: []int{90, 180, 360}},
			`{"colibriClass":"ReceiverVideoConstraints","lastN":-1,"defaultConstraints":{"maxHeight":360}}`},
		{"cap, unnamed source", receivePrefs{capped: true, video: []string{"a-v0"}, videoUnnamed: true},
			`{"colibriClass":"ReceiverVideoConstraints","lastN":-1,"defaultConstraints":{"maxHeight":360}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			jsonEqual(t, videoConstraintsMessage(tc.prefs), tc.want)
		})
	}
}

// expectSent returns the next text sent on c, or fails.
func expectSent(t *testing.T, c *fakeDataChannel) string {
	t.Helper()
	select {
	case m := <-c.sent:
		return m
	case <-time.After(2 * time.Second):
		t.Fatal("expected a message on the bridge channel")
		return ""
	}
}

// expectNothingSent fails if c sends within a short delay.
func expectNothingSent(t *testing.T, c *fakeDataChannel) {
	t.Helper()
	select {
	case m := <-c.sent:
		t.Fatalf("expected no message, got %s", m)
	case <-time.After(50 * time.Millisecond):
	}
}

func colibriClass(t *testing.T, msg string) string {
	t.Helper()
	var m struct {
		Class string `json:"colibriClass"`
	}
	if err := json.Unmarshal([]byte(msg), &m); err != nil {
		t.Fatalf("invalid JSON %q: %v", msg, err)
	}
	return m.Class
}

// newTestBridgeChannel returns a bridge channel bound to a fake data channel.
func newTestBridgeChannel(t *testing.T) (*bridgeChannel, *fakeDataChannel, *logBuffer) {
	t.Helper()
	logs, logger := newLogBuffer()
	b := newBridgeChannel(logger)
	c := newFakeDataChannel()
	c.setHandler(b)
	b.setChannel(c)
	t.Cleanup(b.stop)
	return b, c, logs
}

func TestBridgeChannel_SendRules(t *testing.T) {
	b, c, logs := newTestBridgeChannel(t)
	// The audio received does not change: the audio subscription is only
	// sent when the channel opens, after the video constraints.
	capped := func(video ...string) receivePrefs {
		return receivePrefs{capped: true, audio: []string{"x-a0"}, video: video}
	}
	expectAudio := func() {
		t.Helper()
		if m := expectSent(t, c); colibriClass(t, m) != "ReceiverAudioSubscription" {
			t.Fatalf("expected the audio subscription, got %s", m)
		}
	}

	// Nothing before open; the latest state is sent on open.
	b.update(capped("a-v0"))
	b.update(capped("a-v0", "b-v0"))
	expectNothingSent(t, c)
	c.open(t)
	jsonEqual(t, expectSent(t, c), videoConstraintsMessage(capped("a-v0", "b-v0")))
	expectAudio()
	expectNothingSent(t, c)

	// A change is sent, an identical update is not.
	b.update(capped("b-v0"))
	jsonEqual(t, expectSent(t, c), videoConstraintsMessage(capped("b-v0")))
	b.update(capped("b-v0"))
	expectNothingSent(t, c)

	// Closed: stored, then sent again on reopen even if unchanged.
	c.close(t)
	b.update(capped("c-v0"))
	expectNothingSent(t, c)
	c.open(t)
	jsonEqual(t, expectSent(t, c), videoConstraintsMessage(capped("c-v0")))
	expectAudio()
	if n := logs.count("Bridge channel closed"); n != 1 {
		t.Errorf("expected the close logged once, got %d", n)
	}

	// A failed send is logged once and retried on the next update.
	c.mu.Lock()
	c.fail = true
	c.mu.Unlock()
	b.update(capped("d-v0"))
	b.update(capped("e-v0"))
	if n := logs.count("Failed to send on the bridge channel"); n != 1 {
		t.Errorf("expected the send failure logged once, got %d", n)
	}
	c.mu.Lock()
	c.fail = false
	c.mu.Unlock()
	b.update(capped("e-v0"))
	jsonEqual(t, expectSent(t, c), videoConstraintsMessage(capped("e-v0")))
}

// Messages from the bridge are ignored without errors or replies (FR-009).
func TestBridgeChannel_IgnoresIncomingMessages(t *testing.T) {
	b, c, logs := newTestBridgeChannel(t)
	c.open(t)
	expectSent(t, c) // Initial state
	for _, m := range []string{`{"colibriClass":"ServerHello","version":"x"}`, `{"colibriClass":"SomethingNew"}`, `not json`, ``} {
		b.OnMessage(m)
	}
	expectNothingSent(t, c)
	if n := logs.count("level=WARN") + logs.count("level=ERROR"); n != 0 {
		t.Errorf("incoming messages must not log warnings or errors, got %d", n)
	}
}

// The channel may open before OpenDataChannel returns it.
func TestBridgeChannel_OpenBeforeSetChannel(t *testing.T) {
	_, logger := newLogBuffer()
	b := newBridgeChannel(logger)
	t.Cleanup(b.stop)
	c := newFakeDataChannel()
	c.setHandler(b)
	b.update(receivePrefs{})
	c.open(t)
	expectNothingSent(t, c)
	b.setChannel(c)
	jsonEqual(t, expectSent(t, c), videoConstraintsMessage(receivePrefs{}))
}

// Concurrent updates end with the latest state sent last.
func TestBridgeChannel_ConcurrentUpdates(t *testing.T) {
	b, c, _ := newTestBridgeChannel(t)
	c.open(t)
	expectSent(t, c)
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			b.update(receivePrefs{capped: true, video: []string{fmt.Sprintf("s%d-v0", i)}})
		}()
	}
	wg.Wait()
	final := receivePrefs{capped: true, video: []string{"final-v0"}}
	b.update(final)
	var last string
	for {
		select {
		case m := <-c.sent:
			last = m
			continue
		case <-time.After(100 * time.Millisecond):
		}
		break
	}
	jsonEqual(t, last, videoConstraintsMessage(final))
}

// Not open 10 s after the adapter opened it: logged once.
func TestBridgeChannel_LogsWhenNotOpen(t *testing.T) {
	logs, logger := newLogBuffer()
	b := newBridgeChannel(logger)
	t.Cleanup(b.stop)
	b.openTimeout = 20 * time.Millisecond
	b.setChannel(newFakeDataChannel())
	time.Sleep(100 * time.Millisecond)
	if n := logs.count("Bridge channel not open"); n != 1 {
		t.Errorf("expected one 'not open' log, got %d", n)
	}
}

func TestAudioSubscriptionMessage(t *testing.T) {
	for _, tc := range []struct {
		name  string
		prefs receivePrefs
		want  string // Empty: no message
	}{
		{"no cap", receivePrefs{audio: []string{"a-a0"}}, ""},
		{"cap", receivePrefs{capped: true, audio: []string{"a-a0", "b-a0"}},
			`{"colibriClass":"ReceiverAudioSubscription","mode":"Include","list":["a-a0","b-a0"]}`},
		{"cap, nothing received", receivePrefs{capped: true},
			`{"colibriClass":"ReceiverAudioSubscription","mode":"Include","list":[]}`},
		{"cap, unnamed source", receivePrefs{capped: true, audio: []string{"a-a0"}, audioUnnamed: true}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := audioSubscriptionMessage(tc.prefs)
			if tc.want == "" {
				if ok {
					t.Errorf("expected no audio message, got %s", got)
				}
				return
			}
			if !ok {
				t.Fatal("expected an audio message")
			}
			jsonEqual(t, got, tc.want)
		})
	}
}

// With a cap, both messages are sent on open; an unnamed audio source is
// logged once and sends no audio message.
func TestBridgeChannel_SendsAudioSubscription(t *testing.T) {
	b, c, logs := newTestBridgeChannel(t)
	prefs := receivePrefs{capped: true, audio: []string{"a-a0"}, video: []string{"a-v0"}}
	b.update(prefs)
	c.open(t)
	got := map[string]string{}
	for range 2 {
		m := expectSent(t, c)
		got[colibriClass(t, m)] = m
	}
	jsonEqual(t, got["ReceiverVideoConstraints"], videoConstraintsMessage(prefs))
	jsonEqual(t, got["ReceiverAudioSubscription"], `{"colibriClass":"ReceiverAudioSubscription","mode":"Include","list":["a-a0"]}`)

	// Only the audio changed: only the audio message is sent.
	prefs.audio = []string{"b-a0"}
	b.update(prefs)
	if m := expectSent(t, c); colibriClass(t, m) != "ReceiverAudioSubscription" {
		t.Errorf("expected only the audio subscription, got %s", m)
	}
	expectNothingSent(t, c)

	prefs.audioUnnamed = true
	b.update(prefs)
	b.update(prefs)
	expectNothingSent(t, c)
	if n := logs.count("A received audio source has no name"); n != 1 {
		t.Errorf("expected the unnamed audio source logged once, got %d", n)
	}
}

// Without a cap, no audio message is ever sent.
func TestBridgeChannel_NoAudioWithoutCap(t *testing.T) {
	b, c, _ := newTestBridgeChannel(t)
	b.update(receivePrefs{audio: []string{"a-a0"}})
	c.open(t)
	if m := expectSent(t, c); colibriClass(t, m) != "ReceiverVideoConstraints" {
		t.Fatalf("expected the video constraints, got %s", m)
	}
	b.update(receivePrefs{audio: []string{"a-a0", "b-a0"}})
	expectNothingSent(t, c)
}

func TestBridgeHeight(t *testing.T) {
	for _, tc := range []struct {
		height int
		layers []int
		want   int
	}{
		{360, nil, 360},
		{90, []int{90, 180, 360}, 180},
		{180, []int{90, 180, 360}, 360},
		{360, []int{90, 180, 360}, 720},
		{1080, []int{90, 180, 360}, 720},
		{45, []int{90, 180, 360}, 180},  // Below the lowest: the lowest layer
		{200, []int{90, 180, 360}, 360}, // Between layers: the one below
		{720, []int{180, 360, 720}, 720},
		{180, []int{180, 360, 720}, 180},
		{360, []int{180, 360}, 360}, // Not three layers: unchanged
	} {
		if got := bridgeHeight(tc.height, tc.layers); got != tc.want {
			t.Errorf("bridgeHeight(%d, %v) = %d, want %d", tc.height, tc.layers, got, tc.want)
		}
	}
}

// Endpoint statistics go out in lib-jitsi-meet's EndpointStats format,
// only while the channel is open; they are not kept for later.
func TestBridgeChannel_SendsEndpointStats(t *testing.T) {
	b, c, _ := newTestBridgeChannel(t)
	st := signaling.EndpointStats{
		UploadKbps: 2564, AudioUploadKbps: 64, VideoUploadKbps: 2500,
		UploadLoss: 3.46, TotalLoss: 3.46, Quality: 70, RTT: 12400 * time.Microsecond,
	}
	b.sendStats(st)
	expectNothingSent(t, c)
	c.open(t)
	expectSent(t, c) // Video constraints on open
	b.sendStats(st)
	jsonEqual(t, expectSent(t, c), `{"colibriClass":"EndpointStats",
		"bitrate":{"upload":2564,"download":0,"audio":{"upload":64,"download":0},"video":{"upload":2500,"download":0}},
		"packetLoss":{"upload":3,"download":0,"total":3},
		"connectionQuality":70,"jvbRTT":12,"serverRegion":null}`)
	for d, want := range map[time.Duration]int64{0: 0, 470 * time.Microsecond: 1, 12400 * time.Microsecond: 12, 12600 * time.Microsecond: 13} {
		if got := rttMillis(d); got != want {
			t.Errorf("rttMillis(%v) = %d, want %d", d, got, want)
		}
	}
	b.sendStats(st) // Periodic: sent again even if unchanged
	if m := expectSent(t, c); colibriClass(t, m) != "EndpointStats" {
		t.Fatalf("second stats not sent: %s", m)
	}
	c.close(t)
	b.sendStats(st)
	c.open(t)
	if m := expectSent(t, c); colibriClass(t, m) != "ReceiverVideoConstraints" {
		t.Fatalf("after reopen: %s, want only the constraints, not old stats", m)
	}
	expectNothingSent(t, c)
}
