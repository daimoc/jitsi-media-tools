package session

import (
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/daimoc/jitsi-media-tools/internal/session/sessiontest"
)

var quietLogger = slog.New(slog.NewTextHandler(io.Discard, nil))

// fakeSignal stands in for the Jitsi adapter (sessiontest.Signal).
type fakeSignal = sessiontest.Signal

// useFake installs f as the signaling of sessions started by the test, with
// a config.js answer for any meeting URL.
func useFake(t *testing.T, f *fakeSignal) {
	t.Helper()
	f.T = t
	t.Cleanup(UseTestServer(f, sessiontest.FetchMeeting))
}

var errFake = errors.New("fake failure")
