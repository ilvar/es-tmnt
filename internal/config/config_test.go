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

func TestOverrideString(t *testing.T) {
	t.Setenv(envSharedIndexName, "test-index")
	var target string
	overrideString(envSharedIndexName, &target)
	if target != "test-index" {
		t.Fatalf("expected test-index, got %q", target)
	}
}

func TestOverrideStringEmpty(t *testing.T) {
	t.Setenv(envSharedIndexName, "")
	var target string
	overrideString(envSharedIndexName, &target)
	if target != "" {
		t.Fatalf("expected empty string, got %q", target)
	}
}

func TestOverrideStringWhitespace(t *testing.T) {
	t.Setenv(envSharedIndexName, "   ")
	var target string
	overrideString(envSharedIndexName, &target)
	if target != "" {
		t.Fatalf("expected empty string, got %q", target)
	}
}

func TestOverrideInt(t *testing.T) {
	t.Setenv(envHTTPPort, "8080")
	target := 9200
	overrideInt(envHTTPPort, &target)
	if target != 8080 {
		t.Fatalf("expected 8080, got %d", target)
	}
}

func TestOverrideIntInvalid(t *testing.T) {
	t.Setenv(envHTTPPort, "invalid")
	target := 9200
	overrideInt(envHTTPPort, &target)
	if target != 9200 {
		t.Fatalf("expected unchanged 9200, got %d", target)
	}
}

func TestOverrideIntEmpty(t *testing.T) {
	t.Setenv(envHTTPPort, "")
	target := 9200
	overrideInt(envHTTPPort, &target)
	if target != 9200 {
		t.Fatalf("expected unchanged 9200, got %d", target)
	}
}

func TestOverrideBool(t *testing.T) {
	t.Setenv("ES_TMNT_TEST_BOOL", "true")
	var target bool
	overrideBool("ES_TMNT_TEST_BOOL", &target)
	if target != true {
		t.Fatalf("expected true, got %v", target)
	}
}

func TestOverrideBoolFalse(t *testing.T) {
	t.Setenv("ES_TMNT_TEST_BOOL", "false")
	target := true
	overrideBool("ES_TMNT_TEST_BOOL", &target)
	if target != false {
		t.Fatalf("expected false, got %v", target)
	}
}

func TestOverrideBoolInvalid(t *testing.T) {
	t.Setenv("ES_TMNT_TEST_BOOL", "invalid")
	target := false
	overrideBool("ES_TMNT_TEST_BOOL", &target)
	if target != false {
		t.Fatalf("expected unchanged false, got %v", target)
	}
}

func TestOverrideBoolEmpty(t *testing.T) {
	t.Setenv("ES_TMNT_TEST_BOOL", "")
	target := false
	overrideBool("ES_TMNT_TEST_BOOL", &target)
	if target != false {
		t.Fatalf("expected unchanged false, got %v", target)
	}
}

func TestOverridePassthrough(t *testing.T) {
	t.Setenv(envPassthroughPaths, "/path1,/path2,/path3")
	var target []string
	overridePassthrough(envPassthroughPaths, &target)
	if len(target) != 3 {
		t.Fatalf("expected 3 paths, got %d", len(target))
	}
	if target[0] != "/path1" {
		t.Fatalf("expected /path1, got %q", target[0])
	}
}

func TestOverridePassthroughWithSpaces(t *testing.T) {
	t.Setenv(envPassthroughPaths, " /path1 , /path2 ")
	var target []string
	overridePassthrough(envPassthroughPaths, &target)
	if len(target) != 2 {
		t.Fatalf("expected 2 paths, got %d", len(target))
	}
	if target[0] != "/path1" {
		t.Fatalf("expected /path1, got %q", target[0])
	}
}

func TestOverridePassthroughEmptyParts(t *testing.T) {
	t.Setenv(envPassthroughPaths, "/path1,,/path2,")
	var target []string
	overridePassthrough(envPassthroughPaths, &target)
	if len(target) != 2 {
		t.Fatalf("expected 2 paths, got %d", len(target))
	}
}

func TestLoadInvalidJSON(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(configPath, []byte("invalid json"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv(envConfigPath, configPath)

	_, err := Load()
	if err == nil {
		t.Fatalf("expected error for invalid JSON")
	}
	if !strings.Contains(err.Error(), "parse config file") {
		t.Fatalf("expected parse config file error, got %v", err)
	}
}

func TestLoadEnvOverridesAllFields(t *testing.T) {
	t.Setenv(envHTTPPort, "9201")
	t.Setenv(envAdminPort, "9202")
	t.Setenv(envUpstreamURL, "http://test.com")
	t.Setenv(envMode, "shared")
	t.Setenv(envTenantRegexPattern, `^(?P<prefix>[^-]+)-(?P<tenant>[^-]+)(?P<postfix>.*)$`)
	t.Setenv(envSharedIndexName, "shared-{{.index}}")
	t.Setenv(envSharedIndexAliasTemplate, "alias-{{.index}}-{{.tenant}}")
	t.Setenv(envSharedIndexTenantField, "tenant_id")
	t.Setenv(envIndexPerTenantIndexTemplate, "per-{{.tenant}}")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.Ports.HTTP != 9201 {
		t.Fatalf("expected HTTP port 9201, got %d", cfg.Ports.HTTP)
	}
	if cfg.Ports.Admin != 9202 {
		t.Fatalf("expected Admin port 9202, got %d", cfg.Ports.Admin)
	}
	if cfg.UpstreamURL != "http://test.com" {
		t.Fatalf("expected http://test.com, got %q", cfg.UpstreamURL)
	}
	if cfg.Mode != "shared" {
		t.Fatalf("expected shared mode, got %q", cfg.Mode)
	}
}

func TestDefaultConfig(t *testing.T) {
	cfg := Default()
	if cfg.Ports.HTTP != 8080 {
		t.Fatalf("expected HTTP port 8080, got %d", cfg.Ports.HTTP)
	}
	if cfg.Ports.Admin != 8081 {
		t.Fatalf("expected Admin port 8081, got %d", cfg.Ports.Admin)
	}
	if cfg.UpstreamURL != "http://localhost:9200" {
		t.Fatalf("expected http://localhost:9200, got %q", cfg.UpstreamURL)
	}
	if cfg.Mode != "shared" {
		t.Fatalf("expected mode shared, got %q", cfg.Mode)
	}
	if cfg.TenantRegex.Pattern == "" {
		t.Fatalf("expected tenant regex pattern")
	}
	if cfg.SharedIndex.Name == "" {
		t.Fatalf("expected shared index name")
	}
	if cfg.SharedIndex.AliasTemplate == "" {
		t.Fatalf("expected shared alias template")
	}
	if cfg.SharedIndex.TenantField == "" {
		t.Fatalf("expected shared tenant field")
	}
	if cfg.IndexPerTenant.IndexTemplate == "" {
		t.Fatalf("expected index per tenant template")
	}
}

func TestValidateIndexPerTenantMode(t *testing.T) {
	cfg := Default()
	cfg.Mode = "index-per-tenant"
	cfg.IndexPerTenant.IndexTemplate = "{{.index}}-{{.tenant}}"
	
	err := cfg.Validate()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateSharedMode(t *testing.T) {
	cfg := Default()
	cfg.Mode = "shared"
	cfg.SharedIndex.Name = "shared-{{.index}}"
	cfg.SharedIndex.AliasTemplate = "alias-{{.index}}-{{.tenant}}"
	cfg.SharedIndex.TenantField = "tenant_id"
	
	err := cfg.Validate()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidatePassthroughPaths(t *testing.T) {
	cfg := Default()
	cfg.PassthroughPaths = []string{"/path1", "/path2", "/path3"}
	
	err := cfg.Validate()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadEnvOverrideAllStringFields(t *testing.T) {
	t.Setenv(envUpstreamURL, "http://override.com")
	t.Setenv(envMode, "index-per-tenant")
	t.Setenv(envTenantRegexPattern, `^(?P<prefix>[^-]+)-(?P<tenant>[^-]+)(?P<postfix>.*)$`)
	t.Setenv(envSharedIndexName, "override-shared")
	t.Setenv(envSharedIndexAliasTemplate, "override-alias-{{.index}}-{{.tenant}}")
	t.Setenv(envSharedIndexTenantField, "override_tenant_id")
	t.Setenv(envIndexPerTenantIndexTemplate, "override-{{.index}}-{{.tenant}}")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.UpstreamURL != "http://override.com" {
		t.Fatalf("expected http://override.com, got %q", cfg.UpstreamURL)
	}
	if cfg.Mode != "index-per-tenant" {
		t.Fatalf("expected index-per-tenant, got %q", cfg.Mode)
	}
}

func TestLoadConfigFileError(t *testing.T) {
	t.Setenv(envConfigPath, "/nonexistent/path/config.json")
	_, err := Load()
	if err == nil {
		t.Fatalf("expected error for nonexistent config file")
	}
}

func TestLoadInvalidJSONConfig(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "bad-config.json")
	if err := os.WriteFile(configPath, []byte("not json"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv(envConfigPath, configPath)

	_, err := Load()
	if err == nil {
		t.Fatalf("expected error for invalid JSON")
	}
}

func TestLoadValidateError(t *testing.T) {
	// Set upstream URL to empty string - this should trigger validation error
	// But Load() uses Default() first which has a valid URL, so we need to override it
	t.Setenv(envUpstreamURL, "   ")
	// Also set invalid mode to trigger validation error
	t.Setenv(envMode, "invalid")
	_, err := Load()
	if err == nil {
		t.Fatalf("expected error for invalid config")
	}
	if !strings.Contains(err.Error(), "mode must be") {
		t.Fatalf("expected mode error, got %v", err)
	}
}

func TestLoadValidateErrorEmptyUpstream(t *testing.T) {
	// Use config file to set empty upstream URL
	sample := Config{
		UpstreamURL: "",
		TenantRegex: TenantRegex{
			Pattern: `^(?P<prefix>[^-]+)-(?P<tenant>[^-]+)(?P<postfix>.*)$`,
		},
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

	_, err = Load()
	if err == nil {
		t.Fatalf("expected error for invalid config")
	}
	if !strings.Contains(err.Error(), "upstream_url is required") {
		t.Fatalf("expected upstream_url error, got %v", err)
	}
}

func TestLoadRegexCompileError(t *testing.T) {
	t.Setenv(envTenantRegexPattern, "[invalid")
	_, err := Load()
	if err == nil {
		t.Fatalf("expected error for invalid regex")
	}
	if !strings.Contains(err.Error(), "tenant_regex.pattern is invalid") {
		t.Fatalf("expected regex error, got %v", err)
	}
}

func TestLoadAllEnvOverrides(t *testing.T) {
	t.Setenv(envHTTPPort, "9201")
	t.Setenv(envAdminPort, "9202")
	t.Setenv(envUpstreamURL, "http://test.example.com:9200")
	t.Setenv(envMode, "shared")
	t.Setenv(envTenantRegexPattern, `^(?P<prefix>[^-]+)-(?P<tenant>[^-]+)(?P<postfix>.*)$`)
	t.Setenv(envSharedIndexName, "shared-{{.index}}")
	t.Setenv(envSharedIndexAliasTemplate, "alias-{{.index}}-{{.tenant}}")
	t.Setenv(envSharedIndexTenantField, "tenant_id")
	t.Setenv(envIndexPerTenantIndexTemplate, "per-{{.tenant}}")
	t.Setenv(envPassthroughPaths, "/path1,/path2")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.Ports.HTTP != 9201 {
		t.Fatalf("expected 9201, got %d", cfg.Ports.HTTP)
	}
	if cfg.Ports.Admin != 9202 {
		t.Fatalf("expected 9202, got %d", cfg.Ports.Admin)
	}
	if cfg.UpstreamURL != "http://test.example.com:9200" {
		t.Fatalf("expected http://test.example.com:9200, got %q", cfg.UpstreamURL)
	}
	if cfg.Mode != "shared" {
		t.Fatalf("expected shared, got %q", cfg.Mode)
	}
}

func TestLoadInvalidIntPort(t *testing.T) {
	t.Setenv(envHTTPPort, "not-a-number")
	target := 8080
	overrideInt(envHTTPPort, &target)
	if target != 8080 {
		t.Fatalf("expected unchanged 8080, got %d", target)
	}
}

func TestLoadInvalidIntPortNegative(t *testing.T) {
	t.Setenv(envAdminPort, "-1")
	target := 8081
	overrideInt(envAdminPort, &target)
	// Negative numbers are valid integers, so it should be set
	if target == 8081 {
		t.Fatalf("expected port to be set to -1, got %d", target)
	}
}

func TestLoadBoolTrue(t *testing.T) {
	t.Setenv("TEST_BOOL", "1")
	var target bool
	overrideBool("TEST_BOOL", &target)
	if target != true {
		t.Fatalf("expected true, got %v", target)
	}
}

func TestLoadBoolFalseValue(t *testing.T) {
	t.Setenv("TEST_BOOL", "0")
	target := true
	overrideBool("TEST_BOOL", &target)
	if target != false {
		t.Fatalf("expected false, got %v", target)
	}
}

func TestOverridePassthroughEmptyString(t *testing.T) {
	t.Setenv(envPassthroughPaths, "")
	var target []string
	originalLen := len(target)
	overridePassthrough(envPassthroughPaths, &target)
	if len(target) != originalLen {
		t.Fatalf("expected unchanged passthrough paths")
	}
}

func TestOverridePassthroughWhitespaceOnly(t *testing.T) {
	t.Setenv(envPassthroughPaths, "   ")
	var target []string
	overridePassthrough(envPassthroughPaths, &target)
	if len(target) != 0 {
		t.Fatalf("expected empty passthrough paths")
	}
}

func TestValidateURLParseError(t *testing.T) {
	cfg := Default()
	cfg.UpstreamURL = ":invalid://url"
	err := cfg.Validate()
	if err == nil {
		t.Fatalf("expected error for invalid URL")
	}
	if !strings.Contains(err.Error(), "must be a valid URL") {
		t.Fatalf("expected URL validation error, got %v", err)
	}
}

func TestValidateRegexCompileErrorInValidate(t *testing.T) {
	cfg := Default()
	cfg.TenantRegex.Pattern = "[invalid"
	err := cfg.Validate()
	if err == nil {
		t.Fatalf("expected error for invalid regex")
	}
	if !strings.Contains(err.Error(), "tenant_regex.pattern is invalid") {
		t.Fatalf("expected regex error, got %v", err)
	}
}

func TestValidateModeWhitespace(t *testing.T) {
	cfg := Default()
	cfg.Mode = "  shared  "
	err := cfg.Validate()
	if err != nil {
		t.Fatalf("expected whitespace to be trimmed, got error: %v", err)
	}
}

func TestValidateModeCaseInsensitive(t *testing.T) {
	cfg := Default()
	cfg.Mode = "INDEX-PER-TENANT"
	err := cfg.Validate()
	if err != nil {
		t.Fatalf("expected case insensitive validation, got error: %v", err)
	}
}

func TestValidatePassthroughPathsWhitespace(t *testing.T) {
	cfg := Default()
	cfg.PassthroughPaths = []string{"  /path1  ", "  /path2  "}
	err := cfg.Validate()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadWithoutConfigFile(t *testing.T) {
	// Clear config file env var
	t.Setenv(envConfigPath, "")
	t.Setenv(envTenantRegexPattern, `^(?P<prefix>[^-]+)-(?P<tenant>[^-]+)(?P<postfix>.*)$`)
	
	cfg, err := Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.TenantRegex.Compiled == nil {
		t.Fatalf("expected compiled regex")
	}
}

func TestLoadEnvOverridesConfigFile(t *testing.T) {
	sample := Config{
		Ports: Ports{HTTP: 9201},
		UpstreamURL: "http://file.example.com",
		Mode: "shared",
		TenantRegex: TenantRegex{
			Pattern: `^(?P<prefix>[^-]+)-(?P<tenant>[^-]+)(?P<postfix>.*)$`,
		},
		SharedIndex: SharedIndex{
			Name:          "shared-{{.index}}",
			AliasTemplate: "alias-{{.index}}-{{.tenant}}",
			TenantField:   "tenant_id",
		},
	}
	payload, _ := json.Marshal(sample)
	configPath := filepath.Join(t.TempDir(), "config.json")
	os.WriteFile(configPath, payload, 0o600)
	
	t.Setenv(envConfigPath, configPath)
	t.Setenv(envHTTPPort, "9300") // Override from file
	
	cfg, err := Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.Ports.HTTP != 9300 {
		t.Fatalf("expected env override 9300, got %d", cfg.Ports.HTTP)
	}
	if cfg.UpstreamURL != "http://file.example.com" {
		t.Fatalf("expected URL from file, got %q", cfg.UpstreamURL)
	}
}
