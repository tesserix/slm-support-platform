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
	"github.com/tesserix/slm-support-platform/services/slm-router/internal/watcher"
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

	// Mongo watcher — opens a change stream on Otto's `messages`
	// collection and yields new customer messages into a channel the
	// orchestrator drains (orchestrator lands in D6).
	w, err := watcher.NewMongo(ctx, cfg.Env.MongoURI, cfg.Env.MongoDatabase, "messages", lg)
	if err != nil {
		lg.Error("mongo watcher init failed", "err", err.Error())
		log.Fatalf("mongo watcher: %v", err)
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*1e9) // 5s
		defer cancel()
		_ = w.Close(closeCtx)
	}()

	events := make(chan watcher.CustomerMessage, 64)
	go w.Start(ctx, events)
	// Drain events until orchestrator is wired (D6). For now this loop
	// just logs that we received them, proving the watcher works end to
	// end against a real Otto Mongo instance.
	go func() {
		for ev := range events {
			lg.Info("customer message received",
				"tenant_id", ev.TenantID,
				"conversation_id", ev.ConversationID,
				"message_id", ev.MessageID,
			)
		}
	}()

	srv := httpserver.New(cfg.Env.HTTPPort, lg)
	srv.SetReady(true)

	if err := srv.Run(ctx); err != nil {
		lg.Error("http server exited with error", "err", err.Error())
	}
	close(events)
	lg.Info("slm-router stopped cleanly")
}
