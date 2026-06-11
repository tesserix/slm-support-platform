package mailer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// ResendMailer posts the OTP email via the Resend API (https://resend.com).
// Implemented directly (no SDK) for the same reason as SendgridMailer —
// the integration is one stable POST. Wired as the FALLBACK provider
// behind SendGrid via FallbackMailer, so OTP delivery survives a SendGrid
// outage or rate limit.
type ResendMailer struct {
	APIKey     string
	FromEmail  string
	FromName   string
	HTTPClient *http.Client
}

// NewResendMailer returns a ready-to-use sender. 10s timeout matches the
// SendGrid mailer.
func NewResendMailer(apiKey, fromEmail, fromName string) *ResendMailer {
	return &ResendMailer{
		APIKey:    apiKey,
		FromEmail: fromEmail,
		FromName:  fromName,
		HTTPClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

type resendPayload struct {
	From    string   `json:"from"`
	To      []string `json:"to"`
	Subject string   `json:"subject"`
	HTML    string   `json:"html,omitempty"`
	Text    string   `json:"text,omitempty"`
	// Tags are Resend's counterpart to SendGrid custom_args — echoed back
	// on webhook events for per-tenant attribution. Names/values only
	// allow ASCII letters, numbers, underscores and dashes; tenant IDs
	// here arrive on X-Tenant-Id from the product proxies, so anything
	// that doesn't conform is skipped rather than failing the send.
	Tags []resendTag `json:"tags,omitempty"`
}

type resendTag struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// SendOTP builds + dispatches the same text + HTML email as the SendGrid
// mailer (shared otpEmail builder), via Resend.
func (m *ResendMailer) SendOTP(ctx context.Context, tenantID, to, recipientName, code, storeName string) error {
	subject, text, html := otpEmail(recipientName, code, storeName)

	tags := []resendTag{{Name: "product", Value: "mark8ly"}}
	if tenantID != "" && resendTagValueOK(tenantID) {
		tags = append(tags, resendTag{Name: "tenant_id", Value: tenantID})
	}
	payload := resendPayload{
		From:    fmt.Sprintf("%s <%s>", m.FromName, m.FromEmail),
		To:      []string{to},
		Subject: subject,
		HTML:    html,
		Text:    text,
		Tags:    tags,
	}
	buf, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("mailer: marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.resend.com/emails", bytes.NewReader(buf))
	if err != nil {
		return fmt.Errorf("mailer: new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+m.APIKey)

	res, err := m.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("mailer: send: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		body, _ := io.ReadAll(res.Body)
		return fmt.Errorf("mailer: resend %d: %s", res.StatusCode, string(body))
	}
	return nil
}

// resendTagValueOK reports whether v fits Resend's tag charset (ASCII
// letters, numbers, underscores and dashes). Unlike SendGrid custom_args,
// a non-conforming tag value rejects the whole send — better to drop the
// attribution tag than to drop the OTP.
func resendTagValueOK(v string) bool {
	for i := 0; i < len(v); i++ {
		c := v[i]
		switch {
		case c >= 'a' && c <= 'z':
		case c >= 'A' && c <= 'Z':
		case c >= '0' && c <= '9':
		case c == '_' || c == '-':
		default:
			return false
		}
	}
	return true
}
