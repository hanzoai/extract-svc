package auth

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func makeJWKSServer(t *testing.T, key *rsa.PublicKey, kid string) *httptest.Server {
	t.Helper()
	n := base64.RawURLEncoding.EncodeToString(key.N.Bytes())
	// e = 65537 → "AQAB"
	eBytes := []byte{0x01, 0x00, 0x01}
	e := base64.RawURLEncoding.EncodeToString(eBytes)
	doc := map[string]any{
		"keys": []map[string]any{
			{"kty": "RSA", "kid": kid, "alg": "RS256", "n": n, "e": e},
		},
	}
	body, _ := json.Marshal(doc)
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
}

func signRS256(t *testing.T, priv *rsa.PrivateKey, kid string, claims map[string]any) string {
	t.Helper()
	header := map[string]any{"alg": "RS256", "typ": "JWT", "kid": kid}
	h, _ := json.Marshal(header)
	c, _ := json.Marshal(claims)
	signing := base64.RawURLEncoding.EncodeToString(h) + "." + base64.RawURLEncoding.EncodeToString(c)
	dig := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, priv, crypto.SHA256, dig[:])
	if err != nil {
		t.Fatal(err)
	}
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func TestVerifyHappy(t *testing.T) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	srv := makeJWKSServer(t, &priv.PublicKey, "k1")
	defer srv.Close()
	v := New(srv.URL)
	tok := signRS256(t, priv, "k1", map[string]any{
		"sub":   "u1",
		"email": "z@hanzo.ai",
		"exp":   time.Now().Add(time.Hour).Unix(),
	})
	c, err := v.Verify(tok)
	if err != nil {
		t.Fatal(err)
	}
	if c.Subject != "u1" || c.Email != "z@hanzo.ai" {
		t.Fatalf("claims=%+v", c)
	}
}

func TestVerifyExpired(t *testing.T) {
	priv, _ := rsa.GenerateKey(rand.Reader, 2048)
	srv := makeJWKSServer(t, &priv.PublicKey, "k1")
	defer srv.Close()
	v := New(srv.URL)
	tok := signRS256(t, priv, "k1", map[string]any{
		"sub": "u1",
		"exp": time.Now().Add(-time.Minute).Unix(),
	})
	_, err := v.Verify(tok)
	if err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expected expired, got %v", err)
	}
}

func TestVerifyTamperedSig(t *testing.T) {
	priv, _ := rsa.GenerateKey(rand.Reader, 2048)
	srv := makeJWKSServer(t, &priv.PublicKey, "k1")
	defer srv.Close()
	v := New(srv.URL)
	tok := signRS256(t, priv, "k1", map[string]any{"sub": "u1", "exp": time.Now().Add(time.Hour).Unix()})
	// flip a byte in the signature segment
	parts := strings.Split(tok, ".")
	sigBytes, _ := base64.RawURLEncoding.DecodeString(parts[2])
	sigBytes[0] ^= 0xFF
	parts[2] = base64.RawURLEncoding.EncodeToString(sigBytes)
	tampered := strings.Join(parts, ".")
	_, err := v.Verify(tampered)
	if err == nil {
		t.Fatal("expected sig error")
	}
}

func TestVerifyUnknownKid(t *testing.T) {
	priv, _ := rsa.GenerateKey(rand.Reader, 2048)
	srv := makeJWKSServer(t, &priv.PublicKey, "k1")
	defer srv.Close()
	v := New(srv.URL)
	tok := signRS256(t, priv, "k2", map[string]any{"sub": "u1", "exp": time.Now().Add(time.Hour).Unix()})
	_, err := v.Verify(tok)
	if err == nil {
		t.Fatal("expected unknown key")
	}
}

func TestVerifyRejectsHS256(t *testing.T) {
	v := New("http://127.0.0.1:0")
	// minimal HS256 token shell — should be rejected on alg before fetch.
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"x"}`))
	sig := base64.RawURLEncoding.EncodeToString([]byte("sig"))
	_, err := v.Verify(header + "." + payload + "." + sig)
	if err == nil {
		t.Fatal("expected alg rejection")
	}
}
