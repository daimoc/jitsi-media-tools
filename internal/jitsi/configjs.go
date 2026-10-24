package jitsi

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// ErrNotJitsi is returned when a meeting URL's server serves no Jitsi Meet
// config.js.
var ErrNotJitsi = errors.New("not a Jitsi Meet server")

// Meeting is what a meeting URL and its server's config.js tell about how to
// join (research R12). Empty fields were not found.
type Meeting struct {
	Room            string // Lower-cased room name
	Tenant          string // Multi-tenant prefix ("" without)
	Token           string // jwt URL parameter
	Domain          string // hosts.domain
	MUC             string // hosts.muc
	AnonymousDomain string // hosts.anonymousdomain (guests on secure-domain servers)
	Focus           string // hosts.focus (focus component JID)
	WebSocket       string // Absolute wss:// URL
}

// ParseMeetingURL splits https://host[/tenant]/Room[?jwt=...] into its
// parts, without contacting the server.
func ParseMeetingURL(meetingURL string) (*url.URL, Meeting, error) {
	u, err := url.Parse(meetingURL)
	if err != nil {
		return nil, Meeting{}, fmt.Errorf("meeting URL: %w", err)
	}
	if u.Scheme != "https" && u.Scheme != "http" || u.Host == "" {
		return nil, Meeting{}, fmt.Errorf("meeting URL %q: want https://host/Room", meetingURL)
	}
	segs := strings.Split(strings.Trim(u.Path, "/"), "/")
	room := segs[len(segs)-1]
	if room == "" || strings.ContainsAny(room, "@/") {
		return nil, Meeting{}, fmt.Errorf("meeting URL %q: no valid room name", meetingURL)
	}
	m := Meeting{
		Room:   strings.ToLower(room),
		Tenant: strings.Join(segs[:len(segs)-1], "/"),
		Token:  u.Query().Get("jwt"),
	}
	return u, m, nil
}

// configTimeout bounds the config.js request.
const configTimeout = 15 * time.Second

// HTTPClient returns the client used for config.js, verifying certificates
// unless insecure.
func HTTPClient(insecure bool) *http.Client {
	t := http.DefaultTransport.(*http.Transport).Clone()
	if insecure {
		t.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // Asked for: self-signed test servers
	}
	return &http.Client{Transport: t} // No Timeout: the WebSocket dial refuses one; requests use their context
}

// FetchMeeting parses the meeting URL and reads the server's config.js
// (/<tenant>/config.js?room=...).
func FetchMeeting(ctx context.Context, meetingURL string, client *http.Client) (Meeting, error) {
	u, m, err := ParseMeetingURL(meetingURL)
	if err != nil {
		return Meeting{}, err
	}
	prefix := "/"
	if m.Tenant != "" {
		prefix += m.Tenant + "/"
	}
	ctx, cancel := context.WithTimeout(ctx, configTimeout)
	defer cancel()
	cfgURL := url.URL{Scheme: u.Scheme, Host: u.Host, Path: prefix + "config.js", RawQuery: "room=" + url.QueryEscape(m.Room)}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cfgURL.String(), nil)
	if err != nil {
		return Meeting{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		if strings.Contains(err.Error(), "certificate") {
			return Meeting{}, fmt.Errorf("%s: %w (use the insecure option for a self-signed certificate)", cfgURL.Host, err)
		}
		return Meeting{}, fmt.Errorf("%w: %v", ErrServerUnreachable, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return Meeting{}, fmt.Errorf("%w: reading %s: %v", ErrServerUnreachable, cfgURL.String(), err)
	}
	if resp.StatusCode != http.StatusOK {
		return Meeting{}, fmt.Errorf("%w: %s answered %s", ErrNotJitsi, cfgURL.String(), resp.Status)
	}
	vars := map[string]string{"location.host": u.Host, "window.location.host": u.Host, "subdomain": "", "subdir": ""}
	if m.Tenant != "" {
		vars["subdomain"] = m.Tenant + "."
		vars["subdir"] = m.Tenant + "/"
	}
	values := parseConfigJS(string(body), vars)
	if values["domain"] == "" {
		return Meeting{}, fmt.Errorf("%w: %s has no hosts.domain", ErrNotJitsi, cfgURL.String())
	}
	m.Domain = values["domain"]
	m.MUC = values["muc"]
	m.AnonymousDomain = values["anonymousdomain"]
	m.Focus = values["focus"]
	if ws := values["websocket"]; ws != "" {
		m.WebSocket = absoluteWebSocket(u, ws)
	}
	if m.WebSocket == "" {
		m.WebSocket = absoluteWebSocket(u, prefix+"xmpp-websocket")
	}
	return m, nil
}

// absoluteWebSocket resolves a websocket setting (absolute, scheme-relative
// or path) against the meeting URL, with a ws/wss scheme.
func absoluteWebSocket(base *url.URL, ws string) string {
	ref, err := url.Parse(ws)
	if err != nil {
		return ""
	}
	abs := base.ResolveReference(ref)
	switch abs.Scheme {
	case "https":
		abs.Scheme = "wss"
	case "http":
		abs.Scheme = "ws"
	}
	abs.RawQuery, abs.Fragment = "", ""
	return abs.String()
}

// configKeys are the config.js settings read, as written in both styles:
// "hosts: { domain: '...' }" and "config.hosts.domain = '...';".
var configKeys = []string{"domain", "muc", "anonymousdomain", "focus", "websocket", "focusUserJid"}

var (
	blockComment = regexp.MustCompile(`(?s)/\*.*?\*/`)
	lineComment  = regexp.MustCompile(`(?m)^\s*//.*$`)
)

// parseConfigJS extracts configKeys from config.js source. Values are
// string expressions: quoted literals joined with "+", and the identifiers
// in vars. A key assigned several times keeps its last value that could be
// evaluated.
func parseConfigJS(src string, vars map[string]string) map[string]string {
	src = blockComment.ReplaceAllString(src, "")
	src = lineComment.ReplaceAllString(src, "")
	out := map[string]string{}
	for _, key := range configKeys {
		re := regexp.MustCompile(`(?:^|[^A-Za-z0-9_$])` + key + `\s*[:=]\s*`)
		for _, loc := range re.FindAllStringIndex(src, -1) {
			if loc[1] < len(src) && src[loc[1]] == '=' {
				continue // "==" comparison
			}
			if v, ok := evalStringExpr(src[loc[1]:], vars); ok {
				out[key] = v
			}
		}
	}
	return out
}

// evalStringExpr evaluates the string expression at the start of s, up to
// the first ",", ";", "}" or end of line outside quotes. It reports false if
// a term is neither a literal nor a known identifier.
func evalStringExpr(s string, vars map[string]string) (string, bool) {
	var (
		sb    strings.Builder
		terms int
	)
	i := 0
	for {
		for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
			i++
		}
		if i >= len(s) {
			break
		}
		switch c := s[i]; {
		case c == '\'' || c == '"' || c == '`':
			j := strings.IndexByte(s[i+1:], c)
			if j < 0 {
				return "", false
			}
			lit := s[i+1 : i+1+j]
			if c == '`' && strings.Contains(lit, "${") {
				return "", false
			}
			sb.WriteString(lit)
			i += j + 2
		default:
			j := i
			for j < len(s) && (isIdentChar(s[j]) || s[j] == '.') {
				j++
			}
			v, ok := vars[s[i:j]]
			if j == i || !ok {
				return "", false
			}
			sb.WriteString(v)
			i = j
		}
		terms++
		for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
			i++
		}
		if i < len(s) && s[i] == '+' {
			i++
			continue
		}
		break
	}
	if terms == 0 {
		return "", false
	}
	if i < len(s) && !strings.ContainsRune(",;}\r\n/", rune(s[i])) {
		return "", false // More expression than we understand
	}
	return sb.String(), true
}

func isIdentChar(c byte) bool {
	return c == '_' || c == '$' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}
