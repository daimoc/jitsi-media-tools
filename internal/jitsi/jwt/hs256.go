// Package jwt signs the HS256 JSON Web Tokens the platform adapters present
// (Jitsi token authentication, LiveKit access tokens).
package jwt

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
)

// header is the JOSE header of every token: HS256, JWT.
const header = `{"alg":"HS256","typ":"JWT"}`

// Sign returns claims, encoded as JSON, signed with HMAC-SHA256 under
// secret, as a compact JWT (base64url without padding).
func Sign(claims any, secret []byte) (string, error) {
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("encode jwt claims: %w", err)
	}
	enc := base64.RawURLEncoding
	signed := enc.EncodeToString([]byte(header)) + "." + enc.EncodeToString(payload)
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(signed))
	return signed + "." + enc.EncodeToString(mac.Sum(nil)), nil
}
