package teams

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ncecere/pi-durable-platform/internal/apperr"
	"github.com/ncecere/pi-durable-platform/internal/audit"
	"github.com/ncecere/pi-durable-platform/internal/authz"
	"github.com/ncecere/pi-durable-platform/internal/store"
	"github.com/ncecere/pi-durable-platform/internal/store/dbgen"
)

// Personal workspaces: a one-person team (kind personal) each person gets at
// sign-in while platform admins have them turned on. Its owner is its only
// member: it takes no other members, invites or service accounts, and its
// agents come from catalog templates. Platform admins set the defaults for
// every personal workspace and may override them for one.

// Team kinds.
const (
	KindTeam     = "team"
	KindPersonal = "personal"
)

// ErrPersonal refuses team-only actions in a personal workspace.
var ErrPersonal = apperr.Conflict("personal_workspace", "A personal workspace has only its owner: no members, invites, service accounts or own templates")

// PersonalSettings are the defaults for every personal workspace.
type PersonalSettings = dbgen.PersonalWorkspaceSetting

// Limits are the limits that apply to one workspace (personal workspaces
// only, for now; nil: no limit).
type Limits struct {
	MaxAgents  *int32
	MaxRunning *int32
	// MonthlyBudgetUSD is recorded, not enforced yet.
	MonthlyBudgetUSD *float64
	// Overridden says which come from the workspace's own override.
	Overridden map[string]bool
}

// Dollars reads a stored dollar amount (nil when unset).
func Dollars(n pgtype.Numeric) *float64 { return numeric(n) }

func numeric(n pgtype.Numeric) *float64 {
	if !n.Valid {
		return nil
	}
	f, err := n.Float64Value()
	if err != nil || !f.Valid {
		return nil
	}
	return &f.Float64
}

// ToNumeric converts dollars (two decimals) for storage.
func ToNumeric(f *float64) (pgtype.Numeric, error) {
	if f == nil {
		return pgtype.Numeric{}, nil
	}
	if *f < 0 || *f > 1e9 {
		return pgtype.Numeric{}, apperr.Invalid("invalid_budget", "The budget is zero or more dollars")
	}
	cents := int64(*f*100 + 0.5)
	return pgtype.Numeric{Int: big.NewInt(cents), Exp: -2, Valid: true}, nil
}

// LimitsOf returns a workspace's limits: for a personal workspace its
// overrides, else the platform's defaults; shared teams have none.
func LimitsOf(ctx context.Context, q *dbgen.Queries, t dbgen.Team) (Limits, error) {
	if t.Kind != KindPersonal {
		return Limits{}, nil
	}
	d, err := q.GetPersonalSettings(ctx)
	if err != nil {
		return Limits{}, err
	}
	l := Limits{MaxAgents: &d.MaxAgents, MaxRunning: &d.MaxRunning, MonthlyBudgetUSD: numeric(d.MonthlyBudgetUsd), Overridden: map[string]bool{}}
	if t.LimitMaxAgents != nil {
		l.MaxAgents, l.Overridden["maxAgents"] = t.LimitMaxAgents, true
	}
	if t.LimitMaxRunning != nil {
		l.MaxRunning, l.Overridden["maxRunning"] = t.LimitMaxRunning, true
	}
	if t.LimitMonthlyBudgetUsd.Valid {
		l.MonthlyBudgetUSD, l.Overridden["monthlyBudgetUsd"] = numeric(t.LimitMonthlyBudgetUsd), true
	}
	return l, nil
}

// personalSlug is a personal workspace's slug: stable, from the owner's ID.
func personalSlug(userID uuid.UUID) string {
	return "personal-" + strings.ReplaceAll(userID.String(), "-", "")[:12]
}

// EnsurePersonal gives the person a personal workspace if they have none and
// personal workspaces are on (at sign-in, in its transaction). It returns
// the workspace, or nil when there is none.
func EnsurePersonal(ctx context.Context, q *dbgen.Queries, user dbgen.User, reqID, ip string) (*dbgen.Team, error) {
	t, err := q.GetPersonalTeam(ctx, uuid.NullUUID{UUID: user.ID, Valid: true})
	if err == nil {
		return &t, nil
	} else if !errors.Is(store.NotFound(err), store.ErrNotFound) {
		return nil, err
	}
	settings, err := q.GetPersonalSettings(ctx)
	if err != nil {
		return nil, err
	}
	if !settings.Enabled {
		return nil, nil
	}
	t, err = q.InsertPersonalTeam(ctx, dbgen.InsertPersonalTeamParams{Slug: personalSlug(user.ID), Name: "Personal",
		PersonalOwner: uuid.NullUUID{UUID: user.ID, Valid: true}})
	if errors.Is(err, pgx.ErrNoRows) {
		t, err = q.GetPersonalTeam(ctx, uuid.NullUUID{UUID: user.ID, Valid: true}) // created concurrently
		return &t, err
	} else if err != nil {
		return nil, err
	}
	if _, err := q.InsertMember(ctx, dbgen.InsertMemberParams{TeamID: t.ID, UserID: user.ID, Role: authz.RoleOwner}); err != nil {
		return nil, err
	}
	err = audit.Record(ctx, q, audit.Entry{ActorKind: "user", ActorUserID: user.ID, TeamID: t.ID, Action: "team.personal_create",
		TargetType: "team", TargetID: t.ID.String(), After: map[string]any{"slug": t.Slug, "kind": t.Kind}, RequestID: reqID, ClientIP: ip})
	return &t, err
}

// PersonalSettingsFor returns the defaults (platform staff).
func (s *Service) PersonalSettingsFor(ctx context.Context, a authz.Actor) (PersonalSettings, error) {
	if !a.CanReadPlatform() {
		return PersonalSettings{}, apperr.Forbidden("Only platform admins and auditors can see platform settings")
	}
	return s.q.GetPersonalSettings(ctx)
}

// PersonalSettingsInput changes the defaults.
type PersonalSettingsInput struct {
	Enabled               bool
	MaxAgents, MaxRunning int32
	MonthlyBudgetUSD      *float64
}

// UpdatePersonalSettings changes the defaults (platform admins, If-Match).
// Turning personal workspaces off stops creating them; existing ones stay
// (archive them to make them read-only).
func (s *Service) UpdatePersonalSettings(ctx context.Context, a authz.Actor, in PersonalSettingsInput, expectedRevision int64) (PersonalSettings, error) {
	if !a.IsPlatformAdmin() {
		return PersonalSettings{}, errAdminOnly
	}
	if in.MaxAgents < 0 || in.MaxAgents > 10000 || in.MaxRunning < 0 || in.MaxRunning > 1000 {
		return PersonalSettings{}, apperr.Invalid("invalid_limits", "Max agents is 0-10000 and max running 0-1000")
	}
	budget, err := ToNumeric(in.MonthlyBudgetUSD)
	if err != nil {
		return PersonalSettings{}, err
	}
	var out PersonalSettings
	err = store.InTx(ctx, s.pool, func(q *dbgen.Queries, _ pgx.Tx) error {
		cur, err := q.GetPersonalSettings(ctx)
		if err != nil {
			return err
		}
		if cur.Revision != expectedRevision {
			return apperr.Stale()
		}
		if out, err = q.UpdatePersonalSettings(ctx, dbgen.UpdatePersonalSettingsParams{Enabled: in.Enabled, MaxAgents: in.MaxAgents,
			MaxRunning: in.MaxRunning, MonthlyBudgetUsd: budget}); err != nil {
			return err
		}
		e := a.Audit("platform.personal_settings", "platform", "personal-workspaces")
		e.Before, e.After = settingsSnapshot(cur), settingsSnapshot(out)
		return audit.Record(ctx, q, e)
	})
	return out, err
}

func settingsSnapshot(p PersonalSettings) map[string]any {
	return map[string]any{"enabled": p.Enabled, "maxAgents": p.MaxAgents, "maxRunning": p.MaxRunning, "monthlyBudgetUsd": numeric(p.MonthlyBudgetUsd)}
}

// LimitsInput overrides a personal workspace's limits (nil: the default).
type LimitsInput struct {
	MaxAgents, MaxRunning *int32
	MonthlyBudgetUSD      *float64
}

// SetPersonalLimits overrides one personal workspace's limits (platform
// admins, If-Match on the team's revision).
func (s *Service) SetPersonalLimits(ctx context.Context, a authz.Actor, ref string, in LimitsInput, expectedRevision int64) (Limits, error) {
	if !a.IsPlatformAdmin() {
		return Limits{}, errAdminOnly
	}
	if (in.MaxAgents != nil && (*in.MaxAgents < 0 || *in.MaxAgents > 10000)) || (in.MaxRunning != nil && (*in.MaxRunning < 0 || *in.MaxRunning > 1000)) {
		return Limits{}, apperr.Invalid("invalid_limits", "Max agents is 0-10000 and max running 0-1000")
	}
	budget, err := ToNumeric(in.MonthlyBudgetUSD)
	if err != nil {
		return Limits{}, err
	}
	var out Limits
	err = store.InTx(ctx, s.pool, func(q *dbgen.Queries, _ pgx.Tx) error {
		t, err := resolve(ctx, q, ref)
		if err != nil {
			return err
		}
		if t, err = q.LockTeam(ctx, t.ID); err != nil {
			return err
		}
		if t.Kind != KindPersonal {
			return apperr.Conflict("not_personal", "Limits apply to personal workspaces")
		}
		if t.Revision != expectedRevision {
			return apperr.Stale()
		}
		before, _ := LimitsOf(ctx, q, t)
		updated, err := q.SetPersonalLimits(ctx, dbgen.SetPersonalLimitsParams{ID: t.ID, LimitMaxAgents: in.MaxAgents,
			LimitMaxRunning: in.MaxRunning, LimitMonthlyBudgetUsd: budget})
		if err != nil {
			return err
		}
		if out, err = LimitsOf(ctx, q, updated); err != nil {
			return err
		}
		e := a.Audit("team.personal_limits", "team", t.ID.String())
		e.TeamID, e.Before, e.After = t.ID, limitsSnapshot(before), limitsSnapshot(out)
		return audit.Record(ctx, q, e)
	})
	return out, err
}

func limitsSnapshot(l Limits) map[string]any {
	return map[string]any{"maxAgents": l.MaxAgents, "maxRunning": l.MaxRunning, "monthlyBudgetUsd": l.MonthlyBudgetUSD,
		"overridden": l.Overridden}
}

// TeamLimits returns a workspace's limits, for its members and platform
// staff.
func (s *Service) TeamLimits(ctx context.Context, a authz.Actor, ref string) (dbgen.Team, Limits, error) {
	t, err := resolve(ctx, s.q, ref)
	if err != nil {
		return t, Limits{}, err
	}
	role, err := memberRole(ctx, s.q, t.ID, a.UserID)
	if err != nil {
		return t, Limits{}, err
	}
	if a.Service != nil {
		if a.Service.TeamID != t.ID {
			return t, Limits{}, errTeamNotFound
		}
	} else if role == "" && !a.CanReadPlatform() {
		return t, Limits{}, errTeamNotFound
	}
	l, err := LimitsOf(ctx, s.q, t)
	return t, l, err
}

// LimitError is the refusal when a workspace is at a limit.
func LimitError(what string, limit int32) error {
	return apperr.Conflict("limit_reached", fmt.Sprintf("This workspace may have at most %d %s", limit, what))
}
