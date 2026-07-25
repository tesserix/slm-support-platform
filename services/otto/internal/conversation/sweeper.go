package conversation

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/tesserix/slm-support-platform/services/otto/internal/event"
	"github.com/tesserix/slm-support-platform/services/otto/internal/hub"
	"github.com/tesserix/slm-support-platform/services/otto/internal/message"
)

// InactivitySweeper closes active conversations whose customer has
// gone quiet for InactivityWindow. Runs as a background goroutine
// started from main. Ticks every SweepInterval; the default cadence
// (60s check, 15min window) matches the product spec.
type InactivitySweeper struct {
	Conversations    *Repository
	Availability     *AvailabilityRepository
	Audit            *AuditRepository
	Messages         *message.Repository
	Hub              *hub.Hub
	Logger           *slog.Logger
	InactivityWindow time.Duration
	SweepInterval    time.Duration
	// BatchLimit caps how many conversations we process per tick so
	// a spike can't swamp the DB. Defaults to 200.
	BatchLimit int64
}

// Run blocks until ctx is cancelled, ticking every SweepInterval.
// Safe to call once per process; don't run multiple sweepers against
// the same collection or you'll double-close edge cases.
func (s *InactivitySweeper) Run(ctx context.Context) {
	window := s.InactivityWindow
	if window <= 0 {
		window = 15 * time.Minute
	}
	interval := s.SweepInterval
	if interval <= 0 {
		interval = 60 * time.Second
	}
	limit := s.BatchLimit
	if limit <= 0 {
		limit = 200
	}

	s.Logger.Info("otto: inactivity sweeper started",
		slog.Duration("window", window),
		slog.Duration("interval", interval),
	)

	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			s.tick(ctx, now, window, limit)
		}
	}
}

func (s *InactivitySweeper) tick(ctx context.Context, now time.Time, window time.Duration, limit int64) {
	cutoff := now.Add(-window)
	convos, err := s.Conversations.ListInactive(ctx, cutoff, limit)
	if err != nil {
		s.Logger.Error("otto: sweeper list", "err", err)
		return
	}
	for i := range convos {
		conv := &convos[i]
		updated, err := s.Conversations.CloseForInactivity(ctx, conv.TenantID, conv.StoreID, conv.ID)
		if err != nil {
			// ErrNotFound means the case flipped state between list
			// and update — harmless, skip.
			continue
		}
		// Free the staff member back to the pool.
		if s.Availability != nil && updated.Assignee != nil {
			if releaseErr := s.Availability.ReleaseCase(ctx, updated.TenantID, updated.StoreID, updated.Assignee.UserID); releaseErr != nil {
				s.Logger.Warn("otto: sweeper release availability", "err", releaseErr)
			}
		}
		// Post a system message so both sides see WHY the case
		// closed — better than a silent status flip.
		sys := &message.Message{
			ID:             uuid.NewString(),
			ConversationID: updated.ID,
			TenantID:       updated.TenantID,
			StoreID:        updated.StoreID,
			SenderType:     message.SenderSystem,
			SenderName:     "Otto",
			Body:           "This case was closed automatically because there was no customer activity for 15 minutes. If you still need help, start a new chat.",
			CreatedAt:      now.UTC(),
		}
		if err := s.Messages.Insert(ctx, sys); err != nil {
			s.Logger.Warn("otto: sweeper system msg", "err", err)
		}
		s.Hub.Broadcast(hub.RoomConversation(updated.ID), hub.Envelope{
			Type:    event.TypeMessageCreated,
			Payload: map[string]any{"message": sys},
		})
		s.Hub.Broadcast(hub.RoomConversation(updated.ID), hub.Envelope{
			Type:    event.TypeConversationClosed,
			Payload: map[string]any{"conversation": updated},
		})
		s.Hub.BroadcastInbox(updated.TenantID, updated.StoreID, hub.Envelope{
			Type:    event.TypeConversationClosed,
			Payload: map[string]any{"conversation": updated},
		})
		if s.Audit != nil {
			if auditErr := s.Audit.Emit(ctx, AuditEvent{
				TenantID:       updated.TenantID,
				StoreID:        updated.StoreID,
				ConversationID: updated.ID,
				CaseID:         updated.CaseID,
				Action:         AuditCaseClosedInactive,
				Actor:          Actor{Type: "system", Name: "Otto sweeper"},
			}); auditErr != nil {
				s.Logger.Warn("otto: audit sweeper close", "err", auditErr)
			}
		}
	}
	if len(convos) > 0 {
		s.Logger.Info("otto: inactivity sweep",
			slog.Int("closed", len(convos)),
			slog.Time("cutoff", cutoff),
		)
	}
}
