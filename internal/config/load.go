package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
)

const (
	envConfigPath                = "ES_TMNT_CONFIG"
	envHTTPPort                  = "ES_TMNT_HTTP_PORT"
	envAdminPort                 = "ES_TMNT_ADMIN_PORT"
	envUpstreamURL               = "ES_TMNT_UPSTREAM_URL"
	envMode                      = "ES_TMNT_MODE"
	envPassthrough               = "ES_TMNT_PASSTHROUGH"
	envTenantRegexPattern        = "ES_TMNT_TENANT_REGEX_PATTERN"
	envSharedIndexName           = "ES_TMNT_SHARED_INDEX_NAME"
	envSharedIndexAliasFormat    = "ES_TMNT_SHARED_INDEX_ALIAS_FORMAT"
	envSharedIndexTenantField    = "ES_TMNT_SHARED_INDEX_TENANT_FIELD"
	envIndexPerTenantIndexFormat = "ES_TMNT_INDEX_PER_TENANT_FORMAT"
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
	overrideString(envTenantRegexPattern, &cfg.TenantRegex.Pattern)
	overrideString(envSharedIndexName, &cfg.SharedIndex.Name)
	overrideString(envSharedIndexAliasFormat, &cfg.SharedIndex.AliasFormat)
	overrideString(envSharedIndexTenantField, &cfg.SharedIndex.TenantField)
	overrideString(envIndexPerTenantIndexFormat, &cfg.IndexPerTenant.IndexFormat)
	overridePassthrough(envPassthrough, &cfg.Passthrough)

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
