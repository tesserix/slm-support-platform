package auth

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/tesserix/slm-support-platform/services/otto/internal/session"
)

// Context keys set by these middlewares. Handlers read them via c.GetString.
const (
	CtxTenantID     = "tenant_id"
	CtxStoreID      = "store_id"
	CtxUserID       = "user_id"
	CtxUserEmail    = "user_email"
	CtxUserName     = "user_name"
	CtxUserRole     = "user_role"
	CtxSessionToken = "session_token"
)

// StaffAuth trusts the Next.js admin proxy. The admin middleware already
// validates the m8_session cookie against auth-bff and forwards identity
// headers; we just read them here. When internalSecret is non-empty an
// X-Internal-Auth match is also required — this prevents anyone bypassing
// the proxy and hitting the service directly.
func StaffAuth(internalSecret string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if internalSecret != "" && !constantTimeEqual(c.GetHeader("X-Internal-Auth"), internalSecret) {
			respondUnauthorized(c)
			return
		}
		userID := c.GetHeader("X-User-Id")
		tenantID := c.GetHeader("X-Tenant-Id")
		if userID == "" || tenantID == "" {
			respondUnauthorized(c)
			return
		}
		c.Set(CtxUserID, userID)
		c.Set(CtxTenantID, tenantID)
		if email := c.GetHeader("X-User-Email"); email != "" {
			c.Set(CtxUserEmail, email)
		}
		if name := c.GetHeader("X-User-Name"); name != "" {
			c.Set(CtxUserName, name)
		}
		if role := c.GetHeader("X-User-Role"); role != "" {
			c.Set(CtxUserRole, role)
		}
		if storeID := c.GetHeader("X-Store-Id"); storeID != "" {
			c.Set(CtxStoreID, storeID)
		}
		c.Next()
	}
}

// PlatformAuth gates the platform super-admin endpoints (cross-tenant
// analytics) that aggregate across every tenant. Only a caller holding the
// internal shared secret — i.e. the tesserix-home admin proxy — may pass.
// Unlike StaffAuth there is NO tenant/store scope, and unlike the other
// gates the secret is mandatory: an empty secret denies (a cross-tenant
// surface must never fall open to the permissive-empty path).
func PlatformAuth(internalSecret string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if internalSecret == "" || !constantTimeEqual(c.GetHeader("X-Internal-Auth"), internalSecret) {
			respondUnauthorized(c)
			return
		}
		// Carry the platform admin's id for audit/logging when present.
		if uid := c.GetHeader("X-User-Id"); uid != "" {
			c.Set(CtxUserID, uid)
		}
		c.Next()
	}
}

// StoreResolver locks the staff caller to a specific store. The admin proxy
// selects the store per request (there's no cross-store inbox in v1) and
// forwards it as X-Store-Id. If it's missing the request is rejected.
func StoreResolver() gin.HandlerFunc {
	return func(c *gin.Context) {
		storeID := c.GetString(CtxStoreID)
		if storeID == "" {
			storeID = c.GetHeader("X-Store-Id")
			if storeID != "" {
				c.Set(CtxStoreID, storeID)
			}
		}
		if storeID == "" {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{
				"error":   "missing_store",
				"message": "X-Store-Id header is required",
			})
			return
		}
		c.Next()
	}
}

// CustomerContext reads tenant+store from request headers set by the
// storefront proxy. The storefront middleware resolves tenant from the
// subdomain and forwards it via X-Tenant-Id / X-Store-Id. An
// InternalAuth shared secret prevents bypassing the proxy.
func CustomerContext(internalSecret string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if internalSecret != "" && !constantTimeEqual(c.GetHeader("X-Internal-Auth"), internalSecret) {
			respondUnauthorized(c)
			return
		}
		tenantID := c.GetHeader("X-Tenant-Id")
		storeID := c.GetHeader("X-Store-Id")
		if tenantID == "" || storeID == "" {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{
				"error":   "missing_scope",
				"message": "X-Tenant-Id and X-Store-Id headers are required",
			})
			return
		}
		c.Set(CtxTenantID, tenantID)
		c.Set(CtxStoreID, storeID)
		// Logged-in customers — if the storefront proxy forwards a user id
		// we remember it so messages can be attributed.
		if uid := c.GetHeader("X-User-Id"); uid != "" {
			c.Set(CtxUserID, uid)
		}
		if email := c.GetHeader("X-User-Email"); email != "" {
			c.Set(CtxUserEmail, email)
		}
		c.Next()
	}
}

func constantTimeEqual(got, want string) bool {
	if got == "" || want == "" {
		return false
	}
	gotSum := sha256.Sum256([]byte(got))
	wantSum := sha256.Sum256([]byte(want))
	return subtle.ConstantTimeCompare(gotSum[:], wantSum[:]) == 1
}

// RequireCustomerSession validates the otto_session cookie (or an explicit
// X-Otto-Session header for EventSource/WebSocket handshakes that strip
// cookies). The token is scope-bound to the tenant+store set by
// CustomerContext; any drift is rejected.
func RequireCustomerSession(cookieName string, signer *session.Signer) gin.HandlerFunc {
	return func(c *gin.Context) {
		raw, err := c.Cookie(cookieName)
		if err != nil || raw == "" {
			raw = c.GetHeader("X-Otto-Session")
		}
		if raw == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error":   "no_session",
				"message": "session cookie missing",
			})
			return
		}
		tenantID := c.GetString(CtxTenantID)
		storeID := c.GetString(CtxStoreID)
		tok, err := signer.Parse(raw, tenantID, storeID)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error":   "invalid_session",
				"message": err.Error(),
			})
			return
		}
		c.Set(CtxSessionToken, tok.ID)
		c.Next()
	}
}

func respondUnauthorized(c *gin.Context) {
	c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
		"error":   "unauthorized",
		"message": "authentication required",
	})
}
