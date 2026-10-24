package jitsi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func serveConfig(t *testing.T, tls bool, file string) *httptest.Server {
	t.Helper()
	body, err := os.ReadFile("testdata/configjs/" + file)
	if err != nil {
		t.Fatal(err)
	}
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/config.js") {
			http.NotFound(w, r)
			return
		}
		w.Write(body)
	})
	var s *httptest.Server
	if tls {
		s = httptest.NewTLSServer(h)
	} else {
		s = httptest.NewServer(h)
	}
	t.Cleanup(s.Close)
	return s
}

func TestFetchMeeting_DockerStyle(t *testing.T) {
	s := serveConfig(t, false, "docker.js")
	m, err := FetchMeeting(context.Background(), s.URL+"/IntriguingClimatesSupposeProbably", HTTPClient(false))
	if err != nil {
		t.Fatal(err)
	}
	want := Meeting{Room: "intriguingclimatessupposeprobably", Domain: "meet.jitsi", MUC: "muc.meet.jitsi",
		WebSocket: "wss://localhost:8443/xmpp-websocket"}
	if m != want {
		t.Errorf("got %+v\nwant %+v", m, want)
	}
}

func TestFetchMeeting_ObjectStyleWithTenantAndToken(t *testing.T) {
	s := serveConfig(t, false, "object.js")
	m, err := FetchMeeting(context.Background(), s.URL+"/acme/Room1?jwt=abc", HTTPClient(false))
	if err != nil {
		t.Fatal(err)
	}
	want := Meeting{Room: "room1", Tenant: "acme", Token: "abc", Domain: "meet.example.org",
		MUC: "conference.acme.meet.example.org", AnonymousDomain: "guest.meet.example.org",
		WebSocket: "wss://meet.example.org/acme/xmpp-websocket"}
	if m != want {
		t.Errorf("got %+v\nwant %+v", m, want)
	}
}

func TestFetchMeeting_RelativeWebSocket(t *testing.T) {
	s := serveConfig(t, false, "relative.js")
	m, err := FetchMeeting(context.Background(), s.URL+"/r", HTTPClient(false))
	if err != nil {
		t.Fatal(err)
	}
	if want := "ws://" + strings.TrimPrefix(s.URL, "http://") + "/xmpp-websocket"; m.WebSocket != want {
		t.Errorf("websocket %q, want %q", m.WebSocket, want)
	}
	if m.Domain != "relative.example" || m.MUC != "conference.relative.example" {
		t.Errorf("hosts %q %q", m.Domain, m.MUC)
	}
}

func TestFetchMeeting_NotJitsi(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("<html></html>")) }))
	defer s.Close()
	if _, err := FetchMeeting(context.Background(), s.URL+"/room", HTTPClient(false)); !errors.Is(err, ErrNotJitsi) {
		t.Errorf("err = %v, want ErrNotJitsi", err)
	}
	s404 := httptest.NewServer(http.NotFoundHandler())
	defer s404.Close()
	if _, err := FetchMeeting(context.Background(), s404.URL+"/room", HTTPClient(false)); !errors.Is(err, ErrNotJitsi) {
		t.Errorf("404: err = %v, want ErrNotJitsi", err)
	}
}

// TestFetchMeeting_TLS checks FR-005: a self-signed certificate is refused
// unless insecure, with a message naming the certificate.
func TestFetchMeeting_TLS(t *testing.T) {
	s := serveConfig(t, true, "docker.js")
	_, err := FetchMeeting(context.Background(), s.URL+"/room", HTTPClient(false))
	if err == nil || !strings.Contains(err.Error(), "certificate") {
		t.Fatalf("err = %v, want a certificate error", err)
	}
	if _, err := FetchMeeting(context.Background(), s.URL+"/room", HTTPClient(true)); err != nil {
		t.Fatalf("insecure: %v", err)
	}
}

func TestParseMeetingURL_Rejects(t *testing.T) {
	for _, u := range []string{"meet.jitsi/room", "https://host/", "https://host/a@b", "ftp://host/room"} {
		if _, _, err := ParseMeetingURL(u); err == nil {
			t.Errorf("%s: expected an error", u)
		}
	}
}

func TestEvalStringExpr(t *testing.T) {
	vars := map[string]string{"subdir": "t/"}
	for in, want := range map[string]string{
		`'a' + subdir + "b",`: "a" + "t/" + "b",
		`'x';`:                "x",
		"`y`\n":               "y",
	} {
		if got, ok := evalStringExpr(in, vars); !ok || got != want {
			t.Errorf("%q: got %q %v, want %q", in, got, ok, want)
		}
	}
	for _, in := range []string{`unknown + 'a'`, `'a'.toLowerCase()`, "`${x}`", ``} {
		if got, ok := evalStringExpr(in, vars); ok {
			t.Errorf("%q: got %q, want not evaluable", in, got)
		}
	}
}
