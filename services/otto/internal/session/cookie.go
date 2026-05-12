package session

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Signer mints and validates opaque session tokens.
//
// A token is formatted as "<random-id>.<expiry-unix>.<hmac-base64url>".
// The random id is what's persisted against a conversation as the
// session_token — it's the customer's "bearer ticket" to their own thread.
// The hmac binds tenant + store + id + expiry so we can trust a cookie came
// from us without a database round-trip on every request.
type Signer struct {
	secret []byte
	ttl    time.Duration
}

// NewSigner returns a Signer using the given HMAC secret.
// The secret should be at least 32 bytes of entropy.
func NewSigner(secret string, ttl time.Duration) *Signer {
	return &Signer{secret: []byte(secret), ttl: ttl}
}

// Token is the decoded form of the cookie.
type Token struct {
	ID       string
	TenantID string
	StoreID  string
	Expiry   time.Time
}

// Issue mints a fresh token bound to the given tenant+store.
func (s *Signer) Issue(tenantID, storeID string) (raw string, t Token, err error) {
	idBytes := make([]byte, 24)
	if _, err = rand.Read(idBytes); err != nil {
		return "", Token{}, fmt.Errorf("session: rand: %w", err)
	}
	id := base64.RawURLEncoding.EncodeToString(idBytes)
	expiry := time.Now().UTC().Add(s.ttl)
	t = Token{ID: id, TenantID: tenantID, StoreID: storeID, Expiry: expiry}
	return s.encode(t), t, nil
}

// Parse validates a raw cookie value and returns the decoded token.
// Returns an error if the hmac fails, the cookie is malformed, or expired.
// Crucially, the caller passes the tenant+store they *expect* — if the
// cookie was minted for a different scope, validation fails. That prevents
// a customer from using a cookie across stores.
func (s *Signer) Parse(raw, tenantID, storeID string) (Token, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 5 {
		return Token{}, errors.New("session: malformed token")
	}
	id, tenantEnc, storeEnc, expStr, mac := parts[0], parts[1], parts[2], parts[3], parts[4]

	tenantBytes, err := base64.RawURLEncoding.DecodeString(tenantEnc)
	if err != nil {
		return Token{}, errors.New("session: bad tenant segment")
	}
	storeBytes, err := base64.RawURLEncoding.DecodeString(storeEnc)
	if err != nil {
		return Token{}, errors.New("session: bad store segment")
	}
	if string(tenantBytes) != tenantID || string(storeBytes) != storeID {
		return Token{}, errors.New("session: scope mismatch")
	}

	expUnix, err := parseUnix(expStr)
	if err != nil {
		return Token{}, errors.New("session: bad expiry")
	}
	expiry := time.Unix(expUnix, 0).UTC()
	if time.Now().UTC().After(expiry) {
		return Token{}, errors.New("session: expired")
	}

	expected := s.mac(id, tenantID, storeID, expUnix)
	got, err := base64.RawURLEncoding.DecodeString(mac)
	if err != nil {
		return Token{}, errors.New("session: bad mac")
	}
	if !hmac.Equal(expected, got) {
		return Token{}, errors.New("session: invalid mac")
	}
	return Token{ID: id, TenantID: tenantID, StoreID: storeID, Expiry: expiry}, nil
}

func (s *Signer) encode(t Token) string {
	tenantEnc := base64.RawURLEncoding.EncodeToString([]byte(t.TenantID))
	storeEnc := base64.RawURLEncoding.EncodeToString([]byte(t.StoreID))
	exp := t.Expiry.Unix()
	mac := s.mac(t.ID, t.TenantID, t.StoreID, exp)
	macEnc := base64.RawURLEncoding.EncodeToString(mac)
	return fmt.Sprintf("%s.%s.%s.%d.%s", t.ID, tenantEnc, storeEnc, exp, macEnc)
}

func (s *Signer) mac(id, tenantID, storeID string, exp int64) []byte {
	h := hmac.New(sha256.New, s.secret)
	fmt.Fprintf(h, "%s|%s|%s|%d", id, tenantID, storeID, exp) //nolint:logging-smoke — writing to HMAC hash, not a logger
	return h.Sum(nil)
}

func parseUnix(s string) (int64, error) {
	var n int64
	_, err := fmt.Sscanf(s, "%d", &n)
	return n, err
}
