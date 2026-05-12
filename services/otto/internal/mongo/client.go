package mongo

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

const (
	CollConversations     = "conversations"
	CollMessages          = "messages"
	CollSessions          = "sessions"
	CollOTP               = "otp_codes"
	CollStaffAvailability = "staff_availability"
	CollAudit             = "otto_audit"
)

// Client wraps a mongo.Database for the otto service and provides typed
// collection accessors.
type Client struct {
	client *mongo.Client
	db     *mongo.Database
}

// Connect dials Mongo with a short retry loop (the StatefulSet may still be
// coming up when the first pod starts).
func Connect(ctx context.Context, url, database string) (*Client, error) {
	var (
		client *mongo.Client
		err    error
	)
	for attempt := 1; attempt <= 10; attempt++ {
		dialCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		client, err = mongo.Connect(dialCtx, options.Client().ApplyURI(url))
		cancel()
		if err == nil {
			pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			err = client.Ping(pingCtx, nil)
			cancel()
			if err == nil {
				return &Client{client: client, db: client.Database(database)}, nil
			}
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Duration(attempt) * time.Second):
		}
	}
	return nil, fmt.Errorf("mongo: connect after 10 attempts: %w", err)
}

// Close disconnects the underlying client.
func (c *Client) Close(ctx context.Context) error {
	return c.client.Disconnect(ctx)
}

// DB returns the raw *mongo.Database so repos can resolve collections.
func (c *Client) DB() *mongo.Database { return c.db }

func (c *Client) Conversations() *mongo.Collection     { return c.db.Collection(CollConversations) }
func (c *Client) Messages() *mongo.Collection          { return c.db.Collection(CollMessages) }
func (c *Client) Sessions() *mongo.Collection          { return c.db.Collection(CollSessions) }
func (c *Client) OTP() *mongo.Collection               { return c.db.Collection(CollOTP) }
func (c *Client) StaffAvailability() *mongo.Collection { return c.db.Collection(CollStaffAvailability) }
func (c *Client) Audit() *mongo.Collection             { return c.db.Collection(CollAudit) }

// EnsureIndexes creates the indexes the service relies on for tenant
// isolation + fast inbox queries. Idempotent — safe to run every boot.
func (c *Client) EnsureIndexes(ctx context.Context) error {
	convIdx := []mongo.IndexModel{
		{
			// Inbox: "pending + active conversations for this store, newest first".
			Keys: bson.D{
				{Key: "tenant_id", Value: 1},
				{Key: "store_id", Value: 1},
				{Key: "status", Value: 1},
				{Key: "last_message_at", Value: -1},
			},
			Options: options.Index().SetName("tenant_store_status_last_msg"),
		},
		{
			// Customer self-lookup: "give me the thread I started in this session".
			Keys: bson.D{
				{Key: "customer.session_token", Value: 1},
				{Key: "status", Value: 1},
			},
			Options: options.Index().SetName("customer_session_status"),
		},
		{
			// Staff "my conversations" filter.
			Keys: bson.D{
				{Key: "tenant_id", Value: 1},
				{Key: "assignee.user_id", Value: 1},
				{Key: "status", Value: 1},
			},
			Options: options.Index().SetName("tenant_assignee_status"),
		},
		{
			// Inactivity sweeper — scan active convs sorted by the
			// customer's last message. Small index, hit every 60s.
			Keys: bson.D{
				{Key: "status", Value: 1},
				{Key: "last_customer_message_at", Value: 1},
			},
			Options: options.Index().SetName("status_last_customer_msg"),
		},
	}
	if _, err := c.Conversations().Indexes().CreateMany(ctx, convIdx); err != nil {
		return fmt.Errorf("indexes: conversations: %w", err)
	}

	// staff_availability — queried by (tenant, store, staff) for
	// upsert/fetch and by (tenant, store, available, current_case_id)
	// for the count.
	availIdx := []mongo.IndexModel{
		{
			Keys: bson.D{
				{Key: "tenant_id", Value: 1},
				{Key: "store_id", Value: 1},
				{Key: "staff_id", Value: 1},
			},
			Options: options.Index().SetName("tenant_store_staff").SetUnique(true),
		},
		{
			Keys: bson.D{
				{Key: "tenant_id", Value: 1},
				{Key: "store_id", Value: 1},
				{Key: "available", Value: 1},
			},
			Options: options.Index().SetName("tenant_store_available"),
		},
	}
	if _, err := c.StaffAvailability().Indexes().CreateMany(ctx, availIdx); err != nil {
		return fmt.Errorf("indexes: staff_availability: %w", err)
	}

	// otto_audit — two queries: timeline for a single case
	// (conversation_id sort by at asc), and recent-tail for a store.
	auditIdx := []mongo.IndexModel{
		{
			Keys: bson.D{
				{Key: "tenant_id", Value: 1},
				{Key: "store_id", Value: 1},
				{Key: "conversation_id", Value: 1},
				{Key: "at", Value: 1},
			},
			Options: options.Index().SetName("tenant_store_conv_at"),
		},
		{
			Keys: bson.D{
				{Key: "tenant_id", Value: 1},
				{Key: "store_id", Value: 1},
				{Key: "at", Value: -1},
			},
			Options: options.Index().SetName("tenant_store_at"),
		},
	}
	if _, err := c.Audit().Indexes().CreateMany(ctx, auditIdx); err != nil {
		return fmt.Errorf("indexes: otto_audit: %w", err)
	}

	msgIdx := []mongo.IndexModel{
		{
			// Thread pagination.
			Keys: bson.D{
				{Key: "conversation_id", Value: 1},
				{Key: "created_at", Value: 1},
			},
			Options: options.Index().SetName("conversation_created"),
		},
	}
	if _, err := c.Messages().Indexes().CreateMany(ctx, msgIdx); err != nil {
		return fmt.Errorf("indexes: messages: %w", err)
	}

	sessIdx := []mongo.IndexModel{
		{
			Keys:    bson.D{{Key: "token", Value: 1}},
			Options: options.Index().SetName("token_unique").SetUnique(true),
		},
		{
			Keys: bson.D{
				{Key: "tenant_id", Value: 1},
				{Key: "store_id", Value: 1},
			},
			Options: options.Index().SetName("tenant_store"),
		},
	}
	if _, err := c.Sessions().Indexes().CreateMany(ctx, sessIdx); err != nil {
		return fmt.Errorf("indexes: sessions: %w", err)
	}
	return nil
}
