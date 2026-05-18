package server

import (
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func setupTestServer(t *testing.T) (*Server, *rsa.PrivateKey, func()) {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	jwks := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := base64.RawURLEncoding.EncodeToString(priv.PublicKey.N.Bytes())
		e := base64.RawURLEncoding.EncodeToString([]byte{0x01, 0x00, 0x01})
		body := map[string]any{"keys": []map[string]any{{"kty": "RSA", "kid": "k1", "alg": "RS256", "n": n, "e": e}}}
		_ = json.NewEncoder(w).Encode(body)
	}))
	s := New(Config{Version: "test", JWKSURL: jwks.URL})
	return s, priv, jwks.Close
}

func mintToken(t *testing.T, priv *rsa.PrivateKey) string {
	t.Helper()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT","kid":"k1"}`))
	claims, _ := json.Marshal(map[string]any{
		"sub":   "u1",
		"email": "z@hanzo.ai",
		"exp":   time.Now().Add(time.Hour).Unix(),
	})
	payload := base64.RawURLEncoding.EncodeToString(claims)
	signing := header + "." + payload
	dig := sha256.Sum256([]byte(signing))
	sig, _ := rsa.SignPKCS1v15(rand.Reader, priv, crypto.SHA256, dig[:])
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func TestHealth(t *testing.T) {
	s, _, done := setupTestServer(t)
	defer done()
	req := httptest.NewRequest(http.MethodGet, "/v1/health", nil)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status=%d", w.Code)
	}
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body["status"] != "ok" || body["version"] != "test" {
		t.Fatalf("body=%v", body)
	}
}

func TestExtractUnauthed(t *testing.T) {
	s, _, done := setupTestServer(t)
	defer done()
	req := httptest.NewRequest(http.MethodPost, "/v1/extract", bytes.NewReader(nil))
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != 401 {
		t.Fatalf("status=%d", w.Code)
	}
}

func buildMultipart(t *testing.T, filename string, body []byte) (string, io.Reader) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, err := mw.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = part.Write(body)
	_ = mw.Close()
	return mw.FormDataContentType(), &buf
}

func TestExtractText(t *testing.T) {
	s, priv, done := setupTestServer(t)
	defer done()
	ct, body := buildMultipart(t, "x.txt", []byte("hello world"))
	req := httptest.NewRequest(http.MethodPost, "/v1/extract", body)
	req.Header.Set("Content-Type", ct)
	req.Header.Set("Authorization", "Bearer "+mintToken(t, priv))
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var r map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &r)
	if !strings.Contains(r["text"].(string), "hello world") {
		t.Fatalf("text=%v", r["text"])
	}
	if r["format"] != "txt" {
		t.Fatalf("format=%v", r["format"])
	}
}

func TestExtractImage415(t *testing.T) {
	s, priv, done := setupTestServer(t)
	defer done()
	png := []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A, 0, 0, 0, 13, 'I', 'H', 'D', 'R'}
	ct, body := buildMultipart(t, "x.png", png)
	req := httptest.NewRequest(http.MethodPost, "/v1/extract", body)
	req.Header.Set("Content-Type", ct)
	req.Header.Set("Authorization", "Bearer "+mintToken(t, priv))
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != 415 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "image OCR") {
		t.Fatalf("body=%s", w.Body.String())
	}
}

func TestExtractURL(t *testing.T) {
	s, priv, done := setupTestServer(t)
	defer done()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("remote payload"))
	}))
	defer upstream.Close()
	body, _ := json.Marshal(map[string]string{"url": upstream.URL})
	req := httptest.NewRequest(http.MethodPost, "/v1/extract/url", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+mintToken(t, priv))
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var r map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &r)
	if !strings.Contains(r["text"].(string), "remote payload") {
		t.Fatalf("text=%v", r["text"])
	}
}

func TestExtractURLRejectsNonHTTP(t *testing.T) {
	s, priv, done := setupTestServer(t)
	defer done()
	body, _ := json.Marshal(map[string]string{"url": "file:///etc/passwd"})
	req := httptest.NewRequest(http.MethodPost, "/v1/extract/url", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+mintToken(t, priv))
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != 400 {
		t.Fatalf("status=%d", w.Code)
	}
}
