package mailer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// SendgridMailer posts the OTP email via SendGrid's v3 Mail Send API.
// Implemented directly (no SDK) to keep the dependency footprint small —
// the payload shape is stable and well documented.
type SendgridMailer struct {
	APIKey     string
	FromEmail  string
	FromName   string
	HTTPClient *http.Client
}

// NewSendgridMailer returns a ready-to-use sender. The HTTP client is
// internal — 10s timeout is plenty for an SMTP bridge call.
func NewSendgridMailer(apiKey, fromEmail, fromName string) *SendgridMailer {
	return &SendgridMailer{
		APIKey:    apiKey,
		FromEmail: fromEmail,
		FromName:  fromName,
		HTTPClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

type sendgridPayload struct {
	Personalizations []sendgridPersonalization `json:"personalizations"`
	From             sendgridAddress           `json:"from"`
	Subject          string                    `json:"subject"`
	Content          []sendgridContent         `json:"content"`
	// CustomArgs is echoed back on every SendGrid engagement event
	// (delivered/open/click/bounce). Used by the notification-service
	// webhook ingester to attribute events to the right tenant in
	// tesserix-home dashboards. Always carries `product=mark8ly`;
	// `tenant_id` is included only when the caller has it.
	CustomArgs map[string]string `json:"custom_args,omitempty"`
}

type sendgridPersonalization struct {
	To []sendgridAddress `json:"to"`
}

type sendgridAddress struct {
	Email string `json:"email"`
	Name  string `json:"name,omitempty"`
}

type sendgridContent struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

// SendOTP builds + dispatches a simple text + HTML email. The body is
// intentionally plain: most "unexpected chat OTP" mails go through spam
// filters, and heavy templates make the "is this suspicious" judgement
// harder for the recipient.
//
// tenantID is forwarded to SendGrid as a custom_arg for per-tenant
// engagement attribution. Empty values are omitted rather than sent.
func (m *SendgridMailer) SendOTP(ctx context.Context, tenantID, to, recipientName, code, storeName string) error {
	if storeName == "" {
		storeName = "the store"
	}
	subject := fmt.Sprintf("%s — your support chat verification code", storeName)
	text := fmt.Sprintf(
		"Hi%s,\n\nYour one-time code to start a support chat on %s is:\n\n  %s\n\n"+
			"It expires in 10 minutes. If you did not request this, you can safely ignore this email.\n",
		greetingSuffix(recipientName), storeName, code,
	)
	html := fmt.Sprintf(
		`<div style="font-family: system-ui, -apple-system, Segoe UI, Roboto, sans-serif; color: #111827; font-size: 14px; line-height: 1.5;">`+
			`<p>Hi%s,</p>`+
			`<p>Your one-time code to start a support chat on <strong>%s</strong> is:</p>`+
			`<p style="font-size: 28px; letter-spacing: 8px; font-weight: 600; margin: 18px 0;">%s</p>`+
			`<p style="color: #6b7280;">It expires in 10 minutes. If you did not request this, you can safely ignore this email.</p>`+
			`</div>`,
		greetingSuffix(recipientName), htmlEscape(storeName), code,
	)

	customArgs := map[string]string{"product": "mark8ly"}
	if tenantID != "" {
		customArgs["tenant_id"] = tenantID
	}
	payload := sendgridPayload{
		Personalizations: []sendgridPersonalization{{
			To: []sendgridAddress{{Email: to, Name: recipientName}},
		}},
		From: sendgridAddress{Email: m.FromEmail, Name: m.FromName},
		Subject: subject,
		Content: []sendgridContent{
			{Type: "text/plain", Value: text},
			{Type: "text/html", Value: html},
		},
		CustomArgs: customArgs,
	}
	buf, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("mailer: marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.sendgrid.com/v3/mail/send", bytes.NewReader(buf))
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
		return fmt.Errorf("mailer: sendgrid %d", res.StatusCode)
	}
	return nil
}

func greetingSuffix(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	return " " + name
}

// htmlEscape is a tiny helper so we don't pull in html/template for a
// single substitution. The only value that reaches HTML context is the
// store name, which is merchant-controlled string data.
func htmlEscape(s string) string {
	r := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		"\"", "&quot;",
		"'", "&#39;",
	)
	return r.Replace(s)
}
