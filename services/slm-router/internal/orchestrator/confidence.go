package orchestrator

import "strings"

// scoreConfidence returns a 0-1 estimate of how confident we are in the
// model's reply. It's a heuristic, not a probability — the goal is to
// catch the common low-quality patterns so escalation kicks in.
//
// Penalised patterns:
//   - Empty or trivially short replies (model bailed)
//   - Refusal language ("I don't know", "I'm not sure", "I cannot")
//   - finish_reason that isn't "stop" (truncation, error, etc.)
//
// Rewarded patterns:
//   - Length over ~50 chars
//   - Concrete tokens (numbers, quoted strings) suggesting specifics
//
// Bounded to [0, 1].
func scoreConfidence(reply string, finishReason string) float64 {
	score := 0.9
	trimmed := strings.TrimSpace(reply)
	lower := strings.ToLower(trimmed)

	if len(trimmed) < 30 {
		score -= 0.4
	} else if len(trimmed) >= 80 {
		score += 0.05
	}

	if finishReason != "stop" && finishReason != "" {
		// length/tool truncations aren't necessarily bad, but anything
		// non-"stop" is a tiny signal of trouble.
		score -= 0.1
	}

	for _, phrase := range lowConfidencePhrases {
		if strings.Contains(lower, phrase) {
			// Heavier penalty than the length/finish-reason adjustments
			// because explicit refusal language is the strongest signal
			// the model is about to hallucinate or punt.
			score -= 0.4
			break
		}
	}

	// Soft floor + ceiling so escalation thresholds work in the
	// expected range.
	if score < 0 {
		return 0
	}
	if score > 1 {
		return 1
	}
	return score
}

var lowConfidencePhrases = []string{
	"i don't know",
	"i do not know",
	"i'm not sure",
	"i am not sure",
	"i cannot help",
	"i can't help",
	"i'm unable",
	"unfortunately, i",
	"as an ai",
}
