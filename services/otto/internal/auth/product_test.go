package auth

import (
	"github.com/gin-gonic/gin"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestProductProvenanceRequiresAuthenticatedPlatformProxy(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name, secret, supplied, tenant, user, want string
		status                                     int
	}{
		{"mark8ly", "shared", "shared", "platform", "merchant", "mark8ly", 200},
		{"bad secret", "shared", "wrong", "platform", "merchant", "", 401},
		{"dev unauthenticated", "", "", "platform", "merchant", "", 200},
		{"storefront", "shared", "shared", "merchant-tenant", "customer", "", 200},
		{"anonymous", "shared", "shared", "platform", "", "", 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := gin.New()
			r.Use(CustomerContext(tc.secret))
			r.GET("/", func(c *gin.Context) { c.String(200, c.GetString("product_id")) })
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			for k, v := range map[string]string{"X-Internal-Auth": tc.supplied, "X-Tenant-Id": tc.tenant, "X-Store-Id": "default", "X-User-Id": tc.user, "X-Product-Id": "mark8ly"} {
				req.Header.Set(k, v)
			}
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != tc.status || (w.Code == 200 && w.Body.String() != tc.want) {
				t.Fatalf("status=%d product=%q; want %d %q", w.Code, w.Body.String(), tc.status, tc.want)
			}
		})
	}
}
