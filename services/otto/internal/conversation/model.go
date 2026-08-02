package conversation

import "time"

// Status is the lifecycle state of a conversation. The subset is
// deliberately small — "Otto v1" is customer ↔ staff only.
type Status string

const (
	// StatusPending — customer opened the thread, no staff has accepted yet.
	StatusPending Status = "pending"
	// StatusActive — a staff member has accepted and the thread is live.
	StatusActive Status = "active"
	// StatusClosed — staff resolved the thread. Read-only for customer.
	StatusClosed Status = "closed"
)

// Customer captures whoever opened the thread. For anonymous visitors
// session_token is the only durable identifier we have.
type Customer struct {
	SessionToken string `bson:"session_token" json:"session_token"`
	UserID       string `bson:"user_id,omitempty" json:"user_id,omitempty"`
	Name         string `bson:"name,omitempty" json:"name,omitempty"`
	Email        string `bson:"email,omitempty" json:"email,omitempty"`
}

// Assignee is the staff member currently handling the thread. Set on accept.
type Assignee struct {
	UserID    string    `bson:"user_id" json:"user_id"`
	Name      string    `bson:"name,omitempty" json:"name,omitempty"`
	Email     string    `bson:"email,omitempty" json:"email,omitempty"`
	AssignedAt time.Time `bson:"assigned_at" json:"assigned_at"`
}

// IntakeForm captures the structured context the customer shares when
// they open a new thread. The reason/status fields route the case to
// the right support flow, and DOB is the shared secret staff use to
// verify identity before discussing order details.
//
// Reason is a short enum-ish string the widget sends from a fixed
// dropdown (order_issue / product_question / return / payment /
// other). Status is free-text describing the current state of the
// issue ("checkout stuck on 3DS", "package arrived damaged"). DOB is
// YYYY-MM-DD; kept as a string so we don't go through tz gymnastics
// on a field where the local-date semantics matter. DOB is only
// required when the reason implies an account/order lookup.
type IntakeForm struct {
	Reason      string    `bson:"reason" json:"reason"`
	Status      string    `bson:"status" json:"status"`
	DOB         string    `bson:"dob,omitempty" json:"dob,omitempty"`
	SubmittedAt time.Time `bson:"submitted_at" json:"submitted_at"`
}

// Feedback captures the post-case survey. Ratings are 1-5, 0 means
// "not answered" so we can distinguish a skipped question from a
// genuine one-star. Only collectable after a conversation closes.
type Feedback struct {
	CallRating    int       `bson:"call_rating" json:"call_rating"`
	QueryResolved bool      `bson:"query_resolved" json:"query_resolved"`
	StaffRating   int       `bson:"staff_rating" json:"staff_rating"`
	Comments      string    `bson:"comments,omitempty" json:"comments,omitempty"`
	SubmittedAt   time.Time `bson:"submitted_at" json:"submitted_at"`
}

// Conversation is a single support thread, fully scoped by tenant + store.
type Conversation struct {
	ID            string     `bson:"_id" json:"id"`
	// CaseID is a human-readable reference (CS-YYMMDD-XXXX) both parties
	// can quote when discussing the thread off-channel. The full uuid ID
	// is still the database key; CaseID is purely for humans.
	CaseID        string     `bson:"case_id" json:"case_id"`
	TenantID      string     `bson:"tenant_id" json:"tenant_id"`
	StoreID       string     `bson:"store_id" json:"store_id"`
	Status        Status     `bson:"status" json:"status"`
	Subject       string     `bson:"subject,omitempty" json:"subject,omitempty"`
	Customer      Customer   `bson:"customer" json:"customer"`
	Assignee      *Assignee  `bson:"assignee,omitempty" json:"assignee,omitempty"`
	CreatedAt     time.Time  `bson:"created_at" json:"created_at"`
	UpdatedAt     time.Time  `bson:"updated_at" json:"updated_at"`
	LastMessageAt time.Time  `bson:"last_message_at" json:"last_message_at"`
	ClosedAt      *time.Time `bson:"closed_at,omitempty" json:"closed_at,omitempty"`

	// Intake context + feedback. Set at create + close respectively.
	Intake   *IntakeForm `bson:"intake,omitempty" json:"intake,omitempty"`
	Feedback *Feedback   `bson:"feedback,omitempty" json:"feedback,omitempty"`

	// LastCustomerMessageAt powers the 15-minute inactivity sweep in
	// phase 2. Set on create (= CreatedAt), bumped on every customer
	// message post.
	LastCustomerMessageAt time.Time `bson:"last_customer_message_at,omitempty" json:"last_customer_message_at,omitempty"`
	// InactivityClosedAt marks conversations the sweeper auto-closed.
	// Widget renders a slightly different closed state for these.
	InactivityClosedAt *time.Time `bson:"inactivity_closed_at,omitempty" json:"inactivity_closed_at,omitempty"`

	// NeedsHuman flips to true when the AI (slm-router) escalates a
	// conversation: low confidence reply, MCP tool failure, customer
	// explicitly asks for a human, or a keyword in the escalation policy
	// (e.g. "refund", "lawyer"). While false, slm-router auto-replies as
	// the assistant. While true, slm-router stops auto-replying and the
	// conversation is visible in the staff inbox as a normal pending
	// thread. Staff can flip it back to false to hand the thread back
	// to AI if they choose.
	NeedsHuman bool `bson:"needs_human" json:"needs_human"`
	// LastAssistantMessageAt is set every time slm-router posts a
	// SenderAssistant message. Used by the inbox UI to show "AI is
	// handling this" badges and by sweeper logic to avoid auto-closing
	// AI-active threads too aggressively.
	LastAssistantMessageAt *time.Time `bson:"last_assistant_message_at,omitempty" json:"last_assistant_message_at,omitempty"`

	// Denormalised counters for inbox UI.
	MessageCount        int `bson:"message_count" json:"message_count"`
	UnreadCountCustomer int `bson:"unread_count_customer" json:"unread_count_customer"`
	UnreadCountStaff    int `bson:"unread_count_staff" json:"unread_count_staff"`
}

// Reasons enumerates the marketplace-shape intake reasons. Kept as
// named consts because the storefront handlers + tests reference them
// directly. New tenants extend the whitelist by adding entries to
// TenantReasons rather than to this block.
const (
	ReasonOrderIssue      = "order_issue"
	ReasonReturn          = "return"
	ReasonPayment         = "payment"
	ReasonProductQuestion = "product_question"
	ReasonOther           = "other"
)

// ReasonGeneralQuestion is the universal quick-ask reason. Every tenant
// includes it. Picking it lets a customer fire off a one-line question
// without filling the "current status" field or providing DOB — the
// chat goes straight to the SLM (or queued staff) with just the
// message body. This is the only reason that skips the status check.
const ReasonGeneralQuestion = "general_question"

// TenantReasons is the per-tenant whitelist of intake reasons + the
// subset that triggers the date-of-birth verification step on the
// intake form + the subset that skips the "current status / one-line
// summary" field. The widget sends `X-Tenant-ID` and a reason string;
// this table is the backend's single source of truth.
//
// Keys MUST match the tenantId the @tesserix/otto-widget wrapper
// passes (see slm-support-platform/docs/per-product-slm-mcp-routing.md
// for the canonical list). An unknown tenant falls back to the
// marketplace whitelist for backwards-compatibility.
var TenantReasons = map[string]struct {
	Whitelist map[string]bool
	NeedsDOB  map[string]bool
	NoStatus  map[string]bool
}{
	"mark8ly": {
		Whitelist: map[string]bool{
			ReasonOrderIssue:      true,
			ReasonReturn:          true,
			ReasonPayment:         true,
			ReasonProductQuestion: true,
			ReasonOther:           true,
			ReasonGeneralQuestion: true,
		},
		NeedsDOB: map[string]bool{
			ReasonOrderIssue: true,
			ReasonReturn:     true,
			ReasonPayment:    true,
		},
		NoStatus: map[string]bool{
			ReasonGeneralQuestion: true,
		},
	},
	"fanzone": {
		Whitelist: map[string]bool{
			"account_issue":       true,
			"points_question":     true,
			"prediction_issue":    true,
			"match_question":      true,
			"bug_report":          true,
			"other":               true,
			ReasonGeneralQuestion: true,
		},
		// No DOB lookups — FanZone identifies users by Firebase UID,
		// not by birthday. Asking for DOB on a sports site is creepy.
		NeedsDOB: map[string]bool{},
		NoStatus: map[string]bool{
			ReasonGeneralQuestion: true,
		},
	},
	"homechef": {
		Whitelist: map[string]bool{
			"order_tracking":      true,
			"delivery_issue":      true,
			"refund":              true,
			"chef_question":       true,
			"account_issue":       true,
			"other":               true,
			ReasonGeneralQuestion: true,
		},
		NeedsDOB: map[string]bool{},
		NoStatus: map[string]bool{
			ReasonGeneralQuestion: true,
		},
	},
	// HomeChef vendor/chef side — same product, separate queue. The
	// homechef-api mobile proxy picks this tenant for callers with the
	// chef role so chef threads land in their own inbox lane and route
	// to the vendor knowledge base (slm-router aliases it to homechef
	// until a dedicated vendor namespace ships).
	"homechef-vendor": {
		Whitelist: map[string]bool{
			"payout_issue":        true,
			"order_management":    true,
			"menu_help":           true,
			"verification_docs":   true,
			"account_issue":       true,
			"other":               true,
			ReasonGeneralQuestion: true,
		},
		NeedsDOB: map[string]bool{},
		NoStatus: map[string]bool{
			ReasonGeneralQuestion: true,
		},
	},
	"stockpilot": {
		Whitelist: map[string]bool{
			"portfolio_question":  true,
			"broker_connection":   true,
			"ai_agent_issue":      true,
			"billing":             true,
			"bug_report":          true,
			"other":               true,
			ReasonGeneralQuestion: true,
		},
		NeedsDOB: map[string]bool{},
		NoStatus: map[string]bool{
			ReasonGeneralQuestion: true,
		},
	},
	"gameverse": {
		Whitelist: map[string]bool{
			"game_rules":           true,
			"multiplayer_issue":    true,
			"leaderboard_question": true,
			"account_issue":        true,
			"bug_report":           true,
			"other":                true,
			ReasonGeneralQuestion:  true,
		},
		NeedsDOB: map[string]bool{},
		NoStatus: map[string]bool{
			ReasonGeneralQuestion: true,
		},
	},
	"horoscope": {
		Whitelist: map[string]bool{
			"chart_question":      true,
			"reading_question":    true,
			"scan_issue":          true,
			"account_issue":       true,
			"billing":             true,
			"other":               true,
			ReasonGeneralQuestion: true,
		},
		NeedsDOB: map[string]bool{},
		NoStatus: map[string]bool{
			ReasonGeneralQuestion: true,
		},
	},
	"scrapper": {
		Whitelist: map[string]bool{
			"scrape_job_issue":    true,
			"ai_analysis_issue":   true,
			"publishing_issue":    true,
			"account_connection":  true,
			"billing":             true,
			"other":               true,
			ReasonGeneralQuestion: true,
		},
		NeedsDOB: map[string]bool{},
		NoStatus: map[string]bool{
			ReasonGeneralQuestion: true,
		},
	},
}

// fallbackTenant is the marketplace shape, used when X-Tenant-ID is
// absent or unrecognised. Keeping it as a separate constant avoids a
// hidden allocation on the hot path.
const fallbackTenant = "mark8ly"

// IsReasonAllowed returns true when the (tenant, reason) pair is in the
// whitelist. Unknown tenants fall back to the marketplace whitelist
// (legacy storefront clients that don't yet send X-Tenant-ID).
func IsReasonAllowed(tenantID, reason string) bool {
	rules, ok := TenantReasons[tenantID]
	if !ok {
		rules = TenantReasons[fallbackTenant]
	}
	return rules.Whitelist[reason]
}

// DOBRequiredFor returns true for reasons where staff needs to verify
// identity before sharing order details. The lookup is tenant-aware:
// only mark8ly's order/return/payment reasons currently demand DOB.
func DOBRequiredFor(tenantID, reason string) bool {
	rules, ok := TenantReasons[tenantID]
	if !ok {
		rules = TenantReasons[fallbackTenant]
	}
	return rules.NeedsDOB[reason]
}

// StatusRequiredFor reports whether the "current status / one-line
// summary" field is required for this (tenant, reason) pair. Quick-ask
// reasons (e.g. general_question) skip it so a one-tap message lands
// without a second free-text input.
func StatusRequiredFor(tenantID, reason string) bool {
	rules, ok := TenantReasons[tenantID]
	if !ok {
		rules = TenantReasons[fallbackTenant]
	}
	return !rules.NoStatus[reason]
}
