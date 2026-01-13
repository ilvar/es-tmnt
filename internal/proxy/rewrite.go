package proxy

import (
	"regexp"
	"strings"

	"es-tmnt/internal/config"
)

type Rewriter struct {
	mode        string
	passthrough []string
	regex       *regexp.Regexp
	replaceWith string
}

func NewRewriter(cfg config.Config) *Rewriter {
	var compiled *regexp.Regexp
	if cfg.Regex.Enabled && cfg.Regex.Pattern != "" {
		compiled = regexp.MustCompile(cfg.Regex.Pattern)
	}
	return &Rewriter{
		mode:        strings.ToLower(cfg.Mode),
		passthrough: cfg.Passthrough,
		regex:       compiled,
		replaceWith: cfg.Regex.Replacement,
	}
}

func (r *Rewriter) RewritePath(path string) string {
	if r.mode == "passthrough" || r.mode == "" {
		return path
	}
	if r.isPassthrough(path) {
		return path
	}
	if r.mode == "regex" && r.regex != nil {
		return r.regex.ReplaceAllString(path, r.replaceWith)
	}
	return path
}

func (r *Rewriter) isPassthrough(path string) bool {
	for _, prefix := range r.passthrough {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}
