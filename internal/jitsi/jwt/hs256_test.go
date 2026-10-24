package jwt

import "testing"

// The jwt.io example token: same header, claims and secret.
func TestSign_KnownVector(t *testing.T) {
	claims := struct {
		Sub  string `json:"sub"`
		Name string `json:"name"`
		Iat  int64  `json:"iat"`
	}{"1234567890", "John Doe", 1516239022}
	got, err := Sign(claims, []byte("your-256-bit-secret"))
	if err != nil {
		t.Fatal(err)
	}
	want := "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIiwibmFtZSI6IkpvaG4gRG9lIiwiaWF0IjoxNTE2MjM5MDIyfQ.SflKxwRJSMeKKF2QT4fwpMeJf36POk6yJV_adQssw5c"
	if got != want {
		t.Fatalf("Sign() = %s, want %s", got, want)
	}
}

func TestSign_BadClaims(t *testing.T) {
	if _, err := Sign(make(chan int), []byte("s")); err == nil {
		t.Fatal("Sign() of unencodable claims returned no error")
	}
}
