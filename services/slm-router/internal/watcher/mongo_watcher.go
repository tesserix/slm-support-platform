package watcher

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// Mongo wraps a *mongo.Client and a target collection. Constructed once
// at boot and shared across all watcher goroutines.
type Mongo struct {
	client     *mongo.Client
	database   string
	collection string
	logger     *slog.Logger
}

// NewMongo dials the URI, pings to confirm connectivity, and returns a
// ready-to-Start watcher. The collection is "messages" (Otto's message
// collection); database is "otto" by default.
func NewMongo(ctx context.Context, uri, database, collection string, logger *slog.Logger) (*Mongo, error) {
	if collection == "" {
		collection = "messages"
	}
	clientOpts := options.Client().ApplyURI(uri)
	client, err := mongo.Connect(ctx, clientOpts)
	if err != nil {
		return nil, fmt.Errorf("mongo connect: %w", err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := client.Ping(pingCtx, nil); err != nil {
		return nil, fmt.Errorf("mongo ping: %w", err)
	}
	return &Mongo{
		client:     client,
		database:   database,
		collection: collection,
		logger:     logger,
	}, nil
}

// Close releases the Mongo connection. Call on shutdown.
func (w *Mongo) Close(ctx context.Context) error {
	return w.client.Disconnect(ctx)
}

// Start opens a change stream and pushes CustomerMessage events to out
// until ctx is cancelled. On a stream error other than context-cancelled,
// it logs and reconnects after a backoff. The caller is expected to
// drain `out` quickly — backpressure here means missed messages.
//
// The pipeline filters to:
//   - operationType = "insert"
//   - fullDocument.sender_type = "customer"
// The first insert on a fresh conversation also lands here, which is
// what we want (it's the customer's first message).
func (w *Mongo) Start(ctx context.Context, out chan<- CustomerMessage) {
	backoff := time.Second
	const maxBackoff = 30 * time.Second

	for {
		if err := ctx.Err(); err != nil {
			w.logger.Info("watcher context cancelled, stopping")
			return
		}
		if err := w.runOnce(ctx, out); err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return
			}
			w.logger.Error("change stream error, reconnecting",
				"err", err.Error(),
				"backoff", backoff,
			)
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return
			}
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
			continue
		}
		// runOnce only returns nil when ctx is cancelled.
		return
	}
}

func (w *Mongo) runOnce(ctx context.Context, out chan<- CustomerMessage) error {
	coll := w.client.Database(w.database).Collection(w.collection)
	pipeline := mongo.Pipeline{
		bson.D{{Key: "$match", Value: bson.D{
			{Key: "operationType", Value: "insert"},
			{Key: "fullDocument.sender_type", Value: "customer"},
		}}},
	}
	streamOpts := options.ChangeStream().SetFullDocument(options.UpdateLookup)
	stream, err := coll.Watch(ctx, pipeline, streamOpts)
	if err != nil {
		return fmt.Errorf("open change stream: %w", err)
	}
	defer stream.Close(ctx)

	w.logger.Info("change stream open",
		"database", w.database,
		"collection", w.collection,
	)

	for stream.Next(ctx) {
		var raw struct {
			FullDocument CustomerMessage `bson:"fullDocument"`
		}
		if err := stream.Decode(&raw); err != nil {
			w.logger.Warn("change stream decode failed", "err", err.Error())
			continue
		}
		select {
		case out <- raw.FullDocument:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if err := stream.Err(); err != nil {
		return fmt.Errorf("change stream iteration: %w", err)
	}
	return ctx.Err()
}
