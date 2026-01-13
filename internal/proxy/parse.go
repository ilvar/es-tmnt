package proxy

import (
	"net/http"
	"regexp"
)

var tenantPattern = regexp.MustCompile(`^/tenant/([^/]+)/`)

type RequestInfo struct {
	Path          string
	Tenant        string
	IsPassthrough bool
}

func ParseRequest(r *http.Request, rewriter *Rewriter) RequestInfo {
	info := RequestInfo{Path: r.URL.Path}
	if matches := tenantPattern.FindStringSubmatch(r.URL.Path); len(matches) == 2 {
		info.Tenant = matches[1]
	}
	if rewriter != nil {
		info.IsPassthrough = rewriter.isPassthrough(r.URL.Path)
	}
	return info
}
