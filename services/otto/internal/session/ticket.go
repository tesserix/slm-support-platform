package session

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// TicketAudience is a small enum so a ticket minted for a customer WS can't
// be replayed against an admin WS and vice versa.
type TicketAudience string

const (
	TicketAudienceCustomer TicketAudience = "customer"
	TicketAudienceStaff    TicketAudience = "staff"
)

// Ticket is a short-lived, server-issued ticket the WebSocket handshake
// carries in the ?ticket=... query string. Required because the WS path is
// routed to Otto directly by Istio, bypassing the Next.js proxy that
// normally injects the identity + tenant headers. The ticket encodes all of
// that in a signed payload.
type Ticket struct {
	Audience       TicketAudience `json:"aud"`
	TenantID       string         `json:"tid"`
	StoreID        string         `json:"sid"`
	UserID         string         `json:"uid,omitempty"`
	UserName       string         `json:"nam,omitempty"`
	UserEmail      string         `json:"eml,omitempty"`
	ConversationID string         `json:"cid,omitempty"` // required for customer; optional for staff
	SessionToken   string         `json:"stk,omitempty"` // for customer tickets, the otto_session id the conversation belongs to
	Nonce          string         `json:"non"`
	ExpiresAt      int64          `json:"exp"`
}

// TicketSigner mints and parses Tickets using a dedicated HMAC key. We
// deliberately reuse the CustomerSessionSecret — the secret has the same
// trust boundary (server-only random bytes) and giving it two purposes
// avoids adding another secret to rotate.
type TicketSigner struct {
	secret []byte
	ttl    time.Duration
}

// NewTicketSigner builds a TicketSigner.
func NewTicketSigner(secret string, ttl time.Duration) *TicketSigner {
	if ttl <= 0 {
		ttl = 2 * time.Minute
	}
	return &TicketSigner{secret: []byte(secret), ttl: ttl}
}

// Issue mints a Ticket + returns its wire-format string. Caller supplies
// audience-specific fields; Nonce and ExpiresAt are populated here.
func (s *TicketSigner) Issue(t Ticket) (string, Ticket, error) {
	if t.Audience == "" {
		return "", Ticket{}, errors.New("ticket: audience required")
	}
	if t.TenantID == "" || t.StoreID == "" {
		return "", Ticket{}, errors.New("ticket: tenant+store required")
	}
	nonceBytes := make([]byte, 16)
	if _, err := rand.Read(nonceBytes); err != nil {
		return "", Ticket{}, fmt.Errorf("ticket: rand: %w", err)
	}
	t.Nonce = base64.RawURLEncoding.EncodeToString(nonceBytes)
	if t.ExpiresAt == 0 {
		t.ExpiresAt = time.Now().UTC().Add(s.ttl).Unix()
	}
	payload, err := json.Marshal(t)
	if err != nil {
		return "", Ticket{}, err
	}
	payloadEnc := base64.RawURLEncoding.EncodeToString(payload)
	mac := s.mac(payloadEnc)
	return payloadEnc + "." + base64.RawURLEncoding.EncodeToString(mac), t, nil
}

// Parse validates wire-format and returns the decoded Ticket. Returns an
// error on bad MAC, malformed payload, or expiry.
func (s *TicketSigner) Parse(raw string) (Ticket, error) {
	if raw == "" {
		return Ticket{}, errors.New("ticket: empty")
	}
	for i := 0; i < len(raw); i++ {
		if raw[i] == '.' {
			payloadEnc := raw[:i]
			macEnc := raw[i+1:]
			mac, err := base64.RawURLEncoding.DecodeString(macEnc)
			if err != nil {
				return Ticket{}, errors.New("ticket: bad mac segment")
			}
			expected := s.mac(payloadEnc)
			if !hmac.Equal(expected, mac) {
				return Ticket{}, errors.New("ticket: invalid mac")
			}
			payload, err := base64.RawURLEncoding.DecodeString(payloadEnc)
			if err != nil {
				return Ticket{}, errors.New("ticket: bad payload")
			}
			var t Ticket
			if err := json.Unmarshal(payload, &t); err != nil {
				return Ticket{}, errors.New("ticket: payload unmarshal")
			}
			if time.Now().UTC().Unix() > t.ExpiresAt {
				return Ticket{}, errors.New("ticket: expired")
			}
			return t, nil
		}
	}
	return Ticket{}, errors.New("ticket: malformed")
}

func (s *TicketSigner) mac(payloadEnc string) []byte {
	h := hmac.New(sha256.New, s.secret)
	h.Write([]byte(payloadEnc))
	return h.Sum(nil)
}
