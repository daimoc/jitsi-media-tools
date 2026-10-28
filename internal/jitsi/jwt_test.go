package jitsi

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func jwtTestConfig(t *testing.T, overrides map[string]any) JingleConfig {
	t.Helper()
	cfg := map[string]any{
		"xmpp_domain":    testDomain,
		"room_name":      "LoadRoom",
		"jwt_app_id":     "my_app",
		"jwt_app_secret": "s3cret",
	}
	for k, v := range overrides {
		cfg[k] = v
	}
	a := NewAdapter(quietLogger)
	if err := a.ParseConfig(cfg); err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	return a.config
}

// decodeJWT checks the HS256 signature with secret and returns the claims.
func decodeJWT(t *testing.T, token, secret string) map[string]any {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("token must have 3 parts, got %d", len(parts))
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(parts[0] + "." + parts[1]))
	if got := base64.RawURLEncoding.EncodeToString(mac.Sum(nil)); got != parts[2] {
		t.Fatal("bad signature")
	}
	var header map[string]any
	raw, _ := base64.RawURLEncoding.DecodeString(parts[0])
	if err := json.Unmarshal(raw, &header); err != nil || header["alg"] != "HS256" || header["typ"] != "JWT" {
		t.Fatalf("unexpected header %s (%v)", raw, err)
	}
	var claims map[string]any
	raw, _ = base64.RawURLEncoding.DecodeString(parts[1])
	if err := json.Unmarshal(raw, &claims); err != nil {
		t.Fatalf("decode claims: %v", err)
	}
	return claims
}

func TestSignJWT_Claims(t *testing.T) {
	cfg := jwtTestConfig(t, map[string]any{"jwt_ttl": "2h", "jwt_moderator": true})
	now := time.Unix(1_700_000_000, 0)
	token, err := signJWT(cfg, "0123456789abcdef", now)
	if err != nil {
		t.Fatal(err)
	}
	c := decodeJWT(t, token, "s3cret")
	if c["iss"] != "my_app" || c["aud"] != "jitsi" || c["sub"] != testDomain || c["room"] != "loadroom" {
		t.Errorf("unexpected claims %v", c)
	}
	if c["iat"] != float64(now.Unix()) || c["exp"] != float64(now.Add(2*time.Hour).Unix()) || c["nbf"].(float64) > float64(now.Unix()) {
		t.Errorf("unexpected times %v", c)
	}
	user := c["context"].(map[string]any)["user"].(map[string]any)
	if user["id"] != "0123456789abcdef" || user["name"] != "bot-01234567" || user["moderator"] != true {
		t.Errorf("unexpected user %v", user)
	}
}

func TestSignJWT_Overrides(t *testing.T) {
	cfg := jwtTestConfig(t, map[string]any{"jwt_audience": "aud1", "jwt_subject": "*"})
	token, err := signJWT(cfg, "p1", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	c := decodeJWT(t, token, "s3cret")
	user := c["context"].(map[string]any)["user"].(map[string]any)
	if c["aud"] != "aud1" || c["sub"] != "*" || user["name"] != "bot-p1" || user["moderator"] != false {
		t.Errorf("unexpected claims %v", c)
	}
}

func TestTokenFor(t *testing.T) {
	static := jwtTestConfig(t, map[string]any{"jwt_app_id": "", "jwt_app_secret": "", "jwt": "abc.def.ghi"})
	if tok, err := static.tokenFor("p1"); err != nil || tok != "abc.def.ghi" {
		t.Errorf("static token: got %q, %v", tok, err)
	}
	none := jwtTestConfig(t, map[string]any{"jwt_app_id": "", "jwt_app_secret": ""})
	if tok, err := none.tokenFor("p1"); err != nil || tok != "" {
		t.Errorf("no token: got %q, %v", tok, err)
	}
	gen := jwtTestConfig(t, nil)
	t1, _ := gen.tokenFor("peer-a")
	t2, _ := gen.tokenFor("peer-b")
	u1 := decodeJWT(t, t1, "s3cret")["context"].(map[string]any)["user"].(map[string]any)["id"]
	u2 := decodeJWT(t, t2, "s3cret")["context"].(map[string]any)["user"].(map[string]any)["id"]
	if u1 != "peer-a" || u2 != "peer-b" {
		t.Errorf("each bot must get its own identity, got %v and %v", u1, u2)
	}
}

func TestWebsocketURL(t *testing.T) {
	for _, tc := range []struct{ base, room, token, want string }{
		// The room routes the connection to the room's shard (meet.jit.si).
		{"wss://x/xmpp-websocket", "Room", "", "wss://x/xmpp-websocket?room=room"},
		{"wss://x/xmpp-websocket", "Room", "t.o.k", "wss://x/xmpp-websocket?room=room&token=t.o.k"},
		{"wss://x/xmpp-websocket?a=1", "r", "tok", "wss://x/xmpp-websocket?a=1&room=r&token=tok"},
	} {
		got, err := websocketURL(tc.base, tc.room, tc.token)
		if err != nil || got != tc.want {
			t.Errorf("websocketURL(%q, %q, %q) = %q, %v; want %q", tc.base, tc.room, tc.token, got, err, tc.want)
		}
	}
}

func TestParseConfig_RejectsBadJWT(t *testing.T) {
	base := func(kv map[string]any) map[string]any {
		cfg := map[string]any{"xmpp_domain": testDomain, "room_name": "room"}
		for k, v := range kv {
			cfg[k] = v
		}
		return cfg
	}
	for name, cfg := range map[string]map[string]any{
		"static and app secret": base(map[string]any{"jwt": "a.b.c", "jwt_app_id": "id", "jwt_app_secret": "s"}),
		"app id without secret": base(map[string]any{"jwt_app_id": "id"}),
		"secret without app id": base(map[string]any{"jwt_app_secret": "s"}),
		"bad jwt_ttl":           base(map[string]any{"jwt_app_id": "id", "jwt_app_secret": "s", "jwt_ttl": "forever"}),
	} {
		if err := NewAdapter(quietLogger).ParseConfig(cfg); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestAdapter_SendsTokenInWebSocketURL(t *testing.T) {
	srv := newFakeXMPPServer(t)
	a := newTestAdapter(t, srv, map[string]any{"jwt": "abc.def.ghi"})
	c := connectPeer(t, srv, a, "peer1", newPionHandler(t))
	if c.query.Get("token") != "abc.def.ghi" || c.query.Get("room") != "room" {
		t.Errorf("unexpected WebSocket query %v", c.query)
	}
	a.Disconnect("peer1")
}

func TestAdapter_NoTokenWithoutJWTConfig(t *testing.T) {
	srv := newFakeXMPPServer(t)
	a := newTestAdapter(t, srv, nil)
	c := connectPeer(t, srv, a, "peer1", newPionHandler(t))
	if c.query.Has("token") || c.query.Get("room") != "room" {
		t.Errorf("expected the room and no token, got %v", c.query)
	}
	a.Disconnect("peer1")
}
