// Package contentguard screens chat message bodies before they are stored or
// forwarded to the AI. It blocks two classes of content:
//
//   - Profanity / derogatory language — keeps the conversation respectful.
//   - PII — phone numbers, emails, and long digit runs (card / id numbers) —
//     so customers don't leak personal data into a support transcript.
//
// It is intentionally conservative (word-boundary matching, a high digit
// threshold for numbers) to avoid false positives on legitimate support text
// like order numbers (e.g. CS-260623-3ADS) or prices.
package contentguard

import (
	"regexp"
	"strings"
)

// Category identifies why a message was blocked.
type Category string

const (
	CategoryNone      Category = ""
	CategoryProfanity Category = "profanity"
	CategoryPII       Category = "pii"
)

// Result is the outcome of a scan.
type Result struct {
	Allowed  bool
	Category Category
	// Reason is a customer-safe explanation suitable to surface in the UI.
	Reason string
}

var allowed = Result{Allowed: true, Category: CategoryNone}

// profanityTerms is a baseline block list of profanity + derogatory slurs.
// Matched case-insensitively on word boundaries (so "assistant" / "class" are
// safe). Extend as needed — kept here rather than in config so the guard works
// out of the box.
var profanityTerms = []string{
	"fuck", "fucker", "fucking", "motherfucker", "shit", "bullshit", "bitch",
	"bastard", "asshole", "dickhead", "cunt", "slut", "whore", "prick", "wanker",
	"twat", "bollocks", "douchebag", "jackass", "dumbass", "retard", "retarded",
	"nigger", "nigga", "faggot", "fag", "chink", "paki", "coon",
	"kike", "wetback", "tranny", "gook", "raghead",
}

var (
	profanityRe = regexp.MustCompile(`(?i)\b(` + strings.Join(profanityTerms, "|") + `)\b`)

	// Email addresses.
	emailRe = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)

	// Candidate number runs (optional leading +, then digits with common
	// phone separators). The digit count is verified separately.
	numberRunRe = regexp.MustCompile(`\+?[0-9][0-9\s().\-]{7,}[0-9]`)
	digitRe     = regexp.MustCompile(`[0-9]`)
)

// minPIIDigits is the digit count at/above which a number run is treated as a
// phone / card / id number. 10 covers mobile numbers worldwide while leaving
// shorter references (6-digit order suffixes, prices, years) alone.
const minPIIDigits = 10

const (
	reasonProfanity = "Please keep the conversation respectful — that message contains language that isn't allowed here."
	reasonEmail     = "For your privacy, please don't share email addresses in chat. If we need to look something up, share your order number instead."
	reasonNumber    = "For your privacy and security, please don't share phone numbers or card details in chat. Share your order number instead and we'll take it from there."
)

// Scan checks a message body and returns whether it is allowed. The first
// violation found wins; the Reason is safe to show the sender.
func Scan(text string) Result {
	if text == "" {
		return allowed
	}
	if profanityRe.MatchString(text) {
		return Result{Allowed: false, Category: CategoryProfanity, Reason: reasonProfanity}
	}
	if emailRe.MatchString(text) {
		return Result{Allowed: false, Category: CategoryPII, Reason: reasonEmail}
	}
	for _, run := range numberRunRe.FindAllString(text, -1) {
		if len(digitRe.FindAllString(run, -1)) >= minPIIDigits {
			return Result{Allowed: false, Category: CategoryPII, Reason: reasonNumber}
		}
	}
	return allowed
}
