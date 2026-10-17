package teams

import (
	"net/http"
	"testing"

	"github.com/ncecere/pi-durable-platform/internal/apperr"
	"github.com/ncecere/pi-durable-platform/internal/store/dbgen"
)

func TestValidateSlug(t *testing.T) {
	for _, ok := range []string{"a", "platform-tools", "team1", "x-1-y"} {
		if err := ValidateSlug(ok); err != nil {
			t.Errorf("%q rejected: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "-x", "x-", "Bad", "a_b", "admin", "mcp", "123e4567-e89b-12d3-a456-426614174000",
		"a.b", string(make([]byte, 64))} {
		err := ValidateSlug(bad)
		if e, ok := apperr.As(err); !ok || e.Code != "invalid_slug" || e.Status != http.StatusBadRequest {
			t.Errorf("%q: %v", bad, err)
		}
	}
}

func TestNormalizeEmail(t *testing.T) {
	if got, err := NormalizeEmail("  Alex@Example.COM "); err != nil || got != "alex@example.com" {
		t.Errorf("got %q %v", got, err)
	}
	for _, bad := range []string{"", "nobody", "Alex <alex@example.com>", "a@b@c"} {
		if _, err := NormalizeEmail(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestApplyUpdate(t *testing.T) {
	team := dbgen.Team{Name: "Tools", Description: "d", Status: StatusActive}
	name, status, bad := " New ", StatusArchived, "deleted"
	p, err := applyUpdate(team, UpdateInput{Name: &name, Status: &status})
	if err != nil || p.Name != "New" || p.Status != StatusArchived || p.Description != "d" {
		t.Fatalf("p = %+v %v", p, err)
	}
	if _, err := applyUpdate(team, UpdateInput{Status: &bad}); err == nil {
		t.Error("invalid status accepted")
	}
	empty := "  "
	if _, err := applyUpdate(team, UpdateInput{Name: &empty}); err == nil {
		t.Error("empty name accepted")
	}
	if teamAction(team, dbgen.Team{Status: StatusArchived}) != "team.archive" ||
		teamAction(dbgen.Team{Status: StatusArchived}, team) != "team.unarchive" || teamAction(team, team) != "team.update" {
		t.Error("team actions")
	}
}
