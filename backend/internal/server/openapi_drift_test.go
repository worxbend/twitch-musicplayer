package server

import (
	"bufio"
	"bytes"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// infraRoutes are served by the router but deliberately not documented as
// API operations in openapi.yaml.
var infraRoutes = map[string]bool{
	"/ws":           true,
	"/swagger":      true,
	"/openapi.yaml": true,
}

var (
	specPathKey   = regexp.MustCompile(`^  (/\S+):$`)
	specOperation = regexp.MustCompile(`^    (get|post|put|patch|delete|head):$`)
	// Any other key at path depth or shallower ends the current path.
	specOutsidePath = regexp.MustCompile(`^ {0,2}\S`)
)

// TestOpenAPIMatchesRouter fails when a chi route is missing from
// openapi.yaml, or when the spec documents an operation the router lacks.
func TestOpenAPIMatchesRouter(t *testing.T) {
	routes := routerOperations(t)
	spec := specOperations(t, "../api/openapi.yaml")

	if missing := difference(routes, spec); len(missing) > 0 {
		t.Errorf("routes missing from openapi.yaml:\n  %s", strings.Join(missing, "\n  "))
	}
	if missing := difference(spec, routes); len(missing) > 0 {
		t.Errorf("openapi.yaml operations with no route:\n  %s", strings.Join(missing, "\n  "))
	}
}

// routerOperations returns every "METHOD path" pair the router serves.
// Handlers are nil and never invoked; only the route table is walked.
func routerOperations(t *testing.T) map[string]bool {
	t.Helper()
	router, ok := buildRoutes(Options{}).(chi.Routes)
	if !ok {
		t.Fatal("buildRoutes did not return chi.Routes")
	}

	ops := map[string]bool{}
	walk := func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		path := normalizeRoute(route)
		if method == http.MethodOptions || infraRoutes[path] {
			return nil
		}
		ops[method+" "+path] = true
		return nil
	}
	if err := chi.Walk(router, walk); err != nil {
		t.Fatalf("chi.Walk: %v", err)
	}
	return ops
}

// normalizeRoute strips the "/*" or "/" chi leaves on mounted subrouters.
func normalizeRoute(route string) string {
	route = strings.TrimSuffix(route, "/*")
	if len(route) > 1 {
		route = strings.TrimSuffix(route, "/")
	}
	return route
}

// specOperations returns every "METHOD path" pair documented in the spec.
// It reads the fixed indentation of openapi.yaml instead of parsing YAML.
func specOperations(t *testing.T, file string) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read spec: %v", err)
	}

	ops := map[string]bool{}
	path := ""
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	for scanner.Scan() {
		line := scanner.Text()
		if m := specPathKey.FindStringSubmatch(line); m != nil {
			path = m[1]
			continue
		}
		if specOutsidePath.MatchString(line) {
			path = "" // left the paths block, e.g. components:
			continue
		}
		if m := specOperation.FindStringSubmatch(line); m != nil && path != "" {
			ops[strings.ToUpper(m[1])+" "+path] = true
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan spec: %v", err)
	}
	return ops
}

// difference returns the sorted keys of a that are absent from b.
func difference(a, b map[string]bool) []string {
	var out []string
	for k := range a {
		if !b[k] {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}
