package mailer

import (
	"context"
	"errors"
	"log/slog"
)

// FallbackMailer chains two Mailers: every OTP goes to Primary first, and
// only when Primary errors does it retry on Fallback. Production wiring is
// SendGrid (primary) → Resend (fallback) so a SendGrid outage degrades to
// a provider switch instead of customers stuck at the OTP screen.
//
// Failover is per-message and stateless — no circuit breaker. Primary is
// retried first on every send, so recovery is automatic.
type FallbackMailer struct {
	Primary  Mailer
	Fallback Mailer
	Logger   *slog.Logger
}

// SendOTP tries Primary, then Fallback. Both errors are joined when both
// providers fail so the log shows the full failure picture.
func (m *FallbackMailer) SendOTP(ctx context.Context, tenantID, to, recipientName, code, storeName string) error {
	primaryErr := m.Primary.SendOTP(ctx, tenantID, to, recipientName, code, storeName)
	if primaryErr == nil {
		return nil
	}
	// A cancelled context will fail on the fallback too — bail early.
	if ctx.Err() != nil {
		return primaryErr
	}

	if m.Logger != nil {
		m.Logger.Warn("otp mailer: primary provider failed, retrying on fallback",
			slog.String("tenant_id", tenantID),
			slog.String("to", to),
			slog.String("error", primaryErr.Error()),
		)
	}

	fallbackErr := m.Fallback.SendOTP(ctx, tenantID, to, recipientName, code, storeName)
	if fallbackErr == nil {
		if m.Logger != nil {
			m.Logger.Info("otp mailer: fallback provider delivered",
				slog.String("to", to),
			)
		}
		return nil
	}
	return errors.Join(primaryErr, fallbackErr)
}
