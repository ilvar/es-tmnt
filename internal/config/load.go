package config

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"regexp"
	"strconv"
	"strings"
)

const (
	envConfigPath                  = "ES_TMNT_CONFIG"
	envHTTPPort                    = "ES_TMNT_HTTP_PORT"
	envAdminPort                   = "ES_TMNT_ADMIN_PORT"
	envUpstreamURL                 = "ES_TMNT_UPSTREAM_URL"
	envMode                        = "ES_TMNT_MODE"
	envVerbose                     = "ES_TMNT_VERBOSE"
	envPassthroughPaths            = "ES_TMNT_PASSTHROUGH_PATHS"
	envTenantRegexPattern          = "ES_TMNT_TENANT_REGEX_PATTERN"
	envSharedIndexName             = "ES_TMNT_SHARED_INDEX_NAME"
	envSharedIndexAliasTemplate    = "ES_TMNT_SHARED_INDEX_ALIAS_TEMPLATE"
	envSharedIndexTenantField      = "ES_TMNT_SHARED_INDEX_TENANT_FIELD"
	envSharedIndexDenyPatterns     = "ES_TMNT_SHARED_INDEX_DENY_PATTERNS"
	envIndexPerTenantIndexTemplate = "ES_TMNT_INDEX_PER_TENANT_TEMPLATE"
	envAuthRequired                = "ES_TMNT_AUTH_REQUIRED"
	envAuthHeader                  = "ES_TMNT_AUTH_HEADER"
)

func Load() (Config, error) {
	cfg := Default()

	if path := strings.TrimSpace(os.Getenv(envConfigPath)); path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return Config{}, fmt.Errorf("read config file: %w", err)
		}
		if err := json.Unmarshal(data, &cfg); err != nil {
			return Config{}, fmt.Errorf("parse config file: %w", err)
		}
	}

	overrideInt(envHTTPPort, &cfg.Ports.HTTP)
	overrideInt(envAdminPort, &cfg.Ports.Admin)
	overrideString(envUpstreamURL, &cfg.UpstreamURL)
	overrideString(envMode, &cfg.Mode)
	overrideBool(envVerbose, &cfg.Verbose)
	overrideString(envTenantRegexPattern, &cfg.TenantRegex.Pattern)
	overrideString(envSharedIndexName, &cfg.SharedIndex.Name)
	overrideString(envSharedIndexAliasTemplate, &cfg.SharedIndex.AliasTemplate)
	overrideString(envSharedIndexTenantField, &cfg.SharedIndex.TenantField)
	overrideStringSlice(envSharedIndexDenyPatterns, &cfg.SharedIndex.DenyPatterns)
	overrideString(envIndexPerTenantIndexTemplate, &cfg.IndexPerTenant.IndexTemplate)
	overridePassthrough(envPassthroughPaths, &cfg.PassthroughPaths)
	overrideBool(envAuthRequired, &cfg.Auth.Required)
	overrideString(envAuthHeader, &cfg.Auth.Header)

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}

	compiled, err := regexp.Compile(cfg.TenantRegex.Pattern)
	if err != nil {
		return Config{}, fmt.Errorf("tenant_regex.pattern is invalid: %w", err)
	}
	cfg.TenantRegex.Compiled = compiled
	cfg.SharedIndex.DenyCompiled = compilePatterns(cfg.SharedIndex.DenyPatterns)

	return cfg, nil
}

func overrideString(key string, target *string) {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		*target = value
	}
}

func overrideInt(key string, target *int) {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil {
			*target = parsed
		}
	}
}

func overrideBool(key string, target *bool) {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		if parsed, err := strconv.ParseBool(value); err == nil {
			*target = parsed
		}
	}
}

func overridePassthrough(key string, target *[]string) {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		parts := strings.Split(value, ",")
		result := make([]string, 0, len(parts))
		for _, part := range parts {
			trimmed := strings.TrimSpace(part)
			if trimmed != "" {
				result = append(result, trimmed)
			}
		}
		*target = result
	}
}

func overrideStringSlice(key string, target *[]string) {
	overridePassthrough(key, target)
}

func compilePatterns(patterns []string) []*regexp.Regexp {
	if len(patterns) == 0 {
		return nil
	}
	compiled := make([]*regexp.Regexp, 0, len(patterns))
	for _, pattern := range patterns {
		if pattern == "" {
			continue
		}
		re, err := regexp.Compile(pattern)
		if err != nil {
			log.Printf("warning: failed to compile regex pattern %q: %v", pattern, err)
			continue
		}
		compiled = append(compiled, re)
	}
	return compiled
}
