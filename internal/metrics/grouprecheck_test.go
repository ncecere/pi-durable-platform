package metrics_test

import (
	"testing"

	"github.com/ncecere/pi-durable-platform/internal/metrics"
	"github.com/ncecere/pi-durable-platform/internal/metrics/metricstest"
)

// The group re-check's series exist from the start (a counter that appears
// with its first event loses that event), carry only the bounded labels, and a
// nil Metrics is fine.
func TestGroupRecheckMetrics(t *testing.T) {
	m := newMetrics()
	s := metricstest.Gather(t, m)
	for _, r := range []string{"allowed", "denied", "unknown", "error", "no_token"} {
		if !s.Has("pdp_group_recheck_total", "result", r) {
			t.Errorf("pdp_group_recheck_total{result=%q} is missing at start:\n%s", r, s.Text)
		}
	}
	if !s.Has("pdp_group_recheck_people", "state", "with_token") || !s.Has("pdp_group_recheck_people", "state", "without_token") {
		t.Errorf("pdp_group_recheck_people is missing at start")
	}

	m.GroupRecheck("allowed")
	m.GroupRecheck("no_token")
	m.GroupRecheck("no_token")
	m.GroupRecheck("alice@example.com") // never a label value
	m.SetGroupRecheckPeople(7, 3)
	s = metricstest.Gather(t, m)
	if s.Sum("pdp_group_recheck_total", "result", "allowed") != 1 || s.Sum("pdp_group_recheck_total", "result", "no_token") != 2 ||
		s.Sum("pdp_group_recheck_total", "result", "other") != 1 || s.Series("pdp_group_recheck_total") != 6 {
		t.Errorf("pdp_group_recheck_total:\n%s", s.Text)
	}
	if s.Sum("pdp_group_recheck_people", "state", "with_token") != 7 || s.Sum("pdp_group_recheck_people", "state", "without_token") != 3 {
		t.Errorf("pdp_group_recheck_people:\n%s", s.Text)
	}

	var none *metrics.Metrics
	none.GroupRecheck("allowed")
	none.SetGroupRecheckPeople(1, 1)
}
