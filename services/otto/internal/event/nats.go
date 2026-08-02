// NATS bridge for the staff queue. The changestream watcher derives
// queue-lifecycle transitions (created / escalated / accepted / closed)
// and hands them here; product backends (e.g. homechef-api) consume the
// subjects durably via their own JetStream streams to drive staff
// notifications and SLA workflows. Publishing is best-effort — the
// durable paths are slm-router's escalation hook and the admin inbox
// WebSocket; NATS being down must never affect chat traffic.
package event

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
)

// Queue lifecycle event names — the last token of the NATS subject.
const (
	QueueCreated   = "created"   // conversation opened, waiting in queue
	QueueEscalated = "escalated" // AI handed off to a human (needs_human flip)
	QueueAccepted  = "accepted"  // staff accepted the thread
	QueueClosed    = "closed"    // thread closed (staff, customer or sweeper)
)

// QueueEvent is the wire payload published to
// <prefix>.<tenant>.<event> (default prefix "otto.support").
type QueueEvent struct {
	Event          string    `json:"event"`
	TenantID       string    `json:"tenant_id"`
	StoreID        string    `json:"store_id"`
	ConversationID string    `json:"conversation_id"`
	CaseID         string    `json:"case_id,omitempty"`
	Status         string    `json:"status"`
	NeedsHuman     bool      `json:"needs_human"`
	Subject        string    `json:"subject,omitempty"`
	IntakeReason   string    `json:"intake_reason,omitempty"`
	IntakeStatus   string    `json:"intake_status,omitempty"`
	CustomerUserID string    `json:"customer_user_id,omitempty"`
	CustomerName   string    `json:"customer_name,omitempty"`
	CustomerEmail  string    `json:"customer_email,omitempty"`
	AssigneeName   string    `json:"assignee_name,omitempty"`
	OccurredAt     time.Time `json:"occurred_at"`
}

// NATSPublisher publishes QueueEvents. A nil publisher is safe to call —
// every method no-ops — so callers never need nil checks.
type NATSPublisher struct {
	nc     *nats.Conn
	prefix string
	log    *slog.Logger
}

// NewNATSPublisher connects to NATS. Returns (nil, nil) when url is
// empty: the feature is dark until OTTO_NATS_URL is configured.
func NewNATSPublisher(url, prefix string, log *slog.Logger) (*NATSPublisher, error) {
	url = strings.TrimSpace(url)
	if url == "" {
		return nil, nil
	}
	if prefix == "" {
		prefix = "otto.support"
	}
	nc, err := nats.Connect(url,
		nats.Name("support-platform-otto"),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(2*time.Second),
	)
	if err != nil {
		return nil, fmt.Errorf("nats connect: %w", err)
	}
	return &NATSPublisher{nc: nc, prefix: prefix, log: log}, nil
}

// Publish sends one queue event. Best-effort: failures are logged, never
// returned — queue notifications must not affect the chat data path.
// The Nats-Msg-Id header lets a capturing JetStream stream dedup the
// same transition published by multiple otto replicas (each replica
// runs its own change-stream watcher and sees the same Mongo event).
func (p *NATSPublisher) Publish(ev QueueEvent) {
	if p == nil || p.nc == nil {
		return
	}
	ev.OccurredAt = ev.OccurredAt.UTC()
	body, err := json.Marshal(ev)
	if err != nil {
		return
	}
	msg := nats.NewMsg(fmt.Sprintf("%s.%s.%s", p.prefix, subjectToken(ev.TenantID), ev.Event))
	msg.Data = body
	msg.Header.Set("Nats-Msg-Id",
		fmt.Sprintf("%s:%s:%s", ev.ConversationID, ev.Event, ev.OccurredAt.Format(time.RFC3339)))
	if err := p.nc.PublishMsg(msg); err != nil && p.log != nil {
		p.log.Warn("nats queue event publish failed", "subject", msg.Subject, "err", err)
	}
}

// Close drains the connection on shutdown.
func (p *NATSPublisher) Close() {
	if p == nil || p.nc == nil {
		return
	}
	_ = p.nc.Drain()
}

// subjectToken makes a tenant id safe as a NATS subject token.
func subjectToken(s string) string {
	if s == "" {
		return "unknown"
	}
	return strings.Map(func(r rune) rune {
		switch r {
		case '.', ' ', '*', '>':
			return '_'
		}
		return r
	}, s)
}
