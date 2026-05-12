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
	e := New(config.EscalationPolicy{ConfidenceThreshold: 0.7})
	d := e.CheckPostInference(0.4, 0)
	if !d.Escalate || d.Reason != ReasonLowConfidence {
		t.Fatalf("expected low_confidence, got %+v", d)
	}
}

func TestPostInferenceToolFailures(t *testing.T) {
	e := New(config.EscalationPolicy{MaxToolFailures: 2})
	d := e.CheckPostInference(1.0, 3)
	if !d.Escalate || d.Reason != ReasonToolFailures {
		t.Fatalf("expected tool_failures, got %+v", d)
	}
}

func TestPostInferenceConfidentNoFailures(t *testing.T) {
	e := New(config.EscalationPolicy{
		ConfidenceThreshold: 0.6,
		MaxToolFailures:     2,
	})
	d := e.CheckPostInference(0.9, 0)
	if d.Escalate {
		t.Fatalf("expected no escalation, got %+v", d)
	}
}
