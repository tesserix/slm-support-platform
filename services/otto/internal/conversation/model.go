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

	// Denormalised counters for inbox UI.
	MessageCount        int `bson:"message_count" json:"message_count"`
	UnreadCountCustomer int `bson:"unread_count_customer" json:"unread_count_customer"`
	UnreadCountStaff    int `bson:"unread_count_staff" json:"unread_count_staff"`
}

// Reasons enumerates the fixed intake reasons. The DOB-required
// subset is the one that touches order/account data.
const (
	ReasonOrderIssue      = "order_issue"
	ReasonReturn          = "return"
	ReasonPayment         = "payment"
	ReasonProductQuestion = "product_question"
	ReasonOther           = "other"
)

// DOBRequiredFor returns true for reasons where staff needs to verify
// identity before sharing order details.
func DOBRequiredFor(reason string) bool {
	switch reason {
	case ReasonOrderIssue, ReasonReturn, ReasonPayment:
		return true
	}
	return false
}
