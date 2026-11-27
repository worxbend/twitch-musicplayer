package server

import (
	"bufio"
	"bytes"
	"os"
	"regexp"
	"strings"
	"testing"
)

var (
	// readmeRouteRow matches a backend/README.md endpoint row: | `GET` | `/v1/songs` | ...
	readmeRouteRow = regexp.MustCompile("^\\| `([A-Z]+)` \\| `(/[^`]*)` \\|")
	// apiDocRouteLine matches a request line in docs/api.md: GET /v1/songs?q=...
	apiDocRouteLine = regexp.MustCompile(`^(GET|POST|PUT|PATCH|DELETE|HEAD)\s+(/[^\s?]*)`)
	// pathParam matches a {param} segment so names may differ between docs and router.
	pathParam = regexp.MustCompile(`\{[^}]*\}`)
)

// TestDocsListEveryRoute fails when a router route is missing from the
// backend README endpoint tables or from docs/api.md.
func TestDocsListEveryRoute(t *testing.T) {
	routes := normalizeParams(routerOperations(t))
	docs := []struct {
		file    string
		pattern *regexp.Regexp
	}{
		{"../../README.md", readmeRouteRow},
		{"../../../docs/api.md", apiDocRouteLine},
	}
	for _, doc := range docs {
		documented := normalizeParams(docOperations(t, doc.file, doc.pattern))
		if missing := difference(routes, documented); len(missing) > 0 {
			t.Errorf("routes missing from %s:\n  %s", doc.file, strings.Join(missing, "\n  "))
		}
	}
}

// docOperations returns every "METHOD path" pair a doc line matches.
func docOperations(t *testing.T, file string, pattern *regexp.Regexp) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read %s: %v", file, err)
	}

	ops := map[string]bool{}
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	for scanner.Scan() {
		if m := pattern.FindStringSubmatch(scanner.Text()); m != nil {
			ops[m[1]+" "+m[2]] = true
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan %s: %v", file, err)
	}
	return ops
}

// normalizeParams rewrites every {param} to {} so /queue/{id} matches
// /queue/{queue_item_id}.
func normalizeParams(ops map[string]bool) map[string]bool {
	out := make(map[string]bool, len(ops))
	for op := range ops {
		out[pathParam.ReplaceAllString(op, "{}")] = true
	}
	return out
}
