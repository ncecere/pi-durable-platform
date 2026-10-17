package auth

import (
	"slices"
	"strings"
	"testing"
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
	if got := Groups(map[string]any{"groups": many}, "groups"); len(got) > MaxGroups {
		t.Errorf("kept %d groups", len(got))
	}
}
