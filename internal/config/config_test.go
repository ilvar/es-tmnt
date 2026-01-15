package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadUsesConfigFileAndEnvOverrides(t *testing.T) {
	sample := Config{
		Ports: Ports{
			HTTP:  9201,
			Admin: 9202,
		},
		UpstreamURL: "http://example.com",
		Mode:        "shared",
		TenantRegex: TenantRegex{
			Pattern: `^(?P<prefix>[^-]+)-(?P<tenant>[^-]+)(?P<postfix>.*)$`,
		},
		SharedIndex: SharedIndex{
			Name:          "shared-{{.index}}",
			AliasTemplate: "alias-{{.index}}-{{.tenant}}",
			TenantField:   "tenant_id",
		},
		IndexPerTenant: IndexPerTenant{
			IndexTemplate: "per-{{.tenant}}",
		},
		PassthroughPaths: []string{"/_cluster/health"},
	}
	payload, err := json.Marshal(sample)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	configPath := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(configPath, payload, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	t.Setenv(envConfigPath, configPath)
	t.Setenv(envHTTPPort, "9300")
	t.Setenv(envMode, "index-per-tenant")
	t.Setenv(envIndexPerTenantIndexTemplate, "tenant-{{.index}}-{{.tenant}}")
	t.Setenv(envPassthroughPaths, " /_cluster/health, /_snapshot ")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.Ports.HTTP != 9300 {
		t.Fatalf("expected HTTP port override, got %d", cfg.Ports.HTTP)
	}
	if cfg.Mode != "index-per-tenant" {
		t.Fatalf("expected mode override, got %q", cfg.Mode)
	}
	if cfg.IndexPerTenant.IndexTemplate != "tenant-{{.index}}-{{.tenant}}" {
		t.Fatalf("expected index template override, got %q", cfg.IndexPerTenant.IndexTemplate)
	}
	if len(cfg.PassthroughPaths) != 2 {
		t.Fatalf("expected passthrough paths override, got %v", cfg.PassthroughPaths)
	}
	if cfg.TenantRegex.Compiled == nil {
		t.Fatalf("expected compiled tenant regex")
	}
}

func TestLoadMissingConfigFile(t *testing.T) {
	t.Setenv(envConfigPath, filepath.Join(t.TempDir(), "missing.json"))

	_, err := Load()
	if err == nil {
		t.Fatalf("expected error for missing config file")
	}
	if !strings.Contains(err.Error(), "read config file") {
		t.Fatalf("expected read config file error, got %v", err)
	}
}

func TestValidateErrors(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(cfg *Config)
		wantErr string
	}{
		{
			name: "missing upstream",
			mutate: func(cfg *Config) {
				cfg.UpstreamURL = ""
			},
			wantErr: "upstream_url is required",
		},
		{
			name: "invalid upstream",
			mutate: func(cfg *Config) {
				cfg.UpstreamURL = ":bad"
			},
			wantErr: "upstream_url must be a valid URL",
		},
		{
			name: "invalid mode",
			mutate: func(cfg *Config) {
				cfg.Mode = "unknown"
			},
			wantErr: "mode must be",
		},
		{
			name: "missing tenant regex",
			mutate: func(cfg *Config) {
				cfg.TenantRegex.Pattern = ""
			},
			wantErr: "tenant_regex.pattern is required",
		},
		{
			name: "invalid tenant regex",
			mutate: func(cfg *Config) {
				cfg.TenantRegex.Pattern = "["
			},
			wantErr: "tenant_regex.pattern is invalid",
		},
		{
			name: "missing tenant capture groups",
			mutate: func(cfg *Config) {
				cfg.TenantRegex.Pattern = "^(.*)$"
			},
			wantErr: "tenant_regex.pattern must include named capture groups",
		},
		{
			name: "empty passthrough",
			mutate: func(cfg *Config) {
				cfg.PassthroughPaths = []string{""}
			},
			wantErr: "passthrough_paths[0] must not be empty",
		},
		{
			name: "missing shared index name",
			mutate: func(cfg *Config) {
				cfg.Mode = "shared"
				cfg.SharedIndex.Name = ""
			},
			wantErr: "shared_index.name is required",
		},
		{
			name: "missing shared alias template",
			mutate: func(cfg *Config) {
				cfg.Mode = "shared"
				cfg.SharedIndex.AliasTemplate = ""
			},
			wantErr: "shared_index.alias_template is required",
		},
		{
			name: "missing shared tenant field",
			mutate: func(cfg *Config) {
				cfg.Mode = "shared"
				cfg.SharedIndex.TenantField = ""
			},
			wantErr: "shared_index.tenant_field is required",
		},
		{
			name: "missing index per tenant template",
			mutate: func(cfg *Config) {
				cfg.Mode = "index-per-tenant"
				cfg.IndexPerTenant.IndexTemplate = ""
			},
			wantErr: "index_per_tenant.index_template is required",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Default()
			tc.mutate(&cfg)
			err := cfg.Validate()
			if err == nil {
				t.Fatalf("expected error")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected error %q, got %v", tc.wantErr, err)
			}
		})
	}
}
