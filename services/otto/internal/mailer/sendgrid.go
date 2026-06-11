package mailer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
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

// SendOTP dispatches the shared OTP email (see otpEmail) via SendGrid.
//
// tenantID is forwarded to SendGrid as a custom_arg for per-tenant
// engagement attribution. Empty values are omitted rather than sent.
func (m *SendgridMailer) SendOTP(ctx context.Context, tenantID, to, recipientName, code, storeName string) error {
	subject, text, html := otpEmail(recipientName, code, storeName)

	customArgs := map[string]string{"product": "mark8ly"}
	if tenantID != "" {
		customArgs["tenant_id"] = tenantID
	}
	payload := sendgridPayload{
		Personalizations: []sendgridPersonalization{{
			To: []sendgridAddress{{Email: to, Name: recipientName}},
		}},
		From:    sendgridAddress{Email: m.FromEmail, Name: m.FromName},
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
