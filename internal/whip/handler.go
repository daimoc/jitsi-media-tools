// Package whip is the HTTP side of a WHIP endpoint (RFC 9725): it checks
// the bearer secret, reads the meeting and display name from the address,
// hands the SDP offer to a Publisher and keeps the resource of each stream
// until it is deleted or ends (research R14). It knows nothing about Jitsi
// or WebRTC.
package whip

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Address prefixes: a stream is published at publishPrefix + host/room and
// lives at resourcePrefix + id.
const (
	publishPrefix  = "/whip/"
	resourcePrefix = "/whip/session/"
	maxOffer       = 64 << 10 // Bytes of an SDP offer
)

// Request is a publisher asking to join a meeting.
type Request struct {
	MeetingURL string // https://host[:port]/room[?jwt=…]
	Name       string // Display name
	Offer      string // SDP
}

// Stream is a published stream, as the Publisher started it.
type Stream struct {
	Answer string
	End    func()          // Ends the stream; called once at most
	Done   <-chan struct{} // Closed once the stream has ended, for any reason
}

// Publisher starts streams. ctx bounds only the start, not the stream.
type Publisher interface {
	Publish(ctx context.Context, r Request) (Stream, error)
}

// Error is a Publisher error with the HTTP status to answer; other errors
// are answered 502, the meeting being upstream.
type Error struct {
	Status int
	Msg    string
}

func (e *Error) Error() string { return e.Msg }

// Info describes a running stream.
type Info struct {
	ID         string
	MeetingURL string // Without the token
	Name       string
	Started    time.Time
}

// Handler serves WHIP publishers.
type Handler struct {
	Secret     string // Bearer token of every request but OPTIONS
	MaxStreams int    // Streams at once, being started included
	Publisher  Publisher
	Logger     *slog.Logger

	mu       sync.Mutex
	streams  map[string]*entry
	starting int
	closed   bool
}

type entry struct {
	info Info
	end  func()
	once sync.Once
}

func (e *entry) stop() { e.once.Do(e.end) }

func (h *Handler) logger() *slog.Logger {
	if h.Logger == nil {
		return slog.Default()
	}
	return h.Logger
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	setCORS(w.Header())
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if !h.authorized(r) {
		h.logger().Warn("WHIP request refused: bad or missing token", slog.String("method", r.Method), slog.String("from", r.RemoteAddr))
		w.Header().Set("WWW-Authenticate", `Bearer realm="whip"`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	path := r.URL.EscapedPath()
	switch {
	case strings.HasPrefix(path, resourcePrefix) && r.Method != http.MethodPost:
		h.resource(w, r, strings.TrimPrefix(path, resourcePrefix))
	case !strings.HasPrefix(path, publishPrefix):
		http.NotFound(w, r)
	case r.Method != http.MethodPost:
		w.Header().Set("Allow", "POST, OPTIONS")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	default:
		h.publish(w, r, strings.TrimPrefix(path, publishPrefix))
	}
}

func setCORS(h http.Header) {
	h.Set("Access-Control-Allow-Origin", "*")
	h.Set("Access-Control-Allow-Methods", "POST, DELETE, PATCH, OPTIONS")
	h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
	h.Set("Access-Control-Expose-Headers", "Location")
}

// authorized compares the bearer token with the secret in constant time.
func (h *Handler) authorized(r *http.Request) bool {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	return ok && h.Secret != "" && subtle.ConstantTimeCompare([]byte(token), []byte(h.Secret)) == 1
}

func (h *Handler) publish(w http.ResponseWriter, r *http.Request, path string) {
	meeting, name, err := parseAddress(path, r.URL.Query())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if ct, _, _ := strings.Cut(r.Header.Get("Content-Type"), ";"); strings.TrimSpace(ct) != "application/sdp" {
		http.Error(w, "the offer must be application/sdp", http.StatusUnsupportedMediaType)
		return
	}
	offer, err := io.ReadAll(io.LimitReader(r.Body, maxOffer+1))
	switch {
	case err != nil:
		http.Error(w, "cannot read the offer", http.StatusBadRequest)
		return
	case len(offer) > maxOffer:
		http.Error(w, "the offer is too large", http.StatusRequestEntityTooLarge)
		return
	}
	if !h.reserve() {
		http.Error(w, "the gateway is full", http.StatusServiceUnavailable)
		return
	}
	shown := stripToken(meeting)
	log := h.logger().With(slog.String("meeting", shown), slog.String("name", name))
	log.Info("WHIP stream starting")
	st, err := h.Publisher.Publish(r.Context(), Request{MeetingURL: meeting, Name: name, Offer: string(offer)})
	if err != nil {
		h.release()
		status := http.StatusBadGateway
		var e *Error
		if errors.As(err, &e) {
			status = e.Status
		}
		log.Warn("WHIP stream refused", slog.String("error", err.Error()))
		http.Error(w, err.Error(), status)
		return
	}
	id := newID()
	e := &entry{info: Info{ID: id, MeetingURL: shown, Name: name, Started: time.Now()}, end: st.End}
	if !h.add(e) {
		e.stop() // Closed while starting
		http.Error(w, "the gateway is stopping", http.StatusServiceUnavailable)
		return
	}
	go func() {
		<-st.Done
		h.remove(id)
		log.Info("WHIP stream ended", slog.String("id", id))
	}()
	log.Info("WHIP stream started", slog.String("id", id))
	w.Header().Set("Content-Type", "application/sdp")
	w.Header().Set("Location", resourcePrefix+id)
	w.WriteHeader(http.StatusCreated)
	io.WriteString(w, st.Answer)
}

func (h *Handler) resource(w http.ResponseWriter, r *http.Request, id string) {
	h.mu.Lock()
	e := h.streams[id]
	h.mu.Unlock()
	if e == nil {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodDelete:
		h.remove(id)
		e.stop()
		w.WriteHeader(http.StatusOK)
	default: // PATCH (trickle ICE, ICE restart): the answer is complete
		w.Header().Set("Allow", "DELETE, OPTIONS")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// reserve takes a place for a stream being started.
func (h *Handler) reserve() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed || h.MaxStreams > 0 && len(h.streams)+h.starting >= h.MaxStreams {
		return false
	}
	h.starting++
	return true
}

func (h *Handler) release() {
	h.mu.Lock()
	h.starting--
	h.mu.Unlock()
}

// add turns a reserved place into a running stream.
func (h *Handler) add(e *entry) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.starting--
	if h.closed {
		return false
	}
	if h.streams == nil {
		h.streams = map[string]*entry{}
	}
	h.streams[e.info.ID] = e
	return true
}

func (h *Handler) remove(id string) {
	h.mu.Lock()
	delete(h.streams, id)
	h.mu.Unlock()
}

// Streams returns the running streams.
func (h *Handler) Streams() []Info {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]Info, 0, len(h.streams))
	for _, e := range h.streams {
		out = append(out, e.info)
	}
	return out
}

// Close ends every stream and refuses new ones.
func (h *Handler) Close() {
	h.mu.Lock()
	h.closed = true
	all := h.streams
	h.streams = nil
	h.mu.Unlock()
	for _, e := range all {
		e.stop()
	}
}

// parseAddress reads host[:port]/room (escaped) and the query of a publish
// address into a meeting URL and a display name.
func parseAddress(path string, q url.Values) (meeting, name string, err error) {
	host, room, _ := strings.Cut(path, "/")
	if room == "" {
		return "", "", errors.New("the address needs a server and a room: /whip/<server>/<room>?name=<name>")
	}
	if !validHost(host) {
		return "", "", errors.New("the server is not a host name or address")
	}
	if name = strings.TrimSpace(q.Get("name")); name == "" {
		return "", "", errors.New("the address needs a display name: ?name=<name>")
	}
	meeting = "https://" + host + "/" + room
	if jwt := q.Get("jwt"); jwt != "" {
		meeting += "?jwt=" + url.QueryEscape(jwt)
	}
	return meeting, name, nil
}

func validHost(hostport string) bool {
	host, port := hostport, ""
	if h, p, err := net.SplitHostPort(hostport); err == nil {
		host, port = h, p
	} else if strings.Contains(hostport, ":") && !strings.HasPrefix(hostport, "[") {
		return false // A colon without a valid port
	}
	if port != "" {
		if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
			return false
		}
	}
	if net.ParseIP(strings.Trim(host, "[]")) != nil {
		return true
	}
	if host == "" || len(host) > 253 {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 {
			return false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}

// stripToken drops the query (the token) from a meeting URL, for logs.
func stripToken(meeting string) string {
	m, _, _ := strings.Cut(meeting, "?")
	return m
}

func newID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}
