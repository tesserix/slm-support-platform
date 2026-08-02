package conversation

import (
	"testing"

	"github.com/gin-gonic/gin"
)

// The platform inbox is cross-tenant, so every case action it offers has to
// be mounted on the platform group as well as the tenant-scoped admin one.
// Reopen was missing here while the button shipped in the widget, so closing
// a case stranded it: close succeeded, reopen 404'd on a route that did not
// exist. Assert the whole set rather than just reopen — the next action added
// to the widget should fail here, not in production.
func TestPlatformRegistersEveryCaseAction(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()

	deps := AdminDeps{Logger: discardLogger()}
	NewPlatformHandler(NewAdminHandler(deps), deps).Register(r.Group("/api/v1/platform/otto"))

	got := make(map[string]bool, 16)
	for _, rt := range r.Routes() {
		got[rt.Method+" "+rt.Path] = true
	}

	for _, want := range []string{
		"GET /api/v1/platform/otto/conversations",
		"GET /api/v1/platform/otto/conversations/:id",
		"GET /api/v1/platform/otto/conversations/:id/messages",
		"POST /api/v1/platform/otto/conversations/:id/accept",
		"POST /api/v1/platform/otto/conversations/:id/messages",
		"POST /api/v1/platform/otto/conversations/:id/close",
		"POST /api/v1/platform/otto/conversations/:id/reopen",
	} {
		if !got[want] {
			t.Errorf("platform group is missing %s", want)
		}
	}
}
