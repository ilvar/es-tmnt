package config

import (
	"fmt"
	"net/url"
	"regexp"
	"regexp/syntax"
	"strings"
)

const (
	tenantPrefixGroup  = "prefix"
	tenantIDGroup      = "tenant"
	tenantPostfixGroup = "postfix"
)

func (c Config) Validate() error {
	if strings.TrimSpace(c.UpstreamURL) == "" {
		return fmt.Errorf("upstream_url is required")
	}
	if _, err := url.ParseRequestURI(c.UpstreamURL); err != nil {
		return fmt.Errorf("upstream_url must be a valid URL: %w", err)
	}

	mode := strings.ToLower(strings.TrimSpace(c.Mode))
	switch mode {
	case "shared", "index-per-tenant":
	default:
		return fmt.Errorf("mode must be \"shared\" or \"index-per-tenant\" (got %q)", c.Mode)
	}

	pattern := strings.TrimSpace(c.TenantRegex.Pattern)
	if pattern == "" {
		return fmt.Errorf("tenant_regex.pattern is required")
	}
	compiled, err := regexp.Compile(pattern)
	if err != nil {
		return fmt.Errorf("tenant_regex.pattern is invalid: %w", err)
	}
	if compiled.SubexpIndex(tenantPrefixGroup) < 0 ||
		compiled.SubexpIndex(tenantIDGroup) < 0 ||
		compiled.SubexpIndex(tenantPostfixGroup) < 0 {
		return fmt.Errorf("tenant_regex.pattern must include named capture groups %q, %q, and %q",
			tenantPrefixGroup, tenantIDGroup, tenantPostfixGroup)
	}
	if err := validateRegexComplexity(pattern); err != nil {
		return err
	}

	for i, path := range c.PassthroughPaths {
		if strings.TrimSpace(path) == "" {
			return fmt.Errorf("passthrough_paths[%d] must not be empty", i)
		}
	}

	if mode == "shared" {
		if strings.TrimSpace(c.SharedIndex.Name) == "" {
			return fmt.Errorf("shared_index.name is required in shared mode")
		}
		if strings.TrimSpace(c.SharedIndex.AliasTemplate) == "" {
			return fmt.Errorf("shared_index.alias_template is required in shared mode")
		}
		if strings.TrimSpace(c.SharedIndex.TenantField) == "" {
			return fmt.Errorf("shared_index.tenant_field is required in shared mode")
		}
	}

	for i, pattern := range c.SharedIndex.DenyPatterns {
		trimmed := strings.TrimSpace(pattern)
		if trimmed == "" {
			return fmt.Errorf("shared_index.deny_patterns[%d] must not be empty", i)
		}
		if _, err := regexp.Compile(trimmed); err != nil {
			return fmt.Errorf("shared_index.deny_patterns[%d] is invalid: %w", i, err)
		}
	}

	if mode == "index-per-tenant" {
		if strings.TrimSpace(c.IndexPerTenant.IndexTemplate) == "" {
			return fmt.Errorf("index_per_tenant.index_template is required in index-per-tenant mode")
		}
	}

	if c.Auth.Required && strings.TrimSpace(c.Auth.Header) == "" {
		return fmt.Errorf("auth.header is required when auth.required is true")
	}

	return nil
}

func validateRegexComplexity(pattern string) error {
	parsed, err := syntax.Parse(pattern, syntax.Perl)
	if err != nil {
		return fmt.Errorf("tenant_regex.pattern is invalid: %w", err)
	}
	if hasNestedQuantifiers(parsed, false) {
		return fmt.Errorf("tenant_regex.pattern contains nested quantifiers and may be vulnerable to ReDoS")
	}
	return nil
}

func hasNestedQuantifiers(re *syntax.Regexp, inRepeat bool) bool {
	if re == nil {
		return false
	}
	isRepeat := re.Op == syntax.OpStar || re.Op == syntax.OpPlus || re.Op == syntax.OpQuest || re.Op == syntax.OpRepeat
	if inRepeat && isRepeat {
		return true
	}
	nextRepeat := inRepeat || isRepeat
	for _, sub := range re.Sub {
		if hasNestedQuantifiers(sub, nextRepeat) {
			return true
		}
	}
	return false
}
