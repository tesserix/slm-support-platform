package httpserver

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNew_HealthEndpoint_Returns200(t *testing.T) {
	r := New("test", slog.New(slog.NewTextHandler(io.Discard, nil)), "")

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Body.String(); got != `{"status":"ok"}` {
		t.Errorf("body = %q, want %q", got, `{"status":"ok"}`)
	}
}

func TestNew_AddsSecurityHeaders(t *testing.T) {
	r := New("test", slog.New(slog.NewTextHandler(io.Discard, nil)), "")

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assertHeader(t, rec, "X-Content-Type-Options", "nosniff")
	assertHeader(t, rec, "X-Frame-Options", "DENY")
	assertHeader(t, rec, "Referrer-Policy", "strict-origin-when-cross-origin")
	assertHeader(t, rec, "Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
}

func assertHeader(t *testing.T, rec *httptest.ResponseRecorder, key, want string) {
	t.Helper()
	if got := rec.Header().Get(key); got != want {
		t.Fatalf("%s = %q, want %q", key, got, want)
	}
}
