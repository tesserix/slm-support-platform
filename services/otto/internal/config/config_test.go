package config

import (
	"errors"
	"testing"
)

func TestLoad_RefusesEmptyInternalAuthInProd(t *testing.T) {
	t.Setenv("ENV", "prod")
	t.Setenv("MONGO_URL", "mongodb://localhost:27017")
	t.Setenv("CUSTOMER_SESSION_SECRET", "cust-secret")
	t.Setenv("INTERNAL_AUTH_SECRET", "")

	if _, err := Load(); !errors.Is(err, ErrInternalAuthRequired) {
		t.Fatalf("expected ErrInternalAuthRequired, got %v", err)
	}
}

func TestLoad_AllowsEmptyInternalAuthInDev(t *testing.T) {
	t.Setenv("ENV", "dev")
	t.Setenv("MONGO_URL", "mongodb://localhost:27017")
	t.Setenv("CUSTOMER_SESSION_SECRET", "cust-secret")
	t.Setenv("INTERNAL_AUTH_SECRET", "")

	if _, err := Load(); err != nil {
		t.Fatalf("dev should boot with empty internal-auth, got %v", err)
	}
}

func TestLoad_TrimsInternalAuth(t *testing.T) {
	t.Setenv("ENV", "prod")
	t.Setenv("MONGO_URL", "mongodb://localhost:27017")
	t.Setenv("CUSTOMER_SESSION_SECRET", "cust-secret")
	t.Setenv("INTERNAL_AUTH_SECRET", "  abcd1234\n")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("expected ok with trimmed value, got %v", err)
	}
	if cfg.InternalAuthSecret != "abcd1234" {
		t.Fatalf("expected trim to strip whitespace + newline, got %q", cfg.InternalAuthSecret)
	}
}
