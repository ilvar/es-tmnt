package proxy

import (
	"fmt"
	"net/http"
	"strings"
)

type routeHandler func(method string, segments []string) (RequestAction, string, bool, error)

func routeForPath(method string, path string) (RequestAction, string, error) {
	segments := splitPath(path)
	for _, handler := range []routeHandler{
		matchSearch,
		matchIndexing,
		matchBulk,
		matchUpdate,
		matchMapping,
		matchCatIndices,
	} {
		action, index, ok, err := handler(method, segments)
		if err != nil {
			return "", "", err
		}
		if ok {
			return action, index, nil
		}
	}
	return "", "", fmt.Errorf("unsupported path %q", path)
}

func matchSearch(method string, segments []string) (RequestAction, string, bool, error) {
	if len(segments) == 1 && segments[0] == "_search" {
		if method != http.MethodGet && method != http.MethodPost {
			return "", "", false, fmt.Errorf("unsupported method %q for search", method)
		}
		return ActionSearch, "", true, nil
	}
	if len(segments) == 2 && isSearchEndpoint(segments[1]) {
		index := segments[0]
		if err := validateIndex(index); err != nil {
			return "", "", false, err
		}
		if method != http.MethodGet && method != http.MethodPost {
			return "", "", false, fmt.Errorf("unsupported method %q for search", method)
		}
		return ActionSearch, index, true, nil
	}
	if len(segments) == 3 && segments[1] == "_doc" && segments[2] == "_search" {
		index := segments[0]
		if err := validateIndex(index); err != nil {
			return "", "", false, err
		}
		if method != http.MethodGet && method != http.MethodPost {
			return "", "", false, fmt.Errorf("unsupported method %q for search", method)
		}
		return ActionSearch, index, true, nil
	}
	return "", "", false, nil
}

func matchIndexing(method string, segments []string) (RequestAction, string, bool, error) {
	if len(segments) == 2 && segments[1] == "_doc" {
		if method != http.MethodPost {
			return "", "", false, fmt.Errorf("unsupported method %q for indexing", method)
		}
		index := segments[0]
		if err := validateIndex(index); err != nil {
			return "", "", false, err
		}
		return ActionIndex, index, true, nil
	}
	if len(segments) == 3 && segments[1] == "_doc" {
		if method != http.MethodPost && method != http.MethodPut {
			return "", "", false, fmt.Errorf("unsupported method %q for indexing", method)
		}
		index := segments[0]
		if err := validateIndex(index); err != nil {
			return "", "", false, err
		}
		return ActionIndex, index, true, nil
	}
	return "", "", false, nil
}

func matchBulk(method string, segments []string) (RequestAction, string, bool, error) {
	if method != http.MethodPost {
		return "", "", false, nil
	}
	if len(segments) == 1 && segments[0] == "_bulk" {
		return ActionBulk, "", true, nil
	}
	if len(segments) == 2 && segments[1] == "_bulk" {
		index := segments[0]
		if err := validateIndex(index); err != nil {
			return "", "", false, err
		}
		return ActionBulk, index, true, nil
	}
	return "", "", false, nil
}

func matchUpdate(method string, segments []string) (RequestAction, string, bool, error) {
	if len(segments) == 3 && segments[1] == "_update" {
		if method != http.MethodPost {
			return "", "", false, fmt.Errorf("unsupported method %q for update", method)
		}
		index := segments[0]
		if err := validateIndex(index); err != nil {
			return "", "", false, err
		}
		return ActionUpdate, index, true, nil
	}
	return "", "", false, nil
}

func matchMapping(method string, segments []string) (RequestAction, string, bool, error) {
	if len(segments) == 2 && segments[1] == "_mapping" {
		if method != http.MethodPut && method != http.MethodPost {
			return "", "", false, fmt.Errorf("unsupported method %q for mapping", method)
		}
		index := segments[0]
		if err := validateIndex(index); err != nil {
			return "", "", false, err
		}
		return ActionMapping, index, true, nil
	}
	return "", "", false, nil
}

func matchCatIndices(method string, segments []string) (RequestAction, string, bool, error) {
	if len(segments) < 2 || segments[0] != "_cat" || segments[1] != "indices" {
		return "", "", false, nil
	}
	if method != http.MethodGet {
		return "", "", false, fmt.Errorf("unsupported method %q for cat indices", method)
	}
	if len(segments) == 2 {
		return ActionCatIndices, "", true, nil
	}
	if len(segments) == 3 {
		index := segments[2]
		if err := validateIndex(index); err != nil {
			return "", "", false, err
		}
		return ActionCatIndices, index, true, nil
	}
	return "", "", false, fmt.Errorf("unsupported cat indices path")
}

func validateIndex(index string) error {
	if strings.TrimSpace(index) == "" || strings.Contains(index, ",") {
		return fmt.Errorf("unsupported index %q", index)
	}
	return nil
}

func isSearchEndpoint(action string) bool {
	switch action {
	case "_search", "_msearch", "_count":
		return true
	default:
		return false
	}
}
