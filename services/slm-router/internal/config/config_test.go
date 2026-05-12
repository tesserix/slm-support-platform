package config

import (
	"os"
	"path/filepath"
	"testing"
)

const sampleRoutesYAML = `
products:
  mark8ly:
    system_prompt_file: /etc/slm-router/prompts/mark8ly.md
    rag_namespace: mark8ly
    mcp_servers:
      - name: mark8ly-mcp
        url: http://mark8ly-mcp.mark8ly.svc.cluster.local:8765/mcp
        auth_header: X-MCP-Key
        auth_env_var: MARK8LY_MCP_KEY
    escalation:
      confidence_threshold: 0.6
      keywords: ["refund", "lawyer"]
      max_tool_failures: 2
  fanzone:
    system_prompt_file: /etc/slm-router/prompts/fanzone.md
    rag_namespace: fanzone
    mcp_servers:
      - name: fanzone-mcp
        url: http://fanzone-mcp.fanzone.svc.cluster.local:8765/mcp
        auth_header: X-MCP-Key
        auth_env_var: FANZONE_MCP_KEY
    escalation:
      confidence_threshold: 0.65
      keywords: ["chargeback"]
      max_tool_failures: 2
`

func TestLoadRoutes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "routes.yaml")
	if err := os.WriteFile(path, []byte(sampleRoutesYAML), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	r, err := loadRoutes(path)
	if err != nil {
		t.Fatalf("loadRoutes: %v", err)
	}
	if got, want := len(r.Products), 2; got != want {
		t.Fatalf("expected %d products, got %d", want, got)
	}
	m, ok := r.Products["mark8ly"]
	if !ok {
		t.Fatal("expected mark8ly tenant in parsed routes")
	}
	if got, want := m.RAGNamespace, "mark8ly"; got != want {
		t.Fatalf("rag_namespace: got %q want %q", got, want)
	}
	if got, want := len(m.MCPServers), 1; got != want {
		t.Fatalf("mcp_servers: got %d want %d", got, want)
	}
	if got, want := m.Escalation.ConfidenceThreshold, 0.6; got != want {
		t.Fatalf("confidence_threshold: got %v want %v", got, want)
	}
}

func TestLoadRoutesMissingFile(t *testing.T) {
	r, err := loadRoutes(filepath.Join(t.TempDir(), "nope.yaml"))
	if err != nil {
		t.Fatalf("missing file should not error, got %v", err)
	}
	if r.Products == nil {
		t.Fatal("expected non-nil Products map even when file missing")
	}
}
