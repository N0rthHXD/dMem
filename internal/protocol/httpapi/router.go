package httpapi

import (
	"net/url"
	"sort"
	"strings"
)

// matchRoute finds the route whose method and path pattern match the request.
//
// When no route matches the method but at least one route matches the path, the
// sorted set of allowed methods is returned so the caller can answer 405. When
// nothing matches the path, both results are empty and the caller answers 404.
//
// Matching is exact: a pattern and a request path match only when they have the
// same number of segments. There is no trailing-slash redirect and no path
// cleaning, so `/v1/memories/` does not match `/v1/memories`.
func matchRoute(routes []route, escapedPath, method string) (*route, routeParams, []string) {
	segments, ok := splitPath(escapedPath)
	if !ok {
		return nil, nil, nil
	}

	allowed := map[string]struct{}{}
	for i := range routes {
		pattern, ok := splitPath(routes[i].Path)
		if !ok {
			continue
		}
		params, matched := matchSegments(pattern, segments)
		if !matched {
			continue
		}
		if routes[i].Method == method {
			return &routes[i], params, nil
		}
		allowed[routes[i].Method] = struct{}{}
	}
	if len(allowed) == 0 {
		return nil, nil, nil
	}
	methods := make([]string, 0, len(allowed))
	for m := range allowed {
		methods = append(methods, m)
	}
	sort.Strings(methods)
	return nil, nil, methods
}

// splitPath splits an escaped URL path into decoded segments. It rejects a path
// that is not rooted at "/" or that contains an invalid escape.
func splitPath(escapedPath string) ([]string, bool) {
	if escapedPath == "" || escapedPath[0] != '/' {
		return nil, false
	}
	if escapedPath == "/" {
		return []string{}, true
	}
	parts := strings.Split(escapedPath[1:], "/")
	for i, part := range parts {
		decoded, err := url.PathUnescape(part)
		if err != nil {
			return nil, false
		}
		parts[i] = decoded
	}
	return parts, true
}

// matchSegments matches one pattern against one decoded path. A {name} segment
// matches any non-empty segment and captures it.
func matchSegments(pattern, actual []string) (routeParams, bool) {
	if len(pattern) != len(actual) {
		return nil, false
	}
	var params routeParams
	for i := range pattern {
		if name, ok := parameterName(pattern[i]); ok {
			if actual[i] == "" {
				return nil, false
			}
			if params == nil {
				params = routeParams{}
			}
			params[name] = actual[i]
			continue
		}
		if pattern[i] != actual[i] {
			return nil, false
		}
	}
	if params == nil {
		params = routeParams{}
	}
	return params, true
}

// parameterName reports whether a pattern segment is a {name} parameter.
func parameterName(segment string) (string, bool) {
	if len(segment) < 3 || segment[0] != '{' || segment[len(segment)-1] != '}' {
		return "", false
	}
	name := segment[1 : len(segment)-1]
	if name == "" {
		return "", false
	}
	return name, true
}
