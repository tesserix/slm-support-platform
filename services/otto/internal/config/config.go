package config

import (
	"errors"
	"strings"

	"github.com/joho/godotenv"
	"github.com/kelseyhightower/envconfig"
)

// ErrInternalAuthRequired is returned by Load when ENV != "dev" and
// INTERNAL_AUTH_SECRET is empty. Otto's REST surface is gated only by
// the X-Internal-Auth header (CORS allows X-Tenant-Id / X-User-Id
// cross-origin with credentials), so an empty secret in prod would
// flip CORS into the only impersonation gate — exactly what we don't
// want.
var ErrInternalAuthRequired = errors.New(
	"otto config: INTERNAL_AUTH_SECRET must be set when ENV != \"dev\"",
)

// Config holds runtime settings for the otto service.
// All fields load from env vars via kelseyhightower/envconfig.
type Config struct {
	Env      string `envconfig:"ENV" default:"dev"`
	HTTPPort int    `envconfig:"HTTP_PORT" default:"8080"`

	MongoURL      string `envconfig:"MONGO_URL" required:"true"`
	MongoDatabase string `envconfig:"MONGO_DATABASE" default:"otto"`

	// CustomerSessionCookie — signed HttpOnly cookie used by anonymous
	// storefront visitors so they can reconnect to their own thread without
	// leaking access to anyone else's messages.
	CustomerSessionCookie string `envconfig:"CUSTOMER_SESSION_COOKIE" default:"otto_session"`
	CustomerSessionSecret string `envconfig:"CUSTOMER_SESSION_SECRET" required:"true"`
	CustomerCookieDomain  string `envconfig:"CUSTOMER_COOKIE_DOMAIN" default:""`
	CustomerCookieSecure  bool   `envconfig:"CUSTOMER_COOKIE_SECURE" default:"true"`

	// Trust boundary with the storefront/admin Next.js apps (matches the
	// convention used by marketplace-api: a shared key the proxy injects as
	// X-Internal-Auth so raw public traffic can never reach /api/v1/* routes
	// without going through the app's server-side proxy first).
	InternalAuthSecret string `envconfig:"INTERNAL_AUTH_SECRET" default:""`

	// CORS — comma-separated list of origins allowed to call the service
	// directly (the WebSocket upgrade needs this; REST calls go through the
	// Next.js proxy and do not).
	CORSAllowedOrigins string `envconfig:"CORS_ALLOWED_ORIGINS" default:""`

	// Outbound email for anonymous OTP verification. When SendgridAPIKey is
	// empty the service falls back to a stdout log mailer — useful for dev
	// and for keeping the service bootable before the provider secret has
	// been provisioned.
	SendgridAPIKey string `envconfig:"SENDGRID_API_KEY" default:""`
	OTPFromEmail   string `envconfig:"OTP_FROM_EMAIL" default:"noreply@mark8ly.com"`
	OTPFromName    string `envconfig:"OTP_FROM_NAME" default:"Otto Support"`
	OTPCodeTTL     int    `envconfig:"OTP_CODE_TTL_SECONDS" default:"600"`
	OTPMaxAttempts int    `envconfig:"OTP_MAX_ATTEMPTS" default:"5"`
	OTPResendCooldown int `envconfig:"OTP_RESEND_COOLDOWN_SECONDS" default:"45"`
}

// Load reads .env (best-effort) then populates a Config from env vars.
func Load() (*Config, error) {
	_ = godotenv.Load()

	var cfg Config
	if err := envconfig.Process("", &cfg); err != nil {
		return nil, err
	}
	// GCP Secret Manager sometimes stores random-base64 secrets with a
	// trailing newline. Strip whitespace so it doesn't poison the HMAC
	// comparison on the X-Internal-Auth check (HTTP strips trailing LF
	// from header values in transit — the mismatch would silently 401
	// every request).
	cfg.CustomerSessionSecret = strings.TrimSpace(cfg.CustomerSessionSecret)
	cfg.InternalAuthSecret = strings.TrimSpace(cfg.InternalAuthSecret)
	cfg.SendgridAPIKey = strings.TrimSpace(cfg.SendgridAPIKey)
	// Refuse-to-boot in non-dev when the internal-auth secret is empty.
	// Allowing the service to start without it would mean the only
	// remaining filter on /api/v1/admin/* and /api/v1/storefront/* is
	// the CORS allow-list — and CORS lets X-Tenant-Id / X-Store-Id /
	// X-User-* through with AllowCredentials=true, which is fine when
	// X-Internal-Auth is also required and not fine when it isn't.
	if cfg.Env != "dev" && cfg.InternalAuthSecret == "" {
		return nil, ErrInternalAuthRequired
	}
	return &cfg, nil
}
