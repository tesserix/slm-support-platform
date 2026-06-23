package conversation

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// PlatformStats is a cross-tenant rollup of support conversations for the
// platform super-admin analytics view (tesserix-home → /admin/analytics/
// support). Unlike the staff inbox this is NOT scoped to a single
// tenant/store — it aggregates across every tenant otto serves.
type PlatformStats struct {
	Total      int64            `json:"total"`
	Open       int64            `json:"open"` // pending + active
	ByStatus   map[string]int64 `json:"by_status"`
	ByReason   map[string]int64 `json:"by_reason"`
	ByTenant   map[string]int64 `json:"by_tenant"`
	Escalated  int64            `json:"escalated"`   // needs_human=true (handed to a person)
	AIResolved int64            `json:"ai_resolved"` // closed without ever escalating
	// AvgResolutionSeconds is the mean (closed_at - created_at) over closed
	// conversations that carry a closed_at.
	AvgResolutionSeconds float64 `json:"avg_resolution_seconds"`
	// CSAT is the mean post-case call_rating (1-5) over answered surveys.
	CSAT float64 `json:"csat"`
	// ResolvedRate is the share of feedback responses where the customer
	// said their query was resolved.
	ResolvedRate  float64 `json:"resolved_rate"`
	FeedbackCount int64   `json:"feedback_count"`
}

// statRow is the minimal projection PlatformStats scans — keeping the
// payload tiny so a cross-tenant scan stays cheap even as volume grows.
type statRow struct {
	TenantID   string     `bson:"tenant_id"`
	Status     string     `bson:"status"`
	NeedsHuman bool       `bson:"needs_human"`
	CreatedAt  time.Time  `bson:"created_at"`
	ClosedAt   *time.Time `bson:"closed_at"`
	Intake     *struct {
		Reason string `bson:"reason"`
	} `bson:"intake"`
	Feedback *struct {
		CallRating    int  `bson:"call_rating"`
		QueryResolved bool `bson:"query_resolved"`
	} `bson:"feedback"`
}

// PlatformStats scans every conversation (projected to the few fields the
// rollup needs) and folds the metrics in Go. A projected Find keeps this
// simple and correct; if the collection grows large this can move to a
// server-side $facet aggregation without changing the response shape.
func (r *Repository) PlatformStats(ctx context.Context) (*PlatformStats, error) {
	proj := options.Find().SetProjection(bson.M{
		"tenant_id":               1,
		"status":                  1,
		"needs_human":             1,
		"created_at":              1,
		"closed_at":               1,
		"intake.reason":           1,
		"feedback.call_rating":    1,
		"feedback.query_resolved": 1,
	})
	cur, err := r.coll.Find(ctx, bson.M{}, proj)
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)

	s := &PlatformStats{
		ByStatus: map[string]int64{},
		ByReason: map[string]int64{},
		ByTenant: map[string]int64{},
	}
	var resSum float64
	var resN int64
	var csatSum float64
	var csatN int64
	var resolvedYes int64

	for cur.Next(ctx) {
		var row statRow
		if err := cur.Decode(&row); err != nil {
			return nil, err
		}
		s.Total++
		if row.Status != "" {
			s.ByStatus[row.Status]++
		}
		if row.TenantID != "" {
			s.ByTenant[row.TenantID]++
		}
		if row.Status == "pending" || row.Status == "active" {
			s.Open++
		}
		if row.Intake != nil && row.Intake.Reason != "" {
			s.ByReason[row.Intake.Reason]++
		}
		if row.NeedsHuman {
			s.Escalated++
		}
		if row.Status == "closed" {
			if !row.NeedsHuman {
				s.AIResolved++
			}
			if row.ClosedAt != nil {
				resSum += row.ClosedAt.Sub(row.CreatedAt).Seconds()
				resN++
			}
		}
		if row.Feedback != nil {
			s.FeedbackCount++
			if row.Feedback.CallRating > 0 {
				csatSum += float64(row.Feedback.CallRating)
				csatN++
			}
			if row.Feedback.QueryResolved {
				resolvedYes++
			}
		}
	}
	if err := cur.Err(); err != nil {
		return nil, err
	}

	if resN > 0 {
		s.AvgResolutionSeconds = resSum / float64(resN)
	}
	if csatN > 0 {
		s.CSAT = csatSum / float64(csatN)
	}
	if s.FeedbackCount > 0 {
		s.ResolvedRate = float64(resolvedYes) / float64(s.FeedbackCount)
	}
	return s, nil
}
