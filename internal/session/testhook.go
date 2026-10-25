package session

import (
	"context"
	"log/slog"

	"github.com/daimoc/jitsi-media-tools/internal/jitsi"
)

// UseTestServer makes sessions started from now on use srv instead of a
// Jitsi server, and meeting instead of fetching config.js; restore undoes
// it. When srv has a RecordConfig(map[string]any) method, it gets each
// session's signaling configuration. For tests only (sessiontest).
func UseTestServer(srv SignalClient, meeting func(ctx context.Context, url string, insecure bool) (jitsi.Meeting, error)) (restore func()) {
	oldSignal, oldFetch := newSignal, fetchMeeting
	newSignal = func(c map[string]any, _ *slog.Logger) (SignalClient, error) {
		if r, ok := srv.(interface{ RecordConfig(map[string]any) }); ok {
			r.RecordConfig(c)
		}
		return srv, nil
	}
	fetchMeeting = meeting
	return func() { newSignal, fetchMeeting = oldSignal, oldFetch }
}
