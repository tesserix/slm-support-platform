package otp

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/tesserix/slm-support-platform/services/otto/internal/auth"
)

// Handler exposes the OTP endpoints under /api/v1/storefront/otto/verify.
type Handler struct {
	svc    *Service
	logger *slog.Logger
}

// NewHandler wires a Handler.
func NewHandler(svc *Service, logger *slog.Logger) *Handler {
	return &Handler{svc: svc, logger: logger}
}

// Register mounts routes onto the given group. Caller must have applied
// auth.CustomerContext so tenant_id + store_id are set.
func (h *Handler) Register(r *gin.RouterGroup) {
	r.POST("/verify/start", h.start)
}

type startRequest struct {
	Email     string `json:"email"`
	Name      string `json:"name"`
	StoreName string `json:"store_name"`
}

type startResponse struct {
	Sent     bool   `json:"sent"`
	MaskedTo string `json:"masked_to"`
}

func (h *Handler) start(c *gin.Context) {
	var body startRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_body"})
		return
	}
	if strings.TrimSpace(body.Email) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "email_required"})
		return
	}

	res, err := h.svc.Start(c.Request.Context(), StartInput{
		TenantID:  c.GetString(auth.CtxTenantID),
		StoreID:   c.GetString(auth.CtxStoreID),
		Email:     body.Email,
		Name:      body.Name,
		StoreName: body.StoreName,
	})
	switch {
	case err == nil:
		c.JSON(http.StatusOK, startResponse{Sent: true, MaskedTo: res.MaskedTo})
	case errors.Is(err, ErrResendTooSoon):
		c.JSON(http.StatusTooManyRequests, gin.H{
			"error":   "resend_too_soon",
			"message": "please wait a moment before requesting another code",
		})
	default:
		h.logger.Error("otp: start failed", "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "otp_start_failed"})
	}
}
