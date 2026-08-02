package session

import "testing"

// A conversation predating the store_id write guard carries no store. The
// per-thread WebSocket ticket is minted from the row's own scope, so
// requiring a store here returned 500 and those threads never went live.
// Tenant is the scope boundary and stays mandatory.
func TestIssueAllowsEmptyStoreButNotEmptyTenant(t *testing.T) {
	s := NewTicketSigner("test-secret-value", 0)

	if _, _, err := s.Issue(Ticket{
		Audience: TicketAudiencePlatform,
		TenantID: "fanzone",
		StoreID:  "",
	}); err != nil {
		t.Fatalf("a store-less conversation must still mint a ticket: %v", err)
	}

	if _, _, err := s.Issue(Ticket{
		Audience: TicketAudiencePlatform,
		TenantID: "",
		StoreID:  "fanzone-main",
	}); err == nil {
		t.Fatal("tenant is the scope boundary and must stay required")
	}
}
