package mailer

import (
	"context"
	"errors"
	"log/slog"
	"testing"
)

// recordingMailer captures every SendOTP call and returns a scripted error.
type recordingMailer struct {
	calls int
	to    string
	err   error
}

func (r *recordingMailer) SendOTP(_ context.Context, _, to, _, _, _ string) error {
	r.calls++
	r.to = to
	return r.err
}

func TestFallbackMailer_PrimarySucceeds_FallbackNotCalled(t *testing.T) {
	primary := &recordingMailer{}
	fallback := &recordingMailer{}
	m := &FallbackMailer{Primary: primary, Fallback: fallback, Logger: slog.Default()}

	if err := m.SendOTP(context.Background(), "t1", "user@example.com", "User", "123456", "Store"); err != nil {
		t.Fatalf("SendOTP: %v", err)
	}
	if primary.calls != 1 || fallback.calls != 0 {
		t.Fatalf("calls primary=%d fallback=%d, want 1/0 — fallback must not fire when primary delivers", primary.calls, fallback.calls)
	}
}

func TestFallbackMailer_PrimaryFails_FallbackDelivers(t *testing.T) {
	primary := &recordingMailer{err: errors.New("sendgrid 503")}
	fallback := &recordingMailer{}
	m := &FallbackMailer{Primary: primary, Fallback: fallback, Logger: slog.Default()}

	if err := m.SendOTP(context.Background(), "t1", "user@example.com", "User", "123456", "Store"); err != nil {
		t.Fatalf("SendOTP: %v — fallback delivery must count as success", err)
	}
	if fallback.calls != 1 || fallback.to != "user@example.com" {
		t.Fatalf("fallback calls=%d to=%q, want the original message retried once", fallback.calls, fallback.to)
	}
}

func TestFallbackMailer_BothFail_ErrorsJoined(t *testing.T) {
	primaryErr := errors.New("sendgrid 503")
	fallbackErr := errors.New("resend 500")
	m := &FallbackMailer{
		Primary:  &recordingMailer{err: primaryErr},
		Fallback: &recordingMailer{err: fallbackErr},
		Logger:   slog.Default(),
	}

	err := m.SendOTP(context.Background(), "t1", "user@example.com", "User", "123456", "Store")
	if !errors.Is(err, primaryErr) || !errors.Is(err, fallbackErr) {
		t.Fatalf("error %v must wrap both provider errors", err)
	}
}

func TestFallbackMailer_ContextCancelled_SkipsFallback(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	primary := &recordingMailer{err: context.Canceled}
	fallback := &recordingMailer{}
	m := &FallbackMailer{Primary: primary, Fallback: fallback, Logger: slog.Default()}

	if err := m.SendOTP(ctx, "t1", "user@example.com", "User", "123456", "Store"); err == nil {
		t.Fatal("SendOTP: want error on cancelled context")
	}
	if fallback.calls != 0 {
		t.Fatalf("fallback calls=%d, want 0 — a dead context must not burn the fallback provider", fallback.calls)
	}
}
