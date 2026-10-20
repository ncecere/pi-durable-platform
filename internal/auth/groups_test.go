package auth

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/ncecere/pi-durable-platform/internal/config"
)

func TestGroups(t *testing.T) {
	claims := map[string]any{
		"groups": []any{"b", "a", " a ", "", 3, strings.Repeat("x", 257), "bad\nline"},
		"single": "solo",
	}
	if got := Groups(claims, "groups"); !slices.Equal(got, []string{"a", "b"}) {
		t.Errorf("groups = %v", got)
	}
	if got := Groups(claims, "single"); !slices.Equal(got, []string{"solo"}) {
		t.Errorf("single = %v", got)
	}
	if got := Groups(claims, ""); got == nil || len(got) != 0 {
		t.Errorf("no claim = %#v", got)
	}
	many := []any{}
	for i := 0; i < 500; i++ {
		many = append(many, strings.Repeat("g", 1+i%200)+string(rune('a'+i%26)))
	}
	// Groups keeps them all; what is stored is bounded.
	got := Groups(map[string]any{"groups": many}, "groups")
	if len(got) <= MaxGroups {
		t.Errorf("Groups dropped groups: %d", len(got))
	}
	if b := boundGroups(got); len(b) != MaxGroups {
		t.Errorf("stored %d groups, want %d", len(b), MaxGroups)
	}
}

// OIDC_ALLOWED_GROUPS is matched against every group the IdP lists, not the
// first MaxGroups of them (sorted: the allowed group may come last).
func TestGroupRuleSeesGroupsBeyondTheStoredBound(t *testing.T) {
	var raw []any
	for i := 0; i < 300; i++ {
		raw = append(raw, fmt.Sprintf("team-%03d", i))
	}
	raw = append(raw, "zz-allowed")
	groups := Groups(map[string]any{"groups": raw}, "groups")
	if len(groups) != 301 || groups[300] != "zz-allowed" {
		t.Fatalf("groups: %d, last %q", len(groups), groups[len(groups)-1])
	}
	s := &Service{cfg: config.Config{OIDC: config.OIDC{AllowedGroups: []string{"zz-allowed"}}}}
	if !s.groupAllowed(groups) {
		t.Fatal("a person in an allowed group beyond the first 200 is refused")
	}
	if s.groupAllowed(boundGroups(groups)) {
		t.Fatal("the stored bound should not contain the group: this test no longer shows the old bug")
	}
	if s.groupAllowed(Groups(map[string]any{"groups": raw[:300]}, "groups")) {
		t.Fatal("a person in no allowed group is allowed")
	}
}
