// Package escalation decides when to flip a conversation's NeedsHuman
// to true. Keep this logic in one place — it's the only thing standing
// between a confident AI and an angry customer.
//
// Three independent triggers, OR-combined:
//
//  1. Confidence below threshold (heuristic on model output shape).
//  2. Customer's message contains an escalation keyword.
//  3. Tool calls failed more than max_tool_failures times in this turn.
//
// Each trigger returns a Reason so the assistant message that ships
// to the customer can explain why we're handing them off.
package escalation

import (
	"strings"

	"github.com/tesserix/slm-support-platform/services/slm-router/internal/config"
)

// Reason is the why behind an escalation decision.
type Reason string

const (
	ReasonNone           Reason = ""
	ReasonLowConfidence  Reason = "low_confidence"
	ReasonKeyword        Reason = "keyword_match"
	ReasonToolFailures   Reason = "tool_failures"
	ReasonCustomerAsked  Reason = "customer_asked_for_human"
)

// Decision summarises whether to escalate and why.
type Decision struct {
	Escalate bool
	// OfferHandoff is set when the answer was low-confidence but we're
	// still inside the "let the bot try first" window (customer turn
	// count below MinTurns). The orchestrator should NOT mark the
	// conversation for a human; instead it appends a soft offer so the
	// customer knows they can ask for one. Mutually exclusive with
	// Escalate.
	OfferHandoff bool
	Reason       Reason
	// Matched is populated for keyword matches (the actual word seen)
	// and for low-confidence (the score), to help humans triaging the
	// inbox understand the AI's reasoning.
	Matched string
}

// Evaluator applies one tenant's escalation policy.
type Evaluator struct {
	cfg config.EscalationPolicy
}

// New constructs an Evaluator. Pass the per-tenant config.
func New(cfg config.EscalationPolicy) *Evaluator {
	return &Evaluator{cfg: cfg}
}

// PreCheckCustomerMessage looks at the customer's message BEFORE we
// invoke the model. Returns an escalation if a keyword matches (we
// don't want the model trying to handle a "lawyer" message at all)
// or if the customer explicitly asked for a human.
func (e *Evaluator) PreCheckCustomerMessage(text string) Decision {
	lower := strings.ToLower(text)

	// Explicit human-request phrases. Hard-coded; not config-driven
	// because they don't vary by product.
	for _, phrase := range humanRequestPhrases {
		if strings.Contains(lower, phrase) {
			return Decision{Escalate: true, Reason: ReasonCustomerAsked, Matched: phrase}
		}
	}

	for _, kw := range e.cfg.Keywords {
		if kw == "" {
			continue
		}
		if strings.Contains(lower, strings.ToLower(kw)) {
			return Decision{Escalate: true, Reason: ReasonKeyword, Matched: kw}
		}
	}
	return Decision{}
}

// CheckPostInference evaluates the model's output after generation.
// confidence is the orchestrator's heuristic score (0–1); toolFailures
// is the count of failed tool calls in this turn; customerTurns is how
// many messages the customer has sent in this conversation including the
// current one.
//
// Low confidence does NOT immediately escalate when MinTurns is set and
// the customer hasn't reached that many turns yet — we give the bot a
// few exchanges to actually resolve the query and only surface a soft
// handoff offer. Tool-failure escalations are not gated (a broken tool
// won't fix itself by waiting). Hard keyword / explicit-human
// escalations are handled earlier in PreCheckCustomerMessage and are
// likewise never gated.
func (e *Evaluator) CheckPostInference(confidence float64, toolFailures int, customerTurns int) Decision {
	if e.cfg.ConfidenceThreshold > 0 && confidence < e.cfg.ConfidenceThreshold {
		if e.cfg.MinTurns > 0 && customerTurns < e.cfg.MinTurns {
			return Decision{
				OfferHandoff: true,
				Reason:       ReasonLowConfidence,
				Matched:      formatConfidence(confidence),
			}
		}
		return Decision{
			Escalate: true,
			Reason:   ReasonLowConfidence,
			Matched:  formatConfidence(confidence),
		}
	}
	if e.cfg.MaxToolFailures > 0 && toolFailures > e.cfg.MaxToolFailures {
		return Decision{Escalate: true, Reason: ReasonToolFailures}
	}
	return Decision{}
}

// humanRequestPhrases are universal phrases that mean "give me a human"
// regardless of product. Tenant-specific keywords (refund, chargeback,
// etc.) come from EscalationPolicy.Keywords.
var humanRequestPhrases = []string{
	"speak to a human",
	"talk to a human",
	"real person",
	"human agent",
	"customer service representative",
}

func formatConfidence(c float64) string {
	// Three significant figures is enough for human-readable triage.
	return strings_FormatFloat(c)
}

// strings_FormatFloat is split out so tests can verify the rendering
// stays stable as we tune the heuristic.
func strings_FormatFloat(c float64) string {
	// Local impl to avoid pulling strconv just for a fixed format —
	// keeps the package's dependency surface small.
	return formatFloatPrec(c)
}
