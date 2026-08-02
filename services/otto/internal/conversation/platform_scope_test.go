package conversation

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/bson"
)

// The platform inbox loads a conversation cross-tenant and pins its real
// tenant+store, then delegates to the tenant-scoped admin handler. That
// handler must reuse the row rather than re-query it: for a conversation
// written before store_id became mandatory, the second lookup filtered on
// store_id = "" and missed a document with no store_id field at all, so
// GET /conversations/:id/messages 404'd on a row the middleware had just
// found. Deps carry a nil Conversations repo — a re-query would panic.
func TestLoadForStaffReusesScopedConversation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := NewAdminHandler(AdminDeps{Logger: discardLogger()})

	want := &Conversation{ID: "legacy-1", TenantID: "fanzone"} // no StoreID
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Set(CtxConversation, want)

	got, ok := h.loadForStaff(c)
	if !ok {
		t.Fatal("loadForStaff rejected a conversation already resolved upstream")
	}
	if got != want {
		t.Fatalf("loadForStaff returned %#v, want the stashed row", got)
	}
}

// Without the stash the handler must still go to the repo — the reuse path
// is an optimisation for authorized callers, never a way to skip the scope
// check on a request that never had one.
func TestLoadForStaffIgnoresWrongTypeInContext(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := NewAdminHandler(AdminDeps{Logger: discardLogger()})

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Set(CtxConversation, "not-a-conversation")

	defer func() {
		if recover() == nil {
			t.Fatal("expected the nil repo to be consulted, not the bogus context value")
		}
	}()
	_, _ = h.loadForStaff(c)
}

func TestStoreScopeMatchesLegacyRowsOnly(t *testing.T) {
	if got := storeScope("fanzone-main"); got != "fanzone-main" {
		t.Fatalf("a real store must filter on plain equality, got %#v", got)
	}
	got, ok := storeScope("").(bson.M)
	if !ok {
		t.Fatalf("an empty store must widen to an $in clause, got %#v", got)
	}
	in, ok := got["$in"].(bson.A)
	if !ok || len(in) != 2 || in[0] != "" || in[1] != nil {
		t.Fatalf(`want $in ["", nil] so a missing store_id matches, got %#v`, got["$in"])
	}
}
