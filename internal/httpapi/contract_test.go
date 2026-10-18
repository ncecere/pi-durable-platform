package httpapi

import (
	"context"
	"sort"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/ncecere/pi-durable-platform/internal/auth"
	"github.com/ncecere/pi-durable-platform/internal/config"
	"github.com/ncecere/pi-durable-platform/internal/testutil"
)

// TestRoutesMatchOpenAPI fails when a served route is missing from
// api/openapi.yaml or the spec documents a route the server does not serve.
func TestRoutesMatchOpenAPI(t *testing.T) {
	loader := openapi3.NewLoader()
	doc, err := loader.LoadFromFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatalf("load spec: %v", err)
	}
	if err := doc.Validate(context.Background()); err != nil {
		t.Fatalf("invalid spec: %v", err)
	}
	spec := map[string]bool{}
	for path, item := range doc.Paths.Map() {
		for method := range item.Operations() {
			spec[strings.ToUpper(method)+" "+path] = true
		}
	}

	deps := Deps{
		Config: config.Defaults(),
		Health: NewHealth(nil),
		Log:    testutil.Logger(),
		Auth:   &auth.Service{},
	}
	served := map[string]bool{}
	seen := map[string]bool{}
	for _, r := range apiRoutes(deps) {
		if seen[r.pattern()] {
			t.Errorf("route registered twice: %s", r.pattern())
		}
		seen[r.pattern()] = true
		// OAuth Connect's protocol endpoints speak OAuth, not the /v1
		// envelope; they are documented in DESIGN.md instead.
		if !protocolRoutes[r.pattern()] {
			served[r.pattern()] = true
		}
	}
	for p := range protocolRoutes {
		if !seen[p] {
			t.Errorf("protocolRoutes lists %s, which is not served", p)
		}
	}

	var missing, extra []string
	for p := range served {
		if !spec[p] {
			missing = append(missing, p)
		}
	}
	for p := range spec {
		if !served[p] {
			extra = append(extra, p)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	if len(missing) > 0 {
		t.Errorf("served but not documented in api/openapi.yaml:\n  %s", strings.Join(missing, "\n  "))
	}
	if len(extra) > 0 {
		t.Errorf("documented in api/openapi.yaml but not served:\n  %s", strings.Join(extra, "\n  "))
	}
}
