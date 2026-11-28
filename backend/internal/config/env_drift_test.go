package config

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

var (
	// envExampleKey matches KEY= at line start; a leading # still counts, so
	// a commented-out key is documented.
	envExampleKey = regexp.MustCompile(`(?m)^#?\s*([A-Z][A-Z0-9_]*)=`)
	// composeVar matches ${VAR, including one nested in another's default.
	composeVar = regexp.MustCompile(`\$\{([A-Z_][A-Z0-9_]*)`)
	// backendEnvBlock captures the bash block under the backend env heading.
	backendEnvBlock = regexp.MustCompile("(?s)## 🌱 Backend Environment.*?```bash\n(.*?)```")
)

// TestBackendEnvExampleMatchesLoader fails when backend/.env.example and the
// env keys config.go reads drift apart in either direction.
func TestBackendEnvExampleMatchesLoader(t *testing.T) {
	read := loaderEnvKeys(t)
	if len(read) < 6 {
		t.Fatalf("found %d env keys in config.go, want at least 6: %v", len(read), read)
	}
	example := envExampleKey.FindAllStringSubmatch(readFile(t, "../../.env.example"), -1)
	reportDrift(t, "config.go", read, "backend/.env.example", submatches(example))
}

// TestConfigDocMirrorsBackendEnvExample fails when the backend env block in
// docs/configuration.md lists different keys than backend/.env.example.
func TestConfigDocMirrorsBackendEnvExample(t *testing.T) {
	block := backendEnvBlock.FindStringSubmatch(readFile(t, "../../../docs/configuration.md"))
	if block == nil {
		t.Fatal("docs/configuration.md: no bash block under '## 🌱 Backend Environment'")
	}
	example := envExampleKey.FindAllStringSubmatch(readFile(t, "../../.env.example"), -1)
	reportDrift(t, "backend/.env.example", submatches(example),
		"docs/configuration.md", submatches(envExampleKey.FindAllStringSubmatch(block[1], -1)))
}

// TestRootEnvExampleMatchesCompose fails when a compose file uses a ${VAR}
// the root .env.example omits, or .env.example lists one no compose file uses.
func TestRootEnvExampleMatchesCompose(t *testing.T) {
	files, err := filepath.Glob("../../../docker-compose*.yml")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) < 3 {
		t.Fatalf("found %d compose files, want at least 3: %v", len(files), files)
	}
	var used [][]string
	for _, f := range files {
		used = append(used, composeVar.FindAllStringSubmatch(readFile(t, f), -1)...)
	}
	example := envExampleKey.FindAllStringSubmatch(readFile(t, "../../../.env.example"), -1)
	reportDrift(t, "docker-compose*.yml", submatches(used), ".env.example", submatches(example))
}

// loaderEnvKeys returns the string literal passed to every getenv or
// os.Getenv call in config.go.
func loaderEnvKeys(t *testing.T) map[string]bool {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "config.go", nil, 0)
	if err != nil {
		t.Fatalf("parse config.go: %v", err)
	}
	keys := map[string]bool{}
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || !isGetenv(call.Fun) || len(call.Args) == 0 {
			return true
		}
		lit, ok := call.Args[0].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			t.Errorf("config.go: env key at %v is not a string literal", call.Pos())
			return true
		}
		key, err := strconv.Unquote(lit.Value)
		if err != nil {
			t.Fatalf("unquote %s: %v", lit.Value, err)
		}
		keys[key] = true
		return true
	})
	return keys
}

// isGetenv reports whether fun is the identifier getenv or os.Getenv.
func isGetenv(fun ast.Expr) bool {
	switch f := fun.(type) {
	case *ast.Ident:
		return f.Name == "getenv"
	case *ast.SelectorExpr:
		pkg, ok := f.X.(*ast.Ident)
		return ok && pkg.Name == "os" && f.Sel.Name == "Getenv"
	}
	return false
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(raw)
}

// submatches collects the first capture group of each match as a set.
func submatches(matches [][]string) map[string]bool {
	set := make(map[string]bool, len(matches))
	for _, m := range matches {
		set[m[1]] = true
	}
	return set
}

// reportDrift names every key present in one set but not the other.
func reportDrift(t *testing.T, aName string, a map[string]bool, bName string, b map[string]bool) {
	t.Helper()
	if missing := missingFrom(a, b); len(missing) > 0 {
		t.Errorf("in %s but not %s: %s", aName, bName, strings.Join(missing, ", "))
	}
	if missing := missingFrom(b, a); len(missing) > 0 {
		t.Errorf("in %s but not %s: %s", bName, aName, strings.Join(missing, ", "))
	}
}

// missingFrom returns the sorted keys of want that got lacks.
func missingFrom(want, got map[string]bool) []string {
	var out []string
	for k := range want {
		if !got[k] {
			out = append(out, k)
		}
	}
	slices.Sort(out)
	return out
}
