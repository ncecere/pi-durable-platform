package metrics_test

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/ncecere/pi-durable-platform/internal/metrics"
)

// The dashboard and the example alerts use only metrics the platform
// exports, with the labels they have: every PromQL expression is checked
// against the registry, so a renamed metric or label breaks this test, not
// the dashboard.

var (
	metricRef   = regexp.MustCompile(`\b((?:pdp|go|process)_[a-z0-9_]+)\b(\{[^}]*\})?`)
	matcherName = regexp.MustCompile(`([a-zA-Z_][a-zA-Z0-9_]*)\s*(?:=~|!~|!=|=)`)
	byClause    = regexp.MustCompile(`\b(?:by|without|on|ignoring)\s*\(([^)]*)\)`)
	legendRef   = regexp.MustCompile(`\{\{\s*([a-zA-Z_][a-zA-Z0-9_]*)\s*\}\}`)
	// Labels Prometheus adds to every series of a scrape (and Kubernetes'
	// service discovery): not ours, but legitimate in a query.
	external = map[string]bool{"pod": true, "namespace": true, "instance": true, "job": true, "container": true, "service": true, "endpoint": true, "le": true}
)

// checkExpr checks one expression: its metrics exist, and the labels it
// matches, groups by or shows in a legend are the metrics'.
func checkExpr(t *testing.T, where, expr, legend string, labels map[string]map[string]bool) {
	t.Helper()
	used := map[string]bool{}
	refs := metricRef.FindAllStringSubmatch(expr, -1)
	if len(refs) == 0 {
		t.Errorf("%s: no metric in %q", where, expr)
	}
	for _, ref := range refs {
		name, selector := ref[1], ref[2]
		set, ok := labels[name]
		if !ok {
			t.Errorf("%s: %s is not a metric the platform exports (%s)", where, name, expr)
			continue
		}
		for l := range set {
			used[l] = true
		}
		for _, m := range matcherName.FindAllStringSubmatch(selector, -1) {
			if !set[m[1]] && !external[m[1]] {
				t.Errorf("%s: %s has no label %q (%s)", where, name, m[1], expr)
			}
		}
	}
	var grouped []string
	for _, m := range byClause.FindAllStringSubmatch(expr, -1) {
		for _, l := range strings.Split(m[1], ",") {
			grouped = append(grouped, strings.TrimSpace(l))
		}
	}
	for _, m := range legendRef.FindAllStringSubmatch(legend, -1) {
		grouped = append(grouped, m[1])
	}
	for _, l := range grouped {
		if l != "" && !used[l] && !external[l] {
			t.Errorf("%s: %q is not a label of the metrics in %q", where, l, expr)
		}
	}
}

func exportedLabels(t *testing.T) map[string]map[string]bool {
	m := newMetrics()
	exerciseAll(t, m)
	return labelNames(t, m)
}

func TestDashboardUsesExportedMetrics(t *testing.T) {
	raw, err := os.ReadFile("../../deploy/grafana/pi-durable-platform.json")
	if err != nil {
		t.Fatal(err)
	}
	var d struct {
		UID    string `json:"uid"`
		Panels []struct {
			Title   string `json:"title"`
			Type    string `json:"type"`
			Targets []struct {
				Expr   string `json:"expr"`
				Legend string `json:"legendFormat"`
			} `json:"targets"`
		} `json:"panels"`
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatalf("the dashboard is not JSON: %v", err)
	}
	if d.UID == "" || len(d.Panels) < 30 {
		t.Fatalf("dashboard uid %q with %d panels", d.UID, len(d.Panels))
	}
	labels := exportedLabels(t)
	exprs := 0
	titles := map[string]bool{}
	for _, p := range d.Panels {
		titles[p.Title] = true
		for _, tg := range p.Targets {
			exprs++
			checkExpr(t, "panel "+p.Title, tg.Expr, tg.Legend, labels)
		}
	}
	for _, want := range []string{"Agents running", "Wake latency (cold, warm and pool-miss starts)", "Wake latency p95: warm vs cold", "Model proxy latency (whole reply)",
		"MCP errors (non-2xx) ratio by server", "Sign-ins per second", "Loop runs per second", "Connections in use / max", "Build"} {
		if !titles[want] {
			t.Errorf("the dashboard has no panel %q", want)
		}
	}
	if exprs < 40 {
		t.Errorf("only %d expressions checked", exprs)
	}
}

func TestExampleAlertsUseExportedMetrics(t *testing.T) {
	raw, err := os.ReadFile("../../deploy/grafana/alerts.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var rule struct {
		Kind string `yaml:"kind"`
		Spec struct {
			Groups []struct {
				Rules []struct {
					Alert string `yaml:"alert"`
					Expr  string `yaml:"expr"`
				} `yaml:"rules"`
			} `yaml:"groups"`
		} `yaml:"spec"`
	}
	if err := yaml.Unmarshal(raw, &rule); err != nil {
		t.Fatal(err)
	}
	if rule.Kind != "PrometheusRule" || len(rule.Spec.Groups) == 0 {
		t.Fatalf("not a PrometheusRule: %+v", rule)
	}
	labels := exportedLabels(t)
	n := 0
	for _, g := range rule.Spec.Groups {
		for _, r := range g.Rules {
			n++
			checkExpr(t, "alert "+r.Alert, r.Expr, "", labels)
		}
	}
	if n < 8 {
		t.Errorf("only %d alerts", n)
	}
	_ = metrics.ListenerAPI
}
