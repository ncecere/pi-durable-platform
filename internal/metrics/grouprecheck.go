package metrics

import "github.com/prometheus/client_golang/prometheus"

// The group re-check's metrics (internal/auth/grouprecheck.go). Both have
// bounded label values and say nothing about any person.

// Results of the group re-check (the `result` label of
// pdp_group_recheck_total).
const (
	RecheckAllowed = "allowed"  // the IdP lists an allowed group: access continues
	RecheckDenied  = "denied"   // access ended: not in an allowed group, the IdP refused the grant, or the grace period passed
	RecheckUnknown = "unknown"  // the IdP couldn't say: access kept for now (the grace period counts)
	RecheckError   = "error"    // the check couldn't be completed here: the stored token can't be opened, or the answer couldn't be recorded
	RecheckNoToken = "no_token" // not a check: a sign-in under the group rule brought no refresh token, so this person can't be re-checked
)

var recheckResults = []string{RecheckAllowed, RecheckDenied, RecheckUnknown, RecheckError, RecheckNoToken}

// groupRecheck holds the group re-check's collectors.
type groupRecheck struct {
	results *prometheus.CounterVec
	people  *prometheus.GaugeVec
}

func newGroupRecheck(reg *prometheus.Registry) *groupRecheck {
	g := &groupRecheck{
		results: counterVec("group_recheck_total", "Group re-check outcomes by result: allowed, denied, unknown (the IdP couldn't say, access kept), error (the stored token can't be opened, or the answer couldn't be recorded), and no_token (a sign-in under OIDC_ALLOWED_GROUPS brought no refresh token: that person is never re-checked).", "result"),
		people:  gaugeVec("group_recheck_people", "Recently active people the group re-check applies to, by state: with_token (a refresh token is kept, so they are re-checked) or without_token (none: they keep access after leaving the allowed group until they sign in again). The same on every replica: use max, not sum.", "state"),
	}
	for _, r := range recheckResults {
		g.results.WithLabelValues(r)
	}
	g.people.WithLabelValues("with_token")
	g.people.WithLabelValues("without_token")
	reg.MustRegister(g.results, g.people)
	return g
}

// GroupRecheck counts one group re-check outcome (one of the Recheck*
// results; anything else is "other").
func (m *Metrics) GroupRecheck(result string) {
	if m == nil {
		return
	}
	m.recheck.results.WithLabelValues(oneOf(result, recheckResults...)).Inc()
}

// SetGroupRecheckPeople sets how many recently active people have a refresh
// token kept for the group re-check, and how many don't.
func (m *Metrics) SetGroupRecheckPeople(withToken, withoutToken int64) {
	if m == nil {
		return
	}
	m.recheck.people.WithLabelValues("with_token").Set(float64(withToken))
	m.recheck.people.WithLabelValues("without_token").Set(float64(withoutToken))
}
