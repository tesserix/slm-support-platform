package main

import (
	"context"
	"os/signal"
	"syscall"
	"time"

	"github.com/tesserix/slm-support-platform/services/otto/internal/auth"
	"github.com/tesserix/slm-support-platform/services/otto/internal/changestream"
	"github.com/tesserix/slm-support-platform/services/otto/internal/config"
	"github.com/tesserix/slm-support-platform/services/otto/internal/conversation"
	"github.com/tesserix/slm-support-platform/services/otto/internal/httpserver"
	"github.com/tesserix/slm-support-platform/services/otto/internal/hub"
	"github.com/tesserix/slm-support-platform/services/otto/internal/logger"
	"github.com/tesserix/slm-support-platform/services/otto/internal/mailer"
	"github.com/tesserix/slm-support-platform/services/otto/internal/message"
	ottomongo "github.com/tesserix/slm-support-platform/services/otto/internal/mongo"
	"github.com/tesserix/slm-support-platform/services/otto/internal/otp"
	"github.com/tesserix/slm-support-platform/services/otto/internal/session"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		panic(err)
	}
	log := logger.New(cfg.Env)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// ── Mongo ──────────────────────────────────────────────────────────
	mongoClient, err := ottomongo.Connect(ctx, cfg.MongoURL, cfg.MongoDatabase)
	if err != nil {
		log.Error("mongo: connect", "err", err)
		panic(err)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = mongoClient.Close(shutdownCtx)
	}()
	if err := mongoClient.EnsureIndexes(ctx); err != nil {
		log.Error("mongo: indexes", "err", err)
		panic(err)
	}

	// ── Core services ──────────────────────────────────────────────────
	convRepo := conversation.NewRepository(mongoClient.Conversations())
	msgRepo := message.NewRepository(mongoClient.Messages())
	availRepo := conversation.NewAvailabilityRepository(mongoClient.StaffAvailability())
	auditRepo := conversation.NewAuditRepository(mongoClient.Audit())

	// OTP: repo + mailer + service. SendGrid is the primary provider with
	// Resend as the always-on fallback when both keys are set. When neither
	// is set we fall back to a stdout log mailer so the service remains
	// bootable.
	otpRepo, err := otp.NewRepository(ctx, mongoClient.OTP())
	if err != nil {
		log.Error("otp: repo", "err", err)
		panic(err)
	}
	var mail mailer.Mailer
	switch {
	case cfg.SendgridAPIKey != "" && cfg.ResendAPIKey != "":
		mail = &mailer.FallbackMailer{
			Primary:  mailer.NewSendgridMailer(cfg.SendgridAPIKey, cfg.OTPFromEmail, cfg.OTPFromName),
			Fallback: mailer.NewResendMailer(cfg.ResendAPIKey, cfg.OTPFromEmail, cfg.OTPFromName),
			Logger:   log,
		}
	case cfg.SendgridAPIKey != "":
		log.Warn("otp: RESEND_API_KEY not set — SendGrid only, no fallback provider")
		mail = mailer.NewSendgridMailer(cfg.SendgridAPIKey, cfg.OTPFromEmail, cfg.OTPFromName)
	case cfg.ResendAPIKey != "":
		log.Warn("otp: SENDGRID_API_KEY not set — using Resend as the only provider")
		mail = mailer.NewResendMailer(cfg.ResendAPIKey, cfg.OTPFromEmail, cfg.OTPFromName)
	default:
		log.Warn("otp: SENDGRID_API_KEY and RESEND_API_KEY not set — falling back to stdout log mailer (codes will not reach real inboxes)")
		mail = &mailer.LogMailer{Logger: log}
	}
	otpSvc := otp.NewService(otpRepo, mail, otp.Config{
		TTL:            time.Duration(cfg.OTPCodeTTL) * time.Second,
		MaxAttempts:    cfg.OTPMaxAttempts,
		ResendCooldown: time.Duration(cfg.OTPResendCooldown) * time.Second,
	})

	signer := session.NewSigner(cfg.CustomerSessionSecret, 30*24*time.Hour)
	// Short TTL — the client opens the WS immediately after minting the
	// ticket, so 2 minutes is plenty and keeps the replay window tight.
	ticketSigner := session.NewTicketSigner(cfg.CustomerSessionSecret, 2*time.Minute)
	h := hub.New(log)

	// ── HTTP server ────────────────────────────────────────────────────
	r := httpserver.New(cfg.Env, log, cfg.CORSAllowedOrigins)

	// Storefront REST routes — all run the CustomerContext middleware so
	// the Next.js proxy's tenant/store/internal-auth headers gate entry.
	storefront := r.Group("/api/v1/storefront/otto")
	storefront.Use(auth.CustomerContext(cfg.InternalAuthSecret))
	storefrontHandler := conversation.NewStorefrontHandler(conversation.StorefrontDeps{
		Conversations: convRepo,
		Availability:  availRepo,
		Audit:         auditRepo,
		Messages:      msgRepo,
		Hub:           h,
		Signer:        signer,
		Tickets:       ticketSigner,
		OTP:           otpSvc,
		CookieName:    cfg.CustomerSessionCookie,
		CookieDomain:  cfg.CustomerCookieDomain,
		CookieSecure:  cfg.CustomerCookieSecure,
		Logger:        log,
	})
	storefrontHandler.Register(storefront)
	otp.NewHandler(otpSvc, log).Register(storefront)

	// Admin REST routes — StaffAuth + StoreResolver enforce identity and
	// scope.
	admin := r.Group("/api/v1/admin/otto")
	admin.Use(auth.StaffAuth(cfg.InternalAuthSecret), auth.StoreResolver())
	adminHandler := conversation.NewAdminHandler(conversation.AdminDeps{
		Conversations: convRepo,
		Availability:  availRepo,
		Audit:         auditRepo,
		Messages:      msgRepo,
		Hub:           h,
		Tickets:       ticketSigner,
		Logger:        log,
	})
	adminHandler.Register(admin)

	// Platform super-admin (cross-tenant) analytics — internal-auth ONLY,
	// no store scope. tesserix-home's admin proxies this for the
	// /admin/analytics/support view; PlatformAuth denies on an empty secret
	// so this cross-tenant surface never falls open.
	platform := r.Group("/api/v1/platform/otto")
	platform.Use(auth.PlatformAuth(cfg.InternalAuthSecret))
	adminHandler.RegisterPlatform(platform)

	// Inactivity sweeper — closes active conversations the customer
	// has gone quiet on for 15 min. One goroutine per process; no
	// leader election needed at v1 volume, the CloseForInactivity
	// CAS handles multi-pod races.
	sweeper := &conversation.InactivitySweeper{
		Conversations:    convRepo,
		Availability:     availRepo,
		Audit:            auditRepo,
		Messages:         msgRepo,
		Hub:              h,
		Logger:           log,
		InactivityWindow: 15 * time.Minute,
		SweepInterval:    60 * time.Second,
	}
	go sweeper.Run(ctx)

	// Mongo change-stream watcher — rebroadcasts inserts on `messages`
	// and updates on `conversations` to the WebSocket hub so writes
	// from slm-router (assistant replies, status flips to active) hit
	// the customer browser in real time. Without this the widget only
	// sees those changes on a manual refresh.
	watcher := &changestream.Watcher{
		DB:     mongoClient.DB(),
		Hub:    h,
		Logger: log,
	}
	go watcher.Run(ctx)

	// WebSocket routes are deliberately mounted on a no-middleware group:
	// Istio routes /api/v1/otto/.../ws directly to Otto, bypassing the
	// Next.js proxies that would otherwise inject our auth headers. Ticket
	// auth takes over instead — see session.TicketSigner.
	adminWS := r.Group("/api/v1/admin/otto")
	adminHandler.RegisterWS(adminWS)
	storefrontHandler.RegisterWS(r.Group("/api/v1/storefront/otto"))

	if err := httpserver.Run(ctx, cfg.HTTPPort, r, log); err != nil {
		log.Error("http", "err", err)
		panic(err)
	}
}
