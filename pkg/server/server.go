// Package server hosts the HTTP layer. One handler per route, no
// framework. Everything is wired in New() so the test harness can
// poke individual handlers if it needs to.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/extract/pkg/auth"
	"github.com/hanzoai/extract/pkg/extract"
)

// MaxUploadBytes is the hard cap on POST bodies. 25 MB per the contract.
const MaxUploadBytes int64 = 25 * 1024 * 1024

// FetchTimeout bounds the URL-fetch path. Sane default — IAM-only
// service shouldn't be a SSRF amplifier either way.
const FetchTimeout = 20 * time.Second

type Config struct {
	Version       string
	JWKSURL       string
	Logger        *slog.Logger
	URLFetchAllow func(url string) bool // optional; defaults to allow all https://
	Now           func() time.Time      // injectable for tests
}

type Server struct {
	cfg      Config
	verifier *auth.Verifier
	mux      *http.ServeMux
	httpc    *http.Client
}

func New(cfg Config) *Server {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	s := &Server{
		cfg:      cfg,
		verifier: auth.New(cfg.JWKSURL),
		mux:      http.NewServeMux(),
		httpc:    &http.Client{Timeout: FetchTimeout},
	}
	s.mux.HandleFunc("GET /v1/health", s.handleHealth)
	s.mux.HandleFunc("POST /v1/extract", s.requireAuth(s.handleExtract))
	s.mux.HandleFunc("POST /v1/extract/url", s.requireAuth(s.handleExtractURL))
	return s
}

func (s *Server) Handler() http.Handler { return s.mux }

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status":  "ok",
		"version": s.cfg.Version,
	})
}

func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, err := s.verifier.FromRequest(r)
		if err != nil {
			s.cfg.Logger.Debug("auth rejected", "err", err)
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		// Stamp identity headers on the inbound context for downstream
		// log lines / future request-scoped values.
		ctx := context.WithValue(r.Context(), claimsKey{}, claims)
		next(w, r.WithContext(ctx))
	}
}

type claimsKey struct{}

func (s *Server) handleExtract(w http.ResponseWriter, r *http.Request) {
	// Reject early on Content-Length so we don't allocate a 1GB form.
	if r.ContentLength > MaxUploadBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "payload too large")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, MaxUploadBytes)
	// MultipartReader streams parts — we don't materialize the whole
	// form, only the `file` part's bytes (still bounded by MaxBytesReader).
	mr, err := r.MultipartReader()
	if err != nil {
		writeError(w, http.StatusBadRequest, "expected multipart/form-data")
		return
	}
	var data []byte
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			if isTooLarge(err) {
				writeError(w, http.StatusRequestEntityTooLarge, "payload too large")
				return
			}
			writeError(w, http.StatusBadRequest, "malformed multipart")
			return
		}
		if part.FormName() != "file" {
			part.Close()
			continue
		}
		data, err = extract.ReadAllLimited(part, MaxUploadBytes)
		part.Close()
		if err != nil {
			writeError(w, http.StatusRequestEntityTooLarge, "payload too large")
			return
		}
		break
	}
	if len(data) == 0 {
		writeError(w, http.StatusBadRequest, "missing file field")
		return
	}
	s.respondExtract(w, data)
}

func (s *Server) handleExtractURL(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<16) // request itself is tiny
	var body struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	u := strings.TrimSpace(body.URL)
	if u == "" || !(strings.HasPrefix(u, "https://") || strings.HasPrefix(u, "http://")) {
		writeError(w, http.StatusBadRequest, "url must be http(s)")
		return
	}
	if s.cfg.URLFetchAllow != nil && !s.cfg.URLFetchAllow(u) {
		writeError(w, http.StatusForbidden, "url not allowed")
		return
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, u, nil)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad url")
		return
	}
	req.Header.Set("User-Agent", "hanzo-extract/"+s.cfg.Version)
	resp, err := s.httpc.Do(req)
	if err != nil {
		writeError(w, http.StatusBadGateway, "fetch failed")
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		writeError(w, http.StatusBadGateway, fmt.Sprintf("upstream status %d", resp.StatusCode))
		return
	}
	data, err := extract.ReadAllLimited(resp.Body, MaxUploadBytes)
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "remote payload too large")
		return
	}
	s.respondExtract(w, data)
}

func (s *Server) respondExtract(w http.ResponseWriter, data []byte) {
	res, err := extract.FromBytes(data)
	if err != nil {
		if errors.Is(err, extract.ErrUnsupportedFormat) {
			if res.Format == extract.FormatImage {
				writeError(w, http.StatusUnsupportedMediaType, "image OCR not supported in v0.1")
				return
			}
			writeError(w, http.StatusUnsupportedMediaType, "unsupported format")
			return
		}
		s.cfg.Logger.Warn("extract failed", "err", err)
		writeError(w, http.StatusUnprocessableEntity, "extraction failed")
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]any{"error": msg})
}

func isTooLarge(err error) bool {
	var mbe *http.MaxBytesError
	return errors.As(err, &mbe)
}
