package config

import (
	"os"
	"path/filepath"
	"testing"
)

const sampleRoutesYAML = `
default_tenant: mark8ly
products:
  mark8ly:
    system_prompt_file: /etc/slm-router/prompts/mark8ly.md
    rag_namespace: mark8ly
    aliases:
      - "00000000-0000-0000-0000-000000000001"
      - "mark8ly-uat"
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
	if got, want := r.DefaultTenant, "mark8ly"; got != want {
		t.Fatalf("default_tenant: got %q want %q", got, want)
	}
	m, ok := r.Products["mark8ly"]
	if !ok {
		t.Fatal("expected mark8ly tenant in parsed routes")
	}
	if got, want := m.RAGNamespace, "mark8ly"; got != want {
		t.Fatalf("rag_namespace: got %q want %q", got, want)
	}
	if got, want := len(m.Aliases), 2; got != want {
		t.Fatalf("aliases: got %d want %d", got, want)
	}
	if got, want := len(m.MCPServers), 1; got != want {
		t.Fatalf("mcp_servers: got %d want %d", got, want)
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

func TestResolveProduct(t *testing.T) {
	r := Routes{
		DefaultTenant: "mark8ly",
		Products: map[string]ProductConfig{
			"mark8ly": {
				RAGNamespace: "mark8ly",
				Aliases:      []string{"00000000-0000-0000-0000-000000000001", "mark8ly-uat"},
			},
			"fanzone": {
				RAGNamespace: "fanzone",
			},
		},
	}

	tests := []struct {
		name, tenant, wantName string
		wantOK                 bool
	}{
		{"exact key", "fanzone", "fanzone", true},
		{"alias UUID", "00000000-0000-0000-0000-000000000001", "mark8ly", true},
		{"alias slug", "mark8ly-uat", "mark8ly", true},
		{"unknown falls back to default", "00000000-0000-0000-0000-000000000999", "mark8ly", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, name, ok := r.ResolveProduct(tc.tenant)
			if ok != tc.wantOK || name != tc.wantName {
				t.Fatalf("ResolveProduct(%q): got (%q, %v), want (%q, %v)", tc.tenant, name, ok, tc.wantName, tc.wantOK)
			}
		})
	}

	// With no default, an unknown tenant should fail to resolve.
	noDefault := Routes{Products: r.Products}
	if _, _, ok := noDefault.ResolveProduct("nope"); ok {
		t.Fatal("expected resolve to fail without default")
	}
}
