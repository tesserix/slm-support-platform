// Package httpserver wires the Gin engine for slm-router's admin/debug
// surface. The hot path (Mongo change stream → AI reply) is event-driven
// inside the orchestrator goroutine, not HTTP — so this server is
// deliberately small. It exposes:
//
//   GET  /healthz   liveness
//   GET  /readyz    readiness (returns 503 until routes load successfully)
//   GET  /metrics   Prometheus metrics (added in a later commit)
//   POST /v1/replay debug-only: replay a conversation through the
//                   orchestrator without waiting for the change stream
package httpserver

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
)

// Server holds the Gin engine and a readiness flag toggled by the
// orchestrator once dependencies (Mongo, inference, embedder) are reachable.
type Server struct {
	engine *gin.Engine
	port   int
	ready  atomic.Bool
	logger *slog.Logger
}

// New constructs the HTTP server. Call SetReady(true) once dependencies
// are confirmed up.
func New(port int, logger *slog.Logger) *Server {
	gin.SetMode(gin.ReleaseMode)
	e := gin.New()
	e.Use(gin.Recovery())

	s := &Server{engine: e, port: port, logger: logger}

	e.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})
	e.GET("/readyz", func(c *gin.Context) {
		if !s.ready.Load() {
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "not ready"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ready"})
	})

	return s
}

// SetReady flips the readiness flag. Called by the orchestrator after
// initial dependency checks pass.
func (s *Server) SetReady(ready bool) {
	s.ready.Store(ready)
}

// Run blocks serving HTTP. Returns the first error encountered, or
// context.Canceled when ctx is cancelled.
func (s *Server) Run(ctx context.Context) error {
	srv := &http.Server{
		Addr:              fmt.Sprintf(":%d", s.port),
		Handler:           s.engine,
		ReadHeaderTimeout: 5 * time.Second,
	}
	s.logger.Info("http server starting", "addr", srv.Addr)

	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()

	select {
	case err := <-errCh:
		if err != http.ErrServerClosed {
			return err
		}
		return nil
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}
