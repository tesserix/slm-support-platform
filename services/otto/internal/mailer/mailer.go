// Package mailer sends outbound email for Otto. The Mailer interface is
// deliberately tiny — a single SendOTP call — so swapping providers
// (SendGrid today, SES tomorrow, whatever) is a one-line change.
package mailer

import (
	"context"
	"fmt"
	"log/slog"
)

// Mailer dispatches OTP verification emails.
//
// tenantID is the mark8ly tenant the recipient belongs to. It's
// forwarded to SendGrid as a `tenant_id` custom_arg so the
// notification-service webhook ingester can attribute open/click events
// back to the right tenant. Empty when the caller doesn't yet know the
// tenant (e.g. pre-onboarding flows); the mailer omits the field rather
// than emitting an empty string.
type Mailer interface {
	SendOTP(ctx context.Context, tenantID, to, recipientName, code, storeName string) error
}

// LogMailer is the dev/fallback impl — writes the OTP to the service log
// so a developer running locally (or any env where SendGrid is not yet
// provisioned) can still complete the verification flow. Never enable in
// prod: a process logging OTPs to stdout is a credential leak.
type LogMailer struct {
	Logger *slog.Logger
}

// SendOTP records the code instead of actually emailing it.
func (m *LogMailer) SendOTP(_ context.Context, tenantID, to, recipientName, code, storeName string) error {
	if m.Logger == nil {
		return fmt.Errorf("mailer: log mailer has no logger")
	}
	m.Logger.Warn("otp (dev log mailer — would be emailed in prod)",
		slog.String("tenant_id", tenantID),
		slog.String("to", to),
		slog.String("name", recipientName),
		slog.String("store", storeName),
		slog.String("code", code),
	)
	return nil
}
