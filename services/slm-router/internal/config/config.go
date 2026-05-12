// Package config loads slm-router runtime configuration from environment
// variables (operational settings like ports, Mongo URI, GPU flags) and
// from a mounted YAML file (per-tenant routing rules — system prompt,
// RAG namespace, MCP servers, escalation policy).
//
// The split exists because operational settings get rotated by
// ExternalSecrets and platform owners, while tenant rules get updated
// by product teams and the ML team. Two different change cadences, two
// different change owners — two files.
package config

import (
	"fmt"
	"os"
	"slices"
	"time"

	"github.com/kelseyhightower/envconfig"
	"gopkg.in/yaml.v3"
)

// Env holds the operational configuration loaded from environment
// variables. Mirror the field doc-strings into the Helm chart's
// values.yaml when wiring the deployment.
type Env struct {
	// Environment label ("dev" / "prod"); used for log fields.
	Env string `envconfig:"ENV" default:"dev"`

	// HTTP server port. Gin binds here; Kubernetes Service points at it.
	HTTPPort int `envconfig:"HTTP_PORT" default:"8090"`

	// Mongo connection for Otto's database. slm-router reads the
	// conversation/message collections via change stream and posts
	// assistant messages back.
	MongoURI      string `envconfig:"MONGO_URI" required:"true"`
	MongoDatabase string `envconfig:"MONGO_DATABASE" default:"otto"`

	// Path to the mounted tenant-routing YAML (ConfigMap volume).
	// Loaded on boot and reloaded periodically (see RouteReloadInterval).
	RoutesFile string `envconfig:"ROUTES_FILE" default:"/etc/slm-router/routes.yaml"`

	// Interval between routes-file reloads. ConfigMap updates are
	// eventual; this controls how fast new tenant config takes effect.
	RouteReloadInterval time.Duration `envconfig:"ROUTE_RELOAD_INTERVAL" default:"60s"`

	// Endpoint of slm-inference (OpenAI-compatible HTTP API, e.g. vLLM
	// or llama.cpp's server mode).
	InferenceURL string `envconfig:"INFERENCE_URL" default:"http://slm-inference.support-platform.svc.cluster.local:8000"`
	// Endpoint of the embedder (POST /embed → []float32).
	EmbedderURL string `envconfig:"EMBEDDER_URL" default:"http://embedder.support-platform.svc.cluster.local:8001"`
	// Endpoint of the reranker (POST /rerank → ordered scores).
	RerankerURL string `envconfig:"RERANKER_URL" default:"http://reranker.support-platform.svc.cluster.local:8002"`

	// Postgres DSN for the pgvector store. Read-only is sufficient for
	// slm-router; doc ingestion writes from a separate CronJob.
	VectorDBDSN string `envconfig:"VECTOR_DB_DSN" required:"true"`
}

// Load reads env + routes file. Returns a fully resolved Config or an
// error suitable to return from main.
func Load() (*Config, error) {
	var env Env
	if err := envconfig.Process("", &env); err != nil {
		return nil, fmt.Errorf("load env: %w", err)
	}
	routes, err := loadRoutes(env.RoutesFile)
	if err != nil {
		return nil, fmt.Errorf("load routes: %w", err)
	}
	return &Config{Env: env, Routes: routes}, nil
}

// Config bundles env + routes so handlers can take a single dependency.
type Config struct {
	Env    Env
	Routes Routes
}

// Routes is the parsed YAML; see config_test.go for an example.
type Routes struct {
	// Products maps tenant_id → product-specific routing.
	Products map[string]ProductConfig `yaml:"products"`

	// DefaultTenant is the product key used when an incoming message's
	// tenant_id doesn't match any Products key or alias. mark8ly's
	// storefront sends platform-api UUIDs we don't know ahead of time;
	// the default catches them.
	//
	// Set to "" to disable the default (unknown tenants escalate).
	DefaultTenant string `yaml:"default_tenant"`
}

// ResolveProduct looks up the routing config for a tenant_id received
// on an Otto message. Resolution order:
//
//	1. exact match on Products map key
//	2. any ProductConfig that lists tenantID in its Aliases
//	3. the DefaultTenant (looked up in Products)
//	4. zero value + ok=false
func (r Routes) ResolveProduct(tenantID string) (ProductConfig, string, bool) {
	if p, ok := r.Products[tenantID]; ok {
		return p, tenantID, true
	}
	for name, p := range r.Products {
		if slices.Contains(p.Aliases, tenantID) {
			return p, name, true
		}
	}
	if r.DefaultTenant != "" {
		if p, ok := r.Products[r.DefaultTenant]; ok {
			return p, r.DefaultTenant, true
		}
	}
	return ProductConfig{}, "", false
}

// ProductConfig is the per-tenant policy slm-router applies.
type ProductConfig struct {
	// Path (inside the container) to the system prompt for this product.
	// Mounted from the same ConfigMap as routes.yaml.
	SystemPromptFile string `yaml:"system_prompt_file"`

	// pgvector namespace to query for this tenant's docs.
	RAGNamespace string `yaml:"rag_namespace"`

	// Aliases let one ProductConfig serve multiple tenant_id values.
	// Used for mark8ly because its storefront sends platform-api UUIDs
	// (one per store) but all of them should route to the same mark8ly
	// system prompt + RAG namespace.
	Aliases []string `yaml:"aliases"`

	// MCP servers slm-router can call as tools for this tenant.
	MCPServers []MCPServerConfig `yaml:"mcp_servers"`

	// Policy that decides when to escalate to human (NeedsHuman=true).
	Escalation EscalationPolicy `yaml:"escalation"`
}

// MCPServerConfig points slm-router at a per-product MCP server. Auth
// is via a header containing the secret value pulled from
// ExternalSecrets — the YAML carries only the secret reference, not
// the secret itself.
type MCPServerConfig struct {
	Name        string `yaml:"name"`
	URL         string `yaml:"url"`
	AuthHeader  string `yaml:"auth_header"`  // e.g. "X-MCP-Key"
	AuthEnvVar  string `yaml:"auth_env_var"` // env var that holds the secret
}

// EscalationPolicy decides when a conversation needs a human.
type EscalationPolicy struct {
	// Below this confidence score we escalate. 0–1 range.
	ConfidenceThreshold float64 `yaml:"confidence_threshold"`
	// Case-insensitive substrings; if any appears in the customer
	// message, escalate immediately (no AI reply).
	Keywords []string `yaml:"keywords"`
	// Tool call failures over this count in one turn escalate.
	MaxToolFailures int `yaml:"max_tool_failures"`
}

func loadRoutes(path string) (Routes, error) {
	if path == "" {
		return Routes{Products: map[string]ProductConfig{}}, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		// Missing routes file at boot is a recoverable error in dev
		// (the file will appear when the ConfigMap mounts). In prod
		// the readiness probe will keep us out of rotation until
		// the reload loop succeeds.
		if os.IsNotExist(err) {
			return Routes{Products: map[string]ProductConfig{}}, nil
		}
		return Routes{}, err
	}
	var r Routes
	if err := yaml.Unmarshal(b, &r); err != nil {
		return Routes{}, fmt.Errorf("parse %s: %w", path, err)
	}
	if r.Products == nil {
		r.Products = map[string]ProductConfig{}
	}
	return r, nil
}
