package orchestrator

import (
	"context"
	"testing"

	"github.com/tesserix/slm-support-platform/services/slm-router/internal/otto"
)

// The assistant answers only while nobody else owns the thread. These cases
// pin the rule down: an accepted chat, or one queued for a human, is not the
// assistant's to reply to — otherwise the customer gets an agent and a bot
// answering the same message.
func TestConversationState_HumanOwned(t *testing.T) {
	for _, tc := range []struct {
		name  string
		state otto.ConversationState
		want  bool
	}{
		{
			name:  "fresh chat, assistant answers",
			state: otto.ConversationState{Status: "pending", Exists: true},
			want:  false,
		},
		{
			name:  "assistant already replied, still its own",
			state: otto.ConversationState{Status: "active", Exists: true},
			want:  false,
		},
		{
			name:  "handoff requested, customer is waiting for a person",
			state: otto.ConversationState{Status: "pending", NeedsHuman: true, Exists: true},
			want:  true,
		},
		{
			name:  "staff accepted, they own the exchange",
			state: otto.ConversationState{Status: "active", HasAssignee: true, Exists: true},
			want:  true,
		},
		{
			name:  "staff accepted and the flag was cleared",
			state: otto.ConversationState{Status: "active", HasAssignee: true, NeedsHuman: false, Exists: true},
			want:  true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.state.HumanOwned(); got != tc.want {
				t.Fatalf("HumanOwned() = %v, want %v", got, tc.want)
			}
		})
	}
}

// stubWriter records whether the orchestrator asked who owns the thread.
type stubWriter struct {
	otto.Writer
	state  otto.ConversationState
	asked  bool
	posted int
}

func (s *stubWriter) State(_ context.Context, _ string) (otto.ConversationState, error) {
	s.asked = true
	return s.state, nil
}

func (s *stubWriter) PostAssistantMessage(_ context.Context, _ otto.AssistantMessage) error {
	s.posted++
	return nil
}

// A conversation an agent has accepted must never receive an assistant reply.
func TestStubWriter_AcceptedConversationIsHumanOwned(t *testing.T) {
	w := &stubWriter{state: otto.ConversationState{Status: "active", HasAssignee: true, Exists: true}}
	st, err := w.State(context.Background(), "conv-1")
	if err != nil {
		t.Fatalf("State: %v", err)
	}
	if !w.asked {
		t.Fatal("expected the state to be consulted")
	}
	if !st.HumanOwned() {
		t.Fatal("an accepted conversation must read as human-owned")
	}
	if w.posted != 0 {
		t.Fatalf("expected no assistant message, got %d", w.posted)
	}
}
