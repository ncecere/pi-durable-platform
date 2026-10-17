package audit_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// Every target type the code writes to the audit log has a live-label lookup
// in ListAudit, so the log never shows an existing target as "(deleted)"
// . The target types are found in the source: Actor.Audit
// calls and Entry literals.
func TestListAuditCoversEveryTargetType(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Join(filepath.Dir(file), "..", "..")
	sqlText, err := os.ReadFile(filepath.Join(root, "internal", "store", "queries", "audit.sql"))
	if err != nil {
		t.Fatal(err)
	}
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`\.Audit\(\s*[^,()]+,\s*"([a-z_]+)"`),
		regexp.MustCompile(`TargetType:\s*"([a-z_]+)"`),
	}
	found := map[string]string{}
	for _, dir := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			src, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for _, re := range patterns {
				for _, m := range re.FindAllStringSubmatch(string(src), -1) {
					found[m[1]] = path
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(found) < 3 {
		t.Fatalf("found only %d audited target types: %v", len(found), found)
	}
	for typ, where := range found {
		if !strings.Contains(string(sqlText), "WHEN '"+typ+"' THEN") {
			t.Errorf("target type %q (audited in %s) has no live label in ListAudit", typ, where)
		}
	}
}
