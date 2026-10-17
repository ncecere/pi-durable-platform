package httpapi_test

import (
	"sort"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
)

// specOperations lists every operationId in api/openapi.yaml.
func specOperations(t *testing.T) []string {
	t.Helper()
	doc, err := openapi3.NewLoader().LoadFromFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatalf("load spec: %v", err)
	}
	var ops []string
	for path, item := range doc.Paths.Map() {
		for method, op := range item.Operations() {
			if op.OperationID == "" {
				t.Errorf("%s %s has no operationId", strings.ToUpper(method), path)
			}
			ops = append(ops, op.OperationID)
		}
	}
	sort.Strings(ops)
	return ops
}

// TestAuthzMatrixClassifiesEveryOperation fails for an operation in the spec
// that the authorization matrix does not classify, or a classification for
// an operation the spec no longer has. It needs no database, so a new route
// is caught by the unit tests too: classify it in matrixPolicies
// (authz_matrix_policy_test.go).
func TestAuthzMatrixClassifiesEveryOperation(t *testing.T) {
	ops := specOperations(t)
	inSpec := map[string]bool{}
	var missing []string
	for _, op := range ops {
		inSpec[op] = true
		if _, ok := matrixPolicies[op]; !ok {
			missing = append(missing, op)
		}
	}
	if len(missing) > 0 {
		t.Errorf("classify these operations in matrixPolicies:\n  %s", strings.Join(missing, "\n  "))
	}
	for op := range matrixPolicies {
		if !inSpec[op] {
			t.Errorf("matrixPolicies classifies %s, which api/openapi.yaml does not have", op)
		}
	}
}
