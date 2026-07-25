package auth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

const testSecret = "internal-secret"

func platformStaffRouter(secret string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	g := r.Group("/p")
	g.Use(PlatformStaff(secret))
	g.GET("/ok", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"user_id": c.GetString(CtxUserID),
			"email":   c.GetString(CtxUserEmail),
		})
	})
	return r
}

func doReq(r *gin.Engine, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/p/ok", nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// Empty configured secret must deny even a matching empty header —
// the cross-tenant surface never falls open (same rule as PlatformAuth).
func TestPlatformStaffDeniesOnEmptyConfiguredSecret(t *testing.T) {
	r := platformStaffRouter("")
	w := doReq(r, map[string]string{"X-Internal-Auth": "", "X-User-Id": "u1"})
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d, want 401", w.Code)
	}
}

func TestPlatformStaffDeniesWrongSecret(t *testing.T) {
	r := platformStaffRouter(testSecret)
	w := doReq(r, map[string]string{"X-Internal-Auth": "nope", "X-User-Id": "u1"})
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d, want 401", w.Code)
	}
}

// Secret alone is not enough — accept/reply/close must be attributable,
// so a missing X-User-Id denies.
func TestPlatformStaffRequiresUserID(t *testing.T) {
	r := platformStaffRouter(testSecret)
	w := doReq(r, map[string]string{"X-Internal-Auth": testSecret})
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d, want 401", w.Code)
	}
}

func TestPlatformStaffSetsIdentityContext(t *testing.T) {
	r := platformStaffRouter(testSecret)
	w := doReq(r, map[string]string{
		"X-Internal-Auth": testSecret,
		"X-User-Id":       "admin-1",
		"X-User-Email":    "admin@tesserix.app",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, want := range []string{`"user_id":"admin-1"`, `"email":"admin@tesserix.app"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("body %s missing %s", body, want)
		}
	}
}

// Regression: PlatformAuth (stats endpoint) also denies on empty secret.
func TestPlatformAuthDeniesOnEmptyConfiguredSecret(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	g := r.Group("/p")
	g.Use(PlatformAuth(""))
	g.GET("/ok", func(c *gin.Context) { c.Status(http.StatusOK) })
	w := doReq(r, map[string]string{"X-Internal-Auth": ""})
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d, want 401", w.Code)
	}
}
