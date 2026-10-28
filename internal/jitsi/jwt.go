package jitsi

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/daimoc/jitsi-media-tools/internal/jitsi/jwt"
)

// jwtClockSkew backdates nbf so that a server clock slightly behind ours
// still accepts a freshly signed token.
const jwtClockSkew = 30 * time.Second

// jwtMode describes how the bots authenticate, for logging (the token and
// the secret are never logged).
func (c JingleConfig) jwtMode() string {
	switch {
	case c.JWT != "":
		return "static"
	case c.JWTAppID != "":
		return "generated"
	default:
		return "none"
	}
}

// tokenFor returns the JWT peerID presents to a token-authenticated Jitsi
// (Prosody mod_auth_token): the configured token, one signed for this peer,
// or "" when no token is configured.
func (c JingleConfig) tokenFor(peerID string) (string, error) {
	switch c.jwtMode() {
	case "static":
		return c.JWT, nil
	case "generated":
		return signJWT(c, peerID, time.Now())
	default:
		return "", nil
	}
}

type jwtUser struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Moderator bool   `json:"moderator"`
}

type jwtClaims struct {
	Issuer    string `json:"iss"`
	Audience  string `json:"aud"`
	Subject   string `json:"sub"`
	Room      string `json:"room"`
	IssuedAt  int64  `json:"iat"`
	NotBefore int64  `json:"nbf"`
	Expires   int64  `json:"exp"`
	Context   struct {
		User jwtUser `json:"user"`
	} `json:"context"`
}

// signJWT signs an HS256 token for peerID with the configured app id and
// secret, in the format Jitsi's token authentication expects. Each bot gets
// its own user id, so the bots appear as distinct users.
func signJWT(c JingleConfig, peerID string, now time.Time) (string, error) {
	name := peerID
	if len(name) > 8 {
		name = name[:8]
	}
	claims := jwtClaims{
		Issuer:    c.JWTAppID,
		Audience:  c.JWTAudience,
		Subject:   c.JWTSubject,
		Room:      strings.ToLower(c.RoomName),
		IssuedAt:  now.Unix(),
		NotBefore: now.Add(-jwtClockSkew).Unix(),
		Expires:   now.Add(c.jwtTTL).Unix(),
	}
	claims.Context.User = jwtUser{ID: peerID, Name: "bot-" + name, Moderator: c.JWTModerator}

	return jwt.Sign(claims, []byte(c.JWTAppSecret))
}

// websocketURL adds the room and the token, if any, to the WebSocket URL,
// as lib-jitsi-meet does. Deployments with several shards (meet.jit.si)
// route the connection to the room's shard by the room; without it, a
// guest lands on any shard and waits there for a host who is on another.
// Prosody reads the token from the URL; the client still authenticates
// with SASL ANONYMOUS.
func websocketURL(base, room, token string) (string, error) {
	u, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("invalid websocket_url: %w", err)
	}
	q := u.Query()
	q.Set("room", strings.ToLower(room))
	if token != "" {
		q.Set("token", token)
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// redactToken hides the token in a WebSocket URL for logging.
func redactToken(wsURL string) string {
	u, err := url.Parse(wsURL)
	if err != nil || !u.Query().Has("token") {
		return wsURL
	}
	q := u.Query()
	q.Set("token", "REDACTED")
	u.RawQuery = q.Encode()
	return u.String()
}
