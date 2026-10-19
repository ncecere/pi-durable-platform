package config

import (
	"bufio"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// This file keeps .env.example complete: every setting the platform reads
// has a line (active or commented out) in it, and it names nothing the
// platform doesn't read. The settings come from two places, both found by
// parsing the Go sources (not by matching text):
//
//   - the `settings` table in settings.go (the keys of the setting literals),
//     plus KEY_FILE for the ones marked secret;
//   - every os.Getenv and os.LookupEnv call in the non-test sources of cmd/
//     and internal/ (the argument must be a string literal, or the test
//     fails: it cannot know what such a call reads).

// notDocumented are variables the code reads that are not platform
// settings, each with the reason it needs no line in .env.example.
var notDocumented = map[string]string{
	"PATH":                    "passed through to the local launcher's agent processes; the process environment's own",
	"HOME":                    "passed through to the local launcher's agent processes; the process environment's own",
	"KUBERNETES_SERVICE_HOST": "set by Kubernetes in every pod; the launcher finds the API server with it",
	"KUBERNETES_SERVICE_PORT": "set by Kubernetes in every pod; the launcher finds the API server with it",
	"PDP_TEST_DATABASE_URL":   "tests only (internal/testutil): the Postgres server the integration tests use",
	"PDP_TEST_REQUIRE_DB":     "tests only (internal/testutil): fail instead of skipping without Postgres",
	"PDP_TEST_LOG":            "tests only (internal/testutil): show the server's logs",
	"CI":                      "set by CI systems; internal/testutil treats it like PDP_TEST_REQUIRE_DB",
}

// repoRoot is the repository's root, from this package's directory.
const repoRoot = "../.."

func parseGo(t *testing.T, fset *token.FileSet, path string) *ast.File {
	t.Helper()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return f
}

// tableSettings returns the keys of the settings table and which of them
// also read KEY_FILE.
func tableSettings(t *testing.T) (keys []string, secret map[string]bool) {
	t.Helper()
	f := parseGo(t, token.NewFileSet(), "settings.go")
	secret = map[string]bool{}
	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		vs, ok := n.(*ast.ValueSpec)
		if !ok || len(vs.Names) != 1 || vs.Names[0].Name != "settings" || len(vs.Values) != 1 {
			return true
		}
		table, ok := vs.Values[0].(*ast.CompositeLit)
		if !ok {
			t.Fatal("settings is not a composite literal")
		}
		found = true
		for _, elt := range table.Elts {
			lit, ok := elt.(*ast.CompositeLit)
			if !ok {
				t.Fatalf("settings holds an element that is not a literal at %v", elt.Pos())
			}
			var key string
			isSecret := false
			for _, kv := range lit.Elts {
				kve, ok := kv.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				name, _ := kve.Key.(*ast.Ident)
				if name == nil {
					continue
				}
				switch name.Name {
				case "key":
					bl, ok := kve.Value.(*ast.BasicLit)
					if !ok || bl.Kind != token.STRING {
						t.Fatalf("a setting's key is not a string literal at %v", kve.Pos())
					}
					key, _ = strconv.Unquote(bl.Value)
				case "secret":
					id, _ := kve.Value.(*ast.Ident)
					isSecret = id != nil && id.Name == "true"
				}
			}
			if key == "" {
				t.Fatalf("a setting without a key at %v", lit.Pos())
			}
			keys = append(keys, key)
			secret[key] = isSecret
		}
		return false
	})
	if !found {
		t.Fatal("the settings table was not found in settings.go")
	}
	return keys, secret
}

// envReads returns every variable named by os.Getenv and os.LookupEnv in the
// platform's non-test sources.
func envReads(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	fset := token.NewFileSet()
	for _, dir := range []string{"cmd", "internal"} {
		root := filepath.Join(repoRoot, dir)
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == "node_modules" || d.Name() == "dbgen" || d.Name() == "apitypes" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			f := parseGo(t, fset, path)
			ast.Inspect(f, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				if pkg, ok := sel.X.(*ast.Ident); !ok || pkg.Name != "os" {
					return true
				}
				switch sel.Sel.Name {
				case "Getenv", "LookupEnv":
					bl, ok := call.Args[0].(*ast.BasicLit)
					if !ok || bl.Kind != token.STRING {
						t.Errorf("%s: os.%s with an argument that is not a string literal: this test cannot tell what it reads", fset.Position(call.Pos()), sel.Sel.Name)
						return true
					}
					name, _ := strconv.Unquote(bl.Value)
					out[name] = strings.TrimPrefix(filepath.ToSlash(path), repoRoot+"/")
				case "Environ", "ExpandEnv", "Expand":
					t.Errorf("%s: os.%s reads the environment wholesale: this test cannot tell which variables", fset.Position(call.Pos()), sel.Sel.Name)
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return out
}

var exampleLine = regexp.MustCompile(`^#?\s*([A-Z][A-Z0-9_]*)=`)

// exampleNames returns the names .env.example sets, active or commented out.
func exampleNames(t *testing.T) map[string]bool {
	t.Helper()
	f, err := os.Open(filepath.Join(repoRoot, ".env.example"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	out := map[string]bool{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if m := exampleLine.FindStringSubmatch(sc.Text()); m != nil {
			out[m[1]] = true
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func sorted(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// readSettings is everything the platform reads that belongs in .env.example,
// with where each is read.
func readSettings(t *testing.T) map[string]string {
	t.Helper()
	keys, secret := tableSettings(t)
	reads := map[string]string{}
	for _, k := range keys {
		reads[k] = "internal/config/settings.go"
		if secret[k] {
			reads[k+"_FILE"] = "internal/config/settings.go (the secret's file form)"
		}
	}
	for name, where := range envReads(t) {
		if _, skip := notDocumented[name]; !skip {
			reads[name] = where
		}
	}
	return reads
}

// Every setting the code reads is in .env.example, as an active line or a
// commented one. Add new settings there (with the default and a one-line
// explanation), or to notDocumented with the reason if it isn't a setting.
func TestEnvExampleListsEverySetting(t *testing.T) {
	reads := readSettings(t)
	example := exampleNames(t)

	// A floor for the extraction itself: a parser that finds nothing would
	// pass vacuously.
	keys, _ := tableSettings(t)
	if len(keys) < 50 {
		t.Fatalf("found only %d settings in the settings table; the extractor is broken", len(keys))
	}
	if len(example) < 40 {
		t.Fatalf("found only %d names in .env.example; the line matcher is broken", len(example))
	}
	for _, must := range []string{"APP_URL", "DATABASE_URL", "ENCRYPTION_KEY", "AGENTS_NAMESPACE", "PDP_CONFIG_FILE", "OIDC_CLIENT_SECRET_FILE"} {
		if _, ok := reads[must]; !ok {
			t.Fatalf("the extractor does not find %s; it is broken", must)
		}
	}
	read := envReads(t)
	for name := range notDocumented {
		if _, ok := read[name]; !ok {
			t.Errorf("notDocumented lists %s, which nothing reads any more: remove it", name)
		}
	}

	var missing []string
	for _, name := range sorted(keysOf(reads)) {
		// The *_FILE form of a secret is documented once, in the file's
		// header; only the settings themselves need a line.
		if strings.HasSuffix(name, "_FILE") && name != "PDP_CONFIG_FILE" {
			continue
		}
		if !example[name] {
			missing = append(missing, name+" (read in "+reads[name]+")")
		}
	}
	if len(missing) > 0 {
		t.Errorf(".env.example lacks %d setting(s) the code reads; add each with its default and a one-line explanation:\n  %s",
			len(missing), strings.Join(missing, "\n  "))
	}
}

// .env.example doesn't name a setting the code no longer reads.
func TestEnvExampleNamesOnlyRealSettings(t *testing.T) {
	reads := readSettings(t)
	var stale []string
	for _, name := range sorted(exampleNames(t)) {
		if _, ok := reads[name]; !ok {
			stale = append(stale, name)
		}
	}
	if len(stale) > 0 {
		t.Errorf(".env.example names variable(s) nothing reads: %s", strings.Join(stale, ", "))
	}
}

func keysOf(m map[string]string) map[string]bool {
	out := make(map[string]bool, len(m))
	for k := range m {
		out[k] = true
	}
	return out
}
