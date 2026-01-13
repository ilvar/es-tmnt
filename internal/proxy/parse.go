package proxy

import (
	"fmt"
	"regexp"
	"strings"

	"es-tmnt/internal/config"
)

const (
	tenantPrefixGroup  = "prefix"
	tenantIDGroup      = "tenant"
	tenantPostfixGroup = "postfix"
)

type TenantExtractor struct {
	regex       *regexp.Regexp
	prefixIndex int
	tenantIndex int
	postIndex   int
}

func NewTenantExtractor(cfg config.Config) (*TenantExtractor, error) {
	if strings.TrimSpace(cfg.TenantRegex.Pattern) == "" {
		return nil, fmt.Errorf("tenant regex pattern is required")
	}
	compiled, err := regexp.Compile(cfg.TenantRegex.Pattern)
	if err != nil {
		return nil, fmt.Errorf("compile tenant regex: %w", err)
	}
	prefixIndex := compiled.SubexpIndex(tenantPrefixGroup)
	tenantIndex := compiled.SubexpIndex(tenantIDGroup)
	postIndex := compiled.SubexpIndex(tenantPostfixGroup)
	if prefixIndex < 0 || tenantIndex < 0 || postIndex < 0 {
		return nil, fmt.Errorf("tenant regex must include named capture groups %q, %q, and %q",
			tenantPrefixGroup, tenantIDGroup, tenantPostfixGroup)
	}
	return &TenantExtractor{
		regex:       compiled,
		prefixIndex: prefixIndex,
		tenantIndex: tenantIndex,
		postIndex:   postIndex,
	}, nil
}

func (t *TenantExtractor) Extract(path string) (tenant string, rewritten string, ok bool) {
	matches := t.regex.FindStringSubmatch(path)
	if matches == nil {
		return "", path, false
	}
	tenant = matches[t.tenantIndex]
	prefix := matches[t.prefixIndex]
	postfix := matches[t.postIndex]
	switch {
	case prefix == "" && postfix == "":
		rewritten = "/"
	case prefix == "":
		rewritten = postfix
	case postfix == "":
		rewritten = prefix
	default:
		rewritten = prefix + postfix
	}
	if rewritten == "" {
		rewritten = "/"
	}
	return tenant, rewritten, true
}
