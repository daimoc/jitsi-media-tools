package whip

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

const secret = "s3cret"

// fakePublisher records requests and returns streams it can end.
type fakePublisher struct {
	mu      sync.Mutex
	reqs    []Request
	err     error
	endErr  error // Why the streams end by themselves
	streams []*fakeStream
}

type fakeStream struct {
	ended chan struct{}
	once  sync.Once
}

func (s *fakeStream) end() { s.once.Do(func() { close(s.ended) }) }

func (p *fakePublisher) Publish(_ context.Context, r Request) (Stream, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.reqs = append(p.reqs, r)
	if p.err != nil {
		return Stream{}, p.err
	}
	s := &fakeStream{ended: make(chan struct{})}
	p.streams = append(p.streams, s)
	endErr := p.endErr
	return Stream{Answer: "v=0 answer\r\n", End: s.end, Done: s.ended, Err: func() error { return endErr }}, nil
}

func (p *fakePublisher) requests() []Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]Request(nil), p.reqs...)
}

// lockedBuffer is a log written by the handler's goroutines and read by
// the test.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func newTestHandler(t *testing.T, pub Publisher, max int) (*Handler, *lockedBuffer) {
	t.Helper()
	logs := &lockedBuffer{}
	h := &Handler{Secret: secret, MaxStreams: max, Publisher: pub,
		Logger: slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))}
	t.Cleanup(h.Close)
	return h, logs
}

func do(h http.Handler, method, target, token, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	if method == http.MethodPost {
		r.Header.Set("Content-Type", "application/sdp")
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestHandler_PublishAddress(t *testing.T) {
	for _, c := range []struct{ target, meeting, name string }{
		{"/whip/meet.example.org:8443/My%20Room?name=OBS&jwt=T", "https://meet.example.org:8443/My%20Room?jwt=T", "OBS"},
		{"/whip/meet.example.org/acme/Standup?name=Studio%20A", "https://meet.example.org/acme/Standup", "Studio A"},
	} {
		pub := &fakePublisher{}
		h, _ := newTestHandler(t, pub, 10)
		w := do(h, http.MethodPost, c.target, secret, "v=0 offer\r\n")
		if w.Code != http.StatusCreated {
			t.Fatalf("%s: status %d %q, want 201", c.target, w.Code, w.Body)
		}
		reqs := pub.requests()
		if len(reqs) != 1 || reqs[0].MeetingURL != c.meeting || reqs[0].Name != c.name || reqs[0].Offer != "v=0 offer\r\n" {
			t.Errorf("%s: publisher got %+v, want meeting %q, name %q", c.target, reqs, c.meeting, c.name)
		}
		if ct := w.Header().Get("Content-Type"); ct != "application/sdp" {
			t.Errorf("content type %q", ct)
		}
		if w.Body.String() != "v=0 answer\r\n" {
			t.Errorf("body %q, want the answer", w.Body)
		}
		loc := w.Header().Get("Location")
		if !strings.HasPrefix(loc, "/whip/session/") || len(loc) != len("/whip/session/")+32 {
			t.Errorf("Location %q, want /whip/session/<32 hex>", loc)
		}
		if st := h.Streams(); len(st) != 1 || st[0].ID != strings.TrimPrefix(loc, "/whip/session/") || st[0].MeetingURL != strings.Split(c.meeting, "?")[0] || st[0].Name != c.name {
			t.Errorf("Streams() = %+v, want the stream without its token", st)
		}
	}
}

func TestHandler_BadAddress(t *testing.T) {
	for _, target := range []string{
		"/whip/meet.example.org/Room",          // No name
		"/whip/meet.example.org/Room?name=",    // Empty name
		"/whip/meet.example.org?name=OBS",      // No room
		"/whip/meet.example.org/?name=OBS",     // Empty room
		"/whip/user@evil.org/Room?name=OBS",    // Not a host
		"/whip/meet.example.org:x/Room?name=O", // Bad port
		"/other/meet.example.org/Room?name=OBS",
	} {
		pub := &fakePublisher{}
		h, _ := newTestHandler(t, pub, 10)
		w := do(h, http.MethodPost, target, secret, "v=0\r\n")
		if w.Code != http.StatusBadRequest && w.Code != http.StatusNotFound {
			t.Errorf("%s: status %d, want 400 or 404", target, w.Code)
		}
		if len(pub.requests()) != 0 {
			t.Errorf("%s: publisher called", target)
		}
	}
}

func TestHandler_Auth(t *testing.T) {
	for _, token := range []string{"", "wrong", secret + "x"} {
		pub := &fakePublisher{}
		h, _ := newTestHandler(t, pub, 10)
		w := do(h, http.MethodPost, "/whip/meet.example.org/Room?name=OBS", token, "v=0\r\n")
		if w.Code != http.StatusUnauthorized {
			t.Errorf("token %q: status %d, want 401", token, w.Code)
		}
		if !strings.HasPrefix(w.Header().Get("WWW-Authenticate"), "Bearer") {
			t.Errorf("token %q: no WWW-Authenticate: Bearer", token)
		}
		if len(pub.requests()) != 0 {
			t.Errorf("token %q: publisher called", token)
		}
	}
}

func TestHandler_DeleteEndsStream(t *testing.T) {
	pub := &fakePublisher{}
	h, _ := newTestHandler(t, pub, 10)
	loc := do(h, http.MethodPost, "/whip/meet.example.org/Room?name=OBS", secret, "v=0\r\n").Header().Get("Location")
	if w := do(h, http.MethodDelete, loc, "", ""); w.Code != http.StatusUnauthorized {
		t.Errorf("DELETE without token: status %d, want 401", w.Code)
	}
	if w := do(h, http.MethodDelete, loc, secret, ""); w.Code != http.StatusOK {
		t.Fatalf("DELETE: status %d, want 200", w.Code)
	}
	select {
	case <-pub.streams[0].ended:
	default:
		t.Error("DELETE did not end the stream")
	}
	if w := do(h, http.MethodDelete, loc, secret, ""); w.Code != http.StatusNotFound {
		t.Errorf("second DELETE: status %d, want 404", w.Code)
	}
	if w := do(h, http.MethodPatch, loc, secret, ""); w.Code != http.StatusNotFound && w.Code != http.StatusMethodNotAllowed {
		t.Errorf("PATCH on an ended stream: status %d", w.Code)
	}
}

func TestHandler_PatchNotSupported(t *testing.T) {
	pub := &fakePublisher{}
	h, _ := newTestHandler(t, pub, 10)
	loc := do(h, http.MethodPost, "/whip/meet.example.org/Room?name=OBS", secret, "v=0\r\n").Header().Get("Location")
	if w := do(h, http.MethodPatch, loc, secret, "a=candidate"); w.Code != http.StatusMethodNotAllowed {
		t.Errorf("PATCH: status %d, want 405", w.Code)
	}
}

func TestHandler_MaxStreams(t *testing.T) {
	pub := &fakePublisher{}
	h, _ := newTestHandler(t, pub, 2)
	post := func() int {
		return do(h, http.MethodPost, "/whip/meet.example.org/Room?name=OBS", secret, "v=0\r\n").Code
	}
	if a, b := post(), post(); a != http.StatusCreated || b != http.StatusCreated {
		t.Fatalf("first two: %d %d, want 201", a, b)
	}
	if c := post(); c != http.StatusServiceUnavailable {
		t.Errorf("third: %d, want 503", c)
	}
	// A stream that ends by itself frees its place.
	pub.streams[0].end()
	for deadline := time.Now().Add(time.Second); len(h.Streams()) != 1; time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("%d streams after one ended, want 1", len(h.Streams()))
		}
	}
	if c := post(); c != http.StatusCreated {
		t.Errorf("after one ended: %d, want 201", c)
	}
}

func TestHandler_PublisherError(t *testing.T) {
	pub := &fakePublisher{err: &Error{Status: http.StatusBadRequest, Msg: "the offer has no H.264 video"}}
	h, _ := newTestHandler(t, pub, 10)
	w := do(h, http.MethodPost, "/whip/meet.example.org/Room?name=OBS", secret, "v=0\r\n")
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "H.264") {
		t.Errorf("status %d %q, want 400 naming H.264", w.Code, w.Body)
	}
	pub.err = errors.New("joining failed: authentication failed")
	w = do(h, http.MethodPost, "/whip/meet.example.org/Room?name=OBS", secret, "v=0\r\n")
	if w.Code != http.StatusBadGateway || !strings.Contains(w.Body.String(), "authentication failed") {
		t.Errorf("status %d %q, want 502 with the reason", w.Code, w.Body)
	}
}

func TestHandler_RequestChecks(t *testing.T) {
	pub := &fakePublisher{}
	h, _ := newTestHandler(t, pub, 10)
	r := httptest.NewRequest(http.MethodPost, "/whip/meet.example.org/Room?name=OBS", strings.NewReader("v=0\r\n"))
	r.Header.Set("Authorization", "Bearer "+secret)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusUnsupportedMediaType {
		t.Errorf("JSON body: status %d, want 415", w.Code)
	}
	if w := do(h, http.MethodPost, "/whip/meet.example.org/Room?name=OBS", secret, strings.Repeat("a", maxOffer+1)); w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("large offer: status %d, want 413", w.Code)
	}
	if w := do(h, http.MethodGet, "/whip/meet.example.org/Room?name=OBS", secret, ""); w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET: status %d, want 405", w.Code)
	}
	if len(pub.requests()) != 0 {
		t.Error("publisher called")
	}
}

func TestHandler_Options(t *testing.T) {
	h, _ := newTestHandler(t, &fakePublisher{}, 10)
	w := do(h, http.MethodOptions, "/whip/meet.example.org/Room?name=OBS", "", "")
	if w.Code != http.StatusNoContent {
		t.Errorf("OPTIONS: status %d, want 204", w.Code)
	}
	for k, want := range map[string]string{
		"Access-Control-Allow-Origin":   "*",
		"Access-Control-Allow-Methods":  "POST",
		"Access-Control-Allow-Headers":  "Authorization",
		"Access-Control-Expose-Headers": "Location",
	} {
		if !strings.Contains(w.Header().Get(k), want) {
			t.Errorf("%s = %q, want it to contain %q", k, w.Header().Get(k), want)
		}
	}
}

func TestHandler_LogsNoSecrets(t *testing.T) {
	pub := &fakePublisher{}
	h, logs := newTestHandler(t, pub, 10)
	loc := do(h, http.MethodPost, "/whip/meet.example.org/Room?name=OBS&jwt=TOKEN123", secret, "v=0\r\n").Header().Get("Location")
	do(h, http.MethodPost, "/whip/meet.example.org/Room?name=OBS", "wrong-guess", "v=0\r\n")
	do(h, http.MethodDelete, loc, secret, "")
	out := logs.String()
	if !strings.Contains(out, "meet.example.org/Room") {
		t.Errorf("logs lack the meeting:\n%s", out)
	}
	for _, s := range []string{secret, "TOKEN123", "wrong-guess"} {
		if strings.Contains(out, s) {
			t.Errorf("logs contain %q:\n%s", s, out)
		}
	}
}

func TestHandler_CloseEndsAll(t *testing.T) {
	pub := &fakePublisher{}
	h, _ := newTestHandler(t, pub, 10)
	do(h, http.MethodPost, "/whip/meet.example.org/A?name=OBS", secret, "v=0\r\n")
	do(h, http.MethodPost, "/whip/meet.example.org/B?name=OBS", secret, "v=0\r\n")
	h.Close()
	for i, s := range pub.streams {
		select {
		case <-s.ended:
		default:
			t.Errorf("stream %d not ended", i)
		}
	}
	if w := do(h, http.MethodPost, "/whip/meet.example.org/C?name=OBS", secret, "v=0\r\n"); w.Code != http.StatusServiceUnavailable {
		t.Errorf("POST after Close: status %d, want 503", w.Code)
	}
}

func TestHandler_Events(t *testing.T) {
	pub := &fakePublisher{}
	h, _ := newTestHandler(t, pub, 10)
	evs := make(chan Event, 10)
	h.OnEvent = func(e Event) { evs <- e }
	next := func() Event {
		t.Helper()
		select {
		case e := <-evs:
			return e
		case <-time.After(time.Second):
			t.Fatal("no event")
			return Event{}
		}
	}
	loc := do(h, http.MethodPost, "/whip/meet.example.org/Room?name=OBS&jwt=T", secret, "v=0\r\n").Header().Get("Location")
	if e := next(); e.Kind != StreamStarted || e.Stream.ID != strings.TrimPrefix(loc, resourcePrefix) || e.Stream.MeetingURL != "https://meet.example.org/Room" || e.Stream.Name != "OBS" {
		t.Errorf("first event %+v, want started", e)
	}
	do(h, http.MethodDelete, loc, secret, "")
	if e := next(); e.Kind != StreamEnded || e.Err != nil {
		t.Errorf("after DELETE: %+v, want ended", e)
	}

	pub.err = errors.New("joining failed")
	do(h, http.MethodPost, "/whip/meet.example.org/Room?name=OBS", secret, "v=0\r\n")
	if e := next(); e.Kind != StreamFailed || e.Err == nil || e.Stream.ID != "" {
		t.Errorf("refused: %+v, want failed without id", e)
	}

	pub.err = nil
	pub.endErr = errors.New("no media from the publisher")
	do(h, http.MethodPost, "/whip/meet.example.org/Room?name=OBS", secret, "v=0\r\n")
	next() // Started
	pub.streams[len(pub.streams)-1].end()
	if e := next(); e.Kind != StreamFailed || e.Err == nil || !strings.Contains(e.Err.Error(), "no media") {
		t.Errorf("ended by itself with an error: %+v, want failed", e)
	}
}
