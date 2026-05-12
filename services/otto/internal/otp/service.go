package otp

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/tesserix/slm-support-platform/services/otto/internal/mailer"
)

// Errors that HTTP handlers map to status codes.
var (
	// ErrResendTooSoon — a challenge was issued within the cooldown window.
	ErrResendTooSoon = errors.New("otp: resend too soon")
	// ErrInvalidCode — wrong code supplied (but attempts remain).
	ErrInvalidCode = errors.New("otp: invalid code")
	// ErrExpired — the challenge has expired or been consumed already.
	ErrExpired = errors.New("otp: expired")
	// ErrTooManyAttempts — max attempts exceeded, force a fresh start.
	ErrTooManyAttempts = errors.New("otp: too many attempts")
)

// Service orchestrates OTP creation + delivery + verification.
type Service struct {
	repo           *Repository
	mailer         mailer.Mailer
	ttl            time.Duration
	maxAttempts    int
	resendCooldown time.Duration
	now            func() time.Time // overridable in tests
}

// Config bundles the knobs Service needs at startup.
type Config struct {
	TTL            time.Duration
	MaxAttempts    int
	ResendCooldown time.Duration
}

// NewService returns a ready-to-use OTP service.
func NewService(repo *Repository, m mailer.Mailer, cfg Config) *Service {
	if cfg.TTL <= 0 {
		cfg.TTL = 10 * time.Minute
	}
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = 5
	}
	if cfg.ResendCooldown <= 0 {
		cfg.ResendCooldown = 45 * time.Second
	}
	return &Service{
		repo:           repo,
		mailer:         m,
		ttl:            cfg.TTL,
		maxAttempts:    cfg.MaxAttempts,
		resendCooldown: cfg.ResendCooldown,
		now:            func() time.Time { return time.Now().UTC() },
	}
}

// StartInput is what /verify/start accepts.
type StartInput struct {
	TenantID  string
	StoreID   string
	Email     string
	Name      string
	StoreName string // used by the email template only
}

// StartResult summarises a successful start call. The code is NEVER
// returned to the HTTP caller — it only goes to the email.
type StartResult struct {
	ExpiresAt time.Time
	MaskedTo  string // email shown back to the user to reassure them
}

// Start creates (or replaces) a challenge for the given email and sends
// the code. Rate-limited by ResendCooldown.
func (s *Service) Start(ctx context.Context, in StartInput) (*StartResult, error) {
	email := normaliseEmail(in.Email)
	if email == "" {
		return nil, fmt.Errorf("otp: email required")
	}

	// Cooldown: if a pending challenge was minted within the cooldown
	// window, refuse to mint another. Prevents a malicious site from
	// using our SendGrid quota to spam an arbitrary inbox.
	if prev, err := s.repo.LatestFor(ctx, in.TenantID, in.StoreID, email); err == nil {
		if s.now().Sub(prev.CreatedAt) < s.resendCooldown {
			return nil, ErrResendTooSoon
		}
	} else if !errors.Is(err, ErrNotFound) {
		return nil, err
	}

	code, err := generateCode()
	if err != nil {
		return nil, err
	}
	now := s.now()
	expires := now.Add(s.ttl)
	ch := &Challenge{
		ID:          uuid.NewString(),
		TenantID:    in.TenantID,
		StoreID:     in.StoreID,
		Email:       email,
		Name:        strings.TrimSpace(in.Name),
		CodeHash:    hashCode(code),
		CreatedAt:   now,
		ExpiresAt:   expires,
		Attempts:    0,
		MaxAttempts: s.maxAttempts,
	}
	if err := s.repo.Insert(ctx, ch); err != nil {
		return nil, err
	}
	if err := s.mailer.SendOTP(ctx, in.TenantID, email, ch.Name, code, in.StoreName); err != nil {
		// Best-effort: the row is already inserted so a retry on the next
		// /verify/start will hit the cooldown. Surface the error so the
		// caller can show a useful message.
		return nil, fmt.Errorf("otp: send: %w", err)
	}
	return &StartResult{ExpiresAt: expires, MaskedTo: mask(email)}, nil
}

// VerifyInput is what the conversation-create handler passes in.
type VerifyInput struct {
	TenantID string
	StoreID  string
	Email    string
	Code     string
}

// Verify consumes the challenge on success. Returns an error aligned to
// the HTTP contract (ErrInvalidCode / ErrExpired / ErrTooManyAttempts).
func (s *Service) Verify(ctx context.Context, in VerifyInput) error {
	email := normaliseEmail(in.Email)
	code := strings.TrimSpace(in.Code)
	if email == "" || code == "" {
		return ErrInvalidCode
	}

	ch, err := s.repo.LatestFor(ctx, in.TenantID, in.StoreID, email)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return ErrExpired
		}
		return err
	}
	if s.now().After(ch.ExpiresAt) {
		return ErrExpired
	}
	// Bump attempts first so a wrong guess still counts even if the
	// challenge is about to be consumed.
	attempts, err := s.repo.IncAttempt(ctx, ch.ID)
	if err != nil {
		return err
	}
	// Constant-time compare on the hash — defence in depth even though
	// the attempt counter already prevents timing-style brute force.
	if subtle.ConstantTimeCompare([]byte(ch.CodeHash), []byte(hashCode(code))) != 1 {
		if attempts >= ch.MaxAttempts {
			return ErrTooManyAttempts
		}
		return ErrInvalidCode
	}
	if err := s.repo.MarkConsumed(ctx, ch.ID, s.now()); err != nil {
		return err
	}
	return nil
}

// generateCode returns a uniformly distributed 6-digit code in the range
// 000000–999999. rand.Int avoids the modulo bias cheap-and-cheerful
// approaches have.
func generateCode() (string, error) {
	max := big.NewInt(1_000_000)
	n, err := rand.Int(rand.Reader, max)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}

func hashCode(code string) string {
	sum := sha256.Sum256([]byte(code))
	return hex.EncodeToString(sum[:])
}

func normaliseEmail(raw string) string {
	return strings.ToLower(strings.TrimSpace(raw))
}

// mask returns a privacy-preserving preview of the email — useful as a
// "we sent a code to s***@gmail.com" confirmation in the widget without
// echoing the full address a caller just typed.
func mask(email string) string {
	at := strings.IndexByte(email, '@')
	if at < 1 {
		return "***"
	}
	local := email[:at]
	domain := email[at:]
	if len(local) <= 2 {
		return local[:1] + "***" + domain
	}
	return local[:1] + strings.Repeat("*", len(local)-2) + local[len(local)-1:] + domain
}
