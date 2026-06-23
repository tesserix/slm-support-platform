// Command slm-router is the gateway that watches Otto's conversations
// MongoDB for new customer messages, looks up per-tenant routing,
// retrieves RAG context, calls slm-inference, executes MCP tool calls,
// and posts the AI reply back as a SenderAssistant message.
package main

import (
	"context"
	"database/sql"
	"log"
	"os/signal"
	"syscall"
	"time"

	_ "github.com/lib/pq"
	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/mongo"
	mongoopts "go.mongodb.org/mongo-driver/mongo/options"
	"go.opentelemetry.io/contrib/instrumentation/github.com/gin-gonic/gin/otelgin"

	"github.com/tesserix/slm-support-platform/services/slm-router/internal/config"
	"github.com/tesserix/slm-support-platform/services/slm-router/internal/embed"
	"github.com/tesserix/slm-support-platform/services/slm-router/internal/httpserver"
	"github.com/tesserix/slm-support-platform/services/slm-router/internal/inference"
	"github.com/tesserix/slm-support-platform/services/slm-router/internal/logger"
	"github.com/tesserix/slm-support-platform/services/slm-router/internal/mcp"
	"github.com/tesserix/slm-support-platform/services/slm-router/internal/observability"
	"github.com/tesserix/slm-support-platform/services/slm-router/internal/orchestrator"
	"github.com/tesserix/slm-support-platform/services/slm-router/internal/otto"
	"github.com/tesserix/slm-support-platform/services/slm-router/internal/rerank"
	"github.com/tesserix/slm-support-platform/services/slm-router/internal/retriever"
	"github.com/tesserix/slm-support-platform/services/slm-router/internal/watcher"
)

// serviceName is the OpenTelemetry service.name for this binary; it also
// names the gin tracing middleware's spans.
const serviceName = "slm-router"

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

	// --- OpenTelemetry (traces + metrics over OTLP/gRPC) ---
	// No-op when OTEL_EXPORTER_OTLP_ENDPOINT is unset (local/dev).
	otelShutdown, err := observability.Init(ctx, serviceName)
	if err != nil {
		log.Fatalf("otel init: %v", err)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := otelShutdown(shutdownCtx); err != nil {
			lg.Error("otel shutdown", "err", err.Error())
		}
	}()

	// --- Mongo client (for Otto watcher + Otto writer) ---
	mongoClient, err := mongo.Connect(ctx, mongoopts.Client().ApplyURI(cfg.Env.MongoURI))
	if err != nil {
		log.Fatalf("mongo connect: %v", err)
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = mongoClient.Disconnect(closeCtx)
	}()
	mongoDB := mongoClient.Database(cfg.Env.MongoDatabase)

	// --- pgvector ---
	pg, err := sql.Open("postgres", cfg.Env.VectorDBDSN)
	if err != nil {
		log.Fatalf("pgvector open: %v", err)
	}
	pg.SetMaxOpenConns(10)
	pg.SetMaxIdleConns(2)
	defer pg.Close()
	pingCtx, pingCancel := context.WithTimeout(ctx, 5*time.Second)
	if err := pg.PingContext(pingCtx); err != nil {
		pingCancel()
		log.Fatalf("pgvector ping: %v", err)
	}
	pingCancel()

	// --- collaborators ---
	w, err := watcher.NewMongo(ctx, cfg.Env.MongoURI, cfg.Env.MongoDatabase, "messages", lg)
	if err != nil {
		log.Fatalf("mongo watcher: %v", err)
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = w.Close(closeCtx)
	}()

	deps := orchestrator.Deps{
		Config:    cfg,
		Embedder:  embed.NewHTTP(cfg.Env.EmbedderURL, embed.WithExpectedDim(384)),
		Retriever: retriever.NewPostgres(pg),
		Reranker:  rerank.NewHTTP(cfg.Env.RerankerURL),
		Inference: inference.NewHTTP(cfg.Env.InferenceURL),
		MCP:       mcp.NewHTTP(),
		Otto:      otto.NewMongoWriter(mongoDB),
		Logger:    lg,
	}
	orch, err := orchestrator.New(deps)
	if err != nil {
		log.Fatalf("orchestrator: %v", err)
	}

	// --- pipe events from watcher to orchestrator ---
	events := make(chan watcher.CustomerMessage, 128)
	go w.Start(ctx, events)
	go orch.Run(ctx, events)

	srv := httpserver.New(cfg.Env.HTTPPort, lg, gin.HandlerFunc(otelgin.Middleware(serviceName)))
	srv.SetReady(true)
	if err := srv.Run(ctx); err != nil {
		lg.Error("http server exited with error", "err", err.Error())
	}
	close(events)
	lg.Info("slm-router stopped cleanly")
}
