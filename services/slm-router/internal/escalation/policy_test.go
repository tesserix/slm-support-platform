package escalation

import (
	"testing"

	"github.com/tesserix/slm-support-platform/services/slm-router/internal/config"
)

func TestPreCheckKeywordMatch(t *testing.T) {
	e := New(config.EscalationPolicy{
		ConfidenceThreshold: 0.6,
		Keywords:            []string{"refund", "lawyer"},
		MaxToolFailures:     2,
	})
	d := e.PreCheckCustomerMessage("I want a REFUND for my order")
	if !d.Escalate || d.Reason != ReasonKeyword || d.Matched != "refund" {
		t.Fatalf("expected keyword match, got %+v", d)
	}
}

func TestPreCheckCustomerAsksForHuman(t *testing.T) {
	e := New(config.EscalationPolicy{Keywords: []string{"refund"}})
	d := e.PreCheckCustomerMessage("can I please speak to a human?")
	if !d.Escalate || d.Reason != ReasonCustomerAsked {
		t.Fatalf("expected customer-asked, got %+v", d)
	}
}

func TestPreCheckNoMatch(t *testing.T) {
	e := New(config.EscalationPolicy{Keywords: []string{"refund"}})
	d := e.PreCheckCustomerMessage("when does my order arrive?")
	if d.Escalate {
		t.Fatalf("expected no escalation, got %+v", d)
	}
}

func TestPostInferenceLowConfidence(t *testing.T) {
	// No MinTurns set -> escalate on the first low-confidence turn.
	e := New(config.EscalationPolicy{ConfidenceThreshold: 0.7})
	d := e.CheckPostInference(0.4, 0, 1)
	if !d.Escalate || d.Reason != ReasonLowConfidence {
		t.Fatalf("expected low_confidence, got %+v", d)
	}
}

func TestPostInferenceToolFailures(t *testing.T) {
	e := New(config.EscalationPolicy{MaxToolFailures: 2})
	d := e.CheckPostInference(1.0, 3, 1)
	if !d.Escalate || d.Reason != ReasonToolFailures {
		t.Fatalf("expected tool_failures, got %+v", d)
	}
}

func TestPostInferenceConfidentNoFailures(t *testing.T) {
	e := New(config.EscalationPolicy{
		ConfidenceThreshold: 0.6,
		MaxToolFailures:     2,
	})
	d := e.CheckPostInference(0.9, 0, 1)
	if d.Escalate || d.OfferHandoff {
		t.Fatalf("expected no escalation, got %+v", d)
	}
}

// With MinTurns set, an early low-confidence answer should only OFFER a
// handoff (keep trying), not escalate.
func TestPostInferenceLowConfidenceGatedByMinTurns(t *testing.T) {
	e := New(config.EscalationPolicy{ConfidenceThreshold: 0.7, MinTurns: 5})
	d := e.CheckPostInference(0.4, 0, 1)
	if d.Escalate {
		t.Fatalf("turn 1: expected no escalation under MinTurns, got %+v", d)
	}
	if !d.OfferHandoff || d.Reason != ReasonLowConfidence {
		t.Fatalf("turn 1: expected soft handoff offer, got %+v", d)
	}
}

// Once the customer reaches MinTurns and we're still not confident, the
// gate lifts and we escalate to a human.
func TestPostInferenceLowConfidenceEscalatesAtMinTurns(t *testing.T) {
	e := New(config.EscalationPolicy{ConfidenceThreshold: 0.7, MinTurns: 5})
	d := e.CheckPostInference(0.4, 0, 5)
	if !d.Escalate || d.Reason != ReasonLowConfidence {
		t.Fatalf("turn 5: expected escalation, got %+v", d)
	}
	if d.OfferHandoff {
		t.Fatalf("turn 5: escalation should not also offer handoff, got %+v", d)
	}
}

// Tool failures are never gated by MinTurns — a broken tool won't fix
// itself by waiting.
func TestPostInferenceToolFailuresNotGated(t *testing.T) {
	e := New(config.EscalationPolicy{MaxToolFailures: 1, MinTurns: 5})
	d := e.CheckPostInference(1.0, 2, 1)
	if !d.Escalate || d.Reason != ReasonToolFailures {
		t.Fatalf("expected tool_failures escalation even at turn 1, got %+v", d)
	}
}
