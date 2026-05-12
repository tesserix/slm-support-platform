// Command slm-router is the gateway that watches Otto's conversations
// MongoDB for new customer messages, looks up per-tenant routing,
// retrieves RAG context, calls slm-inference, executes MCP tool calls,
// and posts the AI reply back as a SenderAssistant message.
//
// This file is the boot wiring only. The real work lives in the
// orchestrator package (added in subsequent commits).
package main

import (
	"context"
	"log"
	"os/signal"
	"syscall"

	"github.com/tesserix/slm-support-platform/services/slm-router/internal/config"
	"github.com/tesserix/slm-support-platform/services/slm-router/internal/httpserver"
	"github.com/tesserix/slm-support-platform/services/slm-router/internal/logger"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config load: %v", err)
	}

	lg := logger.New(cfg.Env.Env)
	lg.Info("slm-router booting",
		"http_port", cfg.Env.HTTPPort,
		"tenants_configured", len(cfg.Routes.Products),
	)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	srv := httpserver.New(cfg.Env.HTTPPort, lg)
	// Skeleton boot — the orchestrator (Mongo watcher + RAG + SLM + MCP)
	// lands in the next commit (D5/D6). Until then we mark ready so the
	// Kubernetes readiness probe succeeds against the skeleton image.
	srv.SetReady(true)

	if err := srv.Run(ctx); err != nil {
		lg.Error("http server exited with error", "err", err.Error())
	}
	lg.Info("slm-router stopped cleanly")
}
