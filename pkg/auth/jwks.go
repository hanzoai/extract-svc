// Package auth verifies bearer JWTs against the Hanzo IAM JWKS
// endpoint. It is intentionally minimal: RS256 / RS512 only, 10-minute
// JWKS cache, no offline mode, no token introspection. If the JWT
// can't be cryptographically verified against a known key, the
// request is unauthenticated. Period.
package auth

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	DefaultJWKSURL  = "https://hanzo.id/v1/iam/.well-known/jwks"
	jwksCacheTTL    = 10 * time.Minute
	httpFetchTimeout = 10 * time.Second
)

// Claims is the small slice of the JWT we care about. Everything else
// is preserved in Raw for callers that need it.
type Claims struct {
	Subject  string         `json:"sub"`
	Email    string         `json:"email"`
	Owner    string         `json:"owner"`
	Name     string         `json:"name"`
	Audience any            `json:"aud"`
	Issuer   string         `json:"iss"`
	Expires  int64          `json:"exp"`
	IssuedAt int64          `json:"iat"`
	Raw      map[string]any `json:"-"`
}

// Verifier is safe for concurrent use. Construct via New().
type Verifier struct {
	jwksURL string
	httpc   *http.Client

	mu      sync.RWMutex
	keys    map[string]*rsa.PublicKey
	fetched time.Time
}

func New(jwksURL string) *Verifier {
	if jwksURL == "" {
		jwksURL = DefaultJWKSURL
	}
	return &Verifier{
		jwksURL: jwksURL,
		httpc:   &http.Client{Timeout: httpFetchTimeout},
	}
}

var (
	ErrMissingBearer = errors.New("missing bearer token")
	ErrBadToken      = errors.New("malformed token")
	ErrBadSignature  = errors.New("invalid signature")
	ErrExpired       = errors.New("token expired")
	ErrUnknownKey    = errors.New("unknown signing key")
	ErrAlgNotAllowed = errors.New("alg not allowed")
)

// FromRequest pulls the bearer token, verifies it, returns the claims.
func (v *Verifier) FromRequest(r *http.Request) (*Claims, error) {
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, "Bearer ") {
		return nil, ErrMissingBearer
	}
	return v.Verify(strings.TrimPrefix(h, "Bearer "))
}

// Verify parses the JWT, locates the signing key by `kid`, and
// validates RS256 / RS512. exp is enforced; iat is informational.
func (v *Verifier) Verify(token string) (*Claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, ErrBadToken
	}
	headerB, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, fmt.Errorf("%w: header b64: %v", ErrBadToken, err)
	}
	payloadB, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("%w: payload b64: %v", ErrBadToken, err)
	}
	sigB, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, fmt.Errorf("%w: sig b64: %v", ErrBadToken, err)
	}
	var hdr struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
		Typ string `json:"typ"`
	}
	if err := json.Unmarshal(headerB, &hdr); err != nil {
		return nil, fmt.Errorf("%w: header json: %v", ErrBadToken, err)
	}
	if hdr.Alg != "RS256" && hdr.Alg != "RS512" {
		return nil, fmt.Errorf("%w: %s", ErrAlgNotAllowed, hdr.Alg)
	}
	key, err := v.lookupKey(hdr.Kid)
	if err != nil {
		return nil, err
	}

	signed := []byte(parts[0] + "." + parts[1])
	var h hash.Hash
	var algo int
	if hdr.Alg == "RS256" {
		h = sha256.New()
		algo = 256
	} else {
		h = sha512.New()
		algo = 512
	}
	if _, err := h.Write(signed); err != nil {
		return nil, fmt.Errorf("%w: hash: %v", ErrBadToken, err)
	}
	digest := h.Sum(nil)
	var verifyErr error
	if algo == 256 {
		verifyErr = rsa.VerifyPKCS1v15(key, crypto.SHA256, digest, sigB)
	} else {
		verifyErr = rsa.VerifyPKCS1v15(key, crypto.SHA512, digest, sigB)
	}
	if verifyErr != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadSignature, verifyErr)
	}

	// Parse claims twice: once strongly-typed, once into Raw so callers
	// can inspect custom fields (org, roles, …) without us hardcoding.
	c := &Claims{}
	if err := json.Unmarshal(payloadB, c); err != nil {
		return nil, fmt.Errorf("%w: claims json: %v", ErrBadToken, err)
	}
	if err := json.Unmarshal(payloadB, &c.Raw); err != nil {
		return nil, fmt.Errorf("%w: claims raw: %v", ErrBadToken, err)
	}
	if c.Expires > 0 && time.Now().Unix() >= c.Expires {
		return nil, ErrExpired
	}
	return c, nil
}

func (v *Verifier) lookupKey(kid string) (*rsa.PublicKey, error) {
	v.mu.RLock()
	fresh := time.Since(v.fetched) < jwksCacheTTL
	k, ok := v.keys[kid]
	v.mu.RUnlock()
	if ok && fresh {
		return k, nil
	}
	if err := v.refresh(); err != nil {
		return nil, err
	}
	v.mu.RLock()
	defer v.mu.RUnlock()
	if k, ok := v.keys[kid]; ok {
		return k, nil
	}
	// Some IAM deployments serve a single key without `kid` — accept
	// that only when there is exactly one key in the set.
	if kid == "" && len(v.keys) == 1 {
		for _, k := range v.keys {
			return k, nil
		}
	}
	return nil, fmt.Errorf("%w: kid=%q", ErrUnknownKey, kid)
}

func (v *Verifier) refresh() error {
	req, err := http.NewRequest(http.MethodGet, v.jwksURL, nil)
	if err != nil {
		return err
	}
	resp, err := v.httpc.Do(req)
	if err != nil {
		return fmt.Errorf("jwks fetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("jwks fetch: status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("jwks read: %w", err)
	}
	var doc struct {
		Keys []struct {
			Kty string `json:"kty"`
			Kid string `json:"kid"`
			N   string `json:"n"`
			E   string `json:"e"`
			Use string `json:"use"`
			Alg string `json:"alg"`
		} `json:"keys"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return fmt.Errorf("jwks json: %w", err)
	}
	keys := map[string]*rsa.PublicKey{}
	for _, k := range doc.Keys {
		if k.Kty != "RSA" {
			continue
		}
		nBytes, err := base64.RawURLEncoding.DecodeString(k.N)
		if err != nil {
			// some providers emit standard base64; tolerate both
			if nBytes, err = base64.StdEncoding.DecodeString(k.N); err != nil {
				continue
			}
		}
		eBytes, err := base64.RawURLEncoding.DecodeString(k.E)
		if err != nil {
			if eBytes, err = base64.StdEncoding.DecodeString(k.E); err != nil {
				continue
			}
		}
		// `e` is typically 3 bytes (0x010001). Pad to 8 to use binary.BigEndian.
		var padded [8]byte
		copy(padded[8-len(eBytes):], eBytes)
		e := int(binary.BigEndian.Uint64(padded[:]))
		keys[k.Kid] = &rsa.PublicKey{
			N: new(big.Int).SetBytes(nBytes),
			E: e,
		}
	}
	v.mu.Lock()
	v.keys = keys
	v.fetched = time.Now()
	v.mu.Unlock()
	return nil
}
