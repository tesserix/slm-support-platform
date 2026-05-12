package orchestrator

import "testing"

func TestScoreConfidence(t *testing.T) {
	tests := []struct {
		name         string
		reply        string
		finishReason string
		wantAtMost   float64 // upper bound — exact score is heuristic
		wantAtLeast  float64
	}{
		{
			name:         "long confident reply",
			reply:        "Your order #12345 was dispatched on Tuesday and is on track to arrive by Friday. The tracking link is in your account.",
			finishReason: "stop",
			wantAtLeast:  0.85,
		},
		{
			name:         "I don't know",
			reply:        "I don't know the answer to that, sorry.",
			finishReason: "stop",
			wantAtMost:   0.6,
		},
		{
			name:         "very short",
			reply:        "Yes.",
			finishReason: "stop",
			wantAtMost:   0.6,
		},
		{
			name:         "length truncation",
			reply:        "Your refund is being processed and should arrive within five business days as per our policy",
			finishReason: "length",
			wantAtMost:   0.9,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := scoreConfidence(tc.reply, tc.finishReason)
			if tc.wantAtLeast > 0 && got < tc.wantAtLeast {
				t.Fatalf("got %.2f, want at least %.2f", got, tc.wantAtLeast)
			}
			if tc.wantAtMost > 0 && got > tc.wantAtMost {
				t.Fatalf("got %.2f, want at most %.2f", got, tc.wantAtMost)
			}
		})
	}
}
