package session

import (
	"testing"
	"time"
)

func newTestSigner() *TicketSigner {
	return NewTicketSigner("test-secret-0123456789", 2*time.Minute)
}

func TestPlatformTicketRoundTrip(t *testing.T) {
	s := newTestSigner()
	raw, _, err := s.Issue(Ticket{
		Audience: TicketAudiencePlatform,
		TenantID: "*",
		StoreID:  "*",
		UserID:   "admin-1",
	})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	tok, err := s.Parse(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if tok.Audience != TicketAudiencePlatform {
		t.Fatalf("audience = %q, want %q", tok.Audience, TicketAudiencePlatform)
	}
	if tok.TenantID != "*" || tok.StoreID != "*" {
		t.Fatalf("scope = %q/%q, want */*", tok.TenantID, tok.StoreID)
	}
	if tok.UserID != "admin-1" {
		t.Fatalf("user = %q, want admin-1", tok.UserID)
	}
}

// The audience constants must stay distinct — WS handlers use them to
// reject a staff ticket on a platform socket and vice versa.
func TestTicketAudiencesAreDistinct(t *testing.T) {
	if TicketAudiencePlatform == TicketAudienceStaff ||
		TicketAudiencePlatform == TicketAudienceCustomer {
		t.Fatal("platform audience must not collide with staff/customer")
	}
}

func TestTamperedTicketRejected(t *testing.T) {
	s := newTestSigner()
	raw, _, err := s.Issue(Ticket{
		Audience: TicketAudiencePlatform,
		TenantID: "*",
		StoreID:  "*",
	})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if _, err := s.Parse(raw + "x"); err == nil {
		t.Fatal("tampered ticket parsed without error")
	}
}
