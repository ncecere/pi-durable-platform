package llm

import (
	"context"
	"sort"
	"time"

	"github.com/google/uuid"

	"github.com/ncecere/pi-durable-platform/internal/apperr"
	"github.com/ncecere/pi-durable-platform/internal/authz"
	"github.com/ncecere/pi-durable-platform/internal/store/dbgen"
	"github.com/ncecere/pi-durable-platform/internal/teams"
)

// Usage reports: tokens and requests per day, model, agent (or agent and
// day) or team, with a cost estimate from the models' current prices. Rows come from the model
// proxy (every request of a proxy-mode provider) and from agents (batches,
// for direct-mode providers and the platform's default model).

// Report groupings.
const (
	GroupDay   = "day"
	GroupModel = "model"
	GroupAgent = "agent"
	GroupTeam  = "team"
	// GroupAgentDay is one row per agent and day.
	GroupAgentDay = "agent,day"
)

// MaxReportDays bounds a report's range.
const MaxReportDays = 366

// MaxReportAgents bounds a report's agent filter.
const MaxReportAgents = 100

var (
	errBadRange   = apperr.Invalid("invalid_range", "from and to are dates (YYYY-MM-DD), from not after to, at most 366 days apart")
	errBadGroupBy = apperr.Invalid("invalid_group_by", "groupBy is day, model, agent or agent,day for a team (day, team or model for the platform)")
	// ErrTooManyAgents: the agent filter names more than MaxReportAgents agents.
	ErrTooManyAgents = apperr.Invalid("too_many_agents", "agentId names at most 100 agents")
)

// UsageQuery is a report request: the days from From to To (UTC, inclusive),
// and, when AgentIDs is not empty, those agents only (at most
// MaxReportAgents; duplicates count once).
type UsageQuery struct {
	From, To time.Time
	GroupBy  string
	AgentIDs []uuid.UUID
}

// UsageRow is one group's totals. Key fields are set by the grouping: Day;
// ModelID/ModelName/ProviderName (no model: the platform's default model);
// AgentID/AgentName (Managed: service-managed agents, summed for callers who
// may not see them); TeamID/TeamSlug/TeamName.
type UsageRow struct {
	Day                     *time.Time
	ModelID                 *uuid.UUID
	ModelName, ProviderName *string
	AgentID                 *uuid.UUID
	AgentName               *string
	Managed                 bool
	TeamID                  *uuid.UUID
	TeamSlug, TeamName      *string
	Requests, Input, Output int64
	CacheRead, CacheWrite   int64
	CostUSD                 float64
	// UnpricedRequests counts requests of models without prices (not in CostUSD).
	UnpricedRequests int64
}

// UsageReport is a report and its total.
type UsageReport struct {
	From, To time.Time
	GroupBy  string
	Rows     []UsageRow
	Total    UsageRow
}

func (q *UsageQuery) normalize(allowed ...string) error {
	today := time.Now().UTC().Truncate(24 * time.Hour)
	if q.To.IsZero() {
		q.To = today
	}
	if q.From.IsZero() {
		q.From = q.To.AddDate(0, 0, -29)
	}
	q.From, q.To = q.From.UTC().Truncate(24*time.Hour), q.To.UTC().Truncate(24*time.Hour)
	if q.From.After(q.To) || q.To.Sub(q.From) > (MaxReportDays-1)*24*time.Hour {
		return errBadRange
	}
	if len(q.AgentIDs) > MaxReportAgents {
		return ErrTooManyAgents
	}
	if q.GroupBy == "" {
		q.GroupBy = GroupDay
	}
	for _, g := range allowed {
		if g == q.GroupBy {
			return nil
		}
	}
	return errBadGroupBy
}

// TeamUsage reports a team's usage (its members and service accounts, and
// platform staff). Service-managed agents are named only for the team's
// owners and admins, service accounts and platform staff; others see them
// summed. An agent filter counts only the team's own usage (an ID of another
// team's agent matches nothing), and, for callers who see service-managed
// agents summed, never a service-managed agent.
func (s *Service) TeamUsage(ctx context.Context, a authz.Actor, teamRef string, q UsageQuery) (UsageReport, error) {
	if err := q.normalize(GroupDay, GroupModel, GroupAgent, GroupAgentDay); err != nil {
		return UsageReport{}, err
	}
	t, err := teams.Resolve(ctx, s.q, teamRef)
	if err != nil {
		return UsageReport{}, err
	}
	seeManaged := a.CanReadPlatform()
	allowed := seeManaged
	if a.Service != nil {
		allowed, seeManaged = a.Service.TeamID == t.ID, a.Service.TeamID == t.ID
	} else if !allowed {
		role, err := teams.MemberRole(ctx, s.q, t.ID, a.UserID)
		if err != nil {
			return UsageReport{}, err
		}
		allowed, seeManaged = role != "", authz.RoleAtLeast(role, authz.RoleAdmin)
	}
	if !allowed {
		return UsageReport{}, errTeamNotFound
	}
	return s.report(ctx, q, uuid.NullUUID{UUID: t.ID, Valid: true}, seeManaged)
}

// PlatformUsage reports every team's usage (platform staff).
func (s *Service) PlatformUsage(ctx context.Context, a authz.Actor, q UsageQuery) (UsageReport, error) {
	if !a.CanReadPlatform() {
		return UsageReport{}, apperr.Forbidden("Only platform admins and auditors can see the platform's usage")
	}
	q.AgentIDs = nil
	if err := q.normalize(GroupDay, GroupTeam, GroupModel); err != nil {
		return UsageReport{}, err
	}
	return s.report(ctx, q, uuid.NullUUID{}, true)
}

func (s *Service) report(ctx context.Context, q UsageQuery, team uuid.NullUUID, seeManaged bool) (UsageReport, error) {
	var agents []uuid.UUID // nil: every agent
	if len(q.AgentIDs) > 0 {
		agents = q.AgentIDs
	}
	facts, err := s.q.UsageFacts(ctx, dbgen.UsageFactsParams{Since: q.From, Until: q.To.AddDate(0, 0, 1), TeamID: team,
		AgentIds: agents, WithManaged: seeManaged})
	if err != nil {
		return UsageReport{}, err
	}
	out := UsageReport{From: q.From, To: q.To, GroupBy: q.GroupBy, Rows: []UsageRow{}}
	groups := map[string]*UsageRow{}
	var order []string
	for _, f := range facts {
		key, row := groupKey(q.GroupBy, f, seeManaged)
		g, ok := groups[key]
		if !ok {
			g = &row
			groups[key] = g
			order = append(order, key)
		}
		for _, dst := range []*UsageRow{g, &out.Total} {
			addFact(dst, f)
		}
	}
	for _, k := range order {
		out.Rows = append(out.Rows, *groups[k])
	}
	switch q.GroupBy {
	case GroupDay:
		sort.Slice(out.Rows, func(i, j int) bool { return out.Rows[i].Day.Before(*out.Rows[j].Day) })
	case GroupAgentDay:
		sort.Slice(out.Rows, func(i, j int) bool { return agentDayLess(out.Rows[i], out.Rows[j]) })
	default:
		sort.SliceStable(out.Rows, func(i, j int) bool {
			if out.Rows[i].CostUSD != out.Rows[j].CostUSD {
				return out.Rows[i].CostUSD > out.Rows[j].CostUSD
			}
			return out.Rows[i].Input+out.Rows[i].Output > out.Rows[j].Input+out.Rows[j].Output
		})
	}
	return out, nil
}

// agentDayLess orders rows by agent (name, then ID; summed service-managed
// agents, then deleted agents, last), then by day.
func agentDayLess(a, b UsageRow) bool {
	ra, rb := agentRank(a), agentRank(b)
	if ra != rb {
		return ra < rb
	}
	if ra == 0 {
		na, nb := deref(a.AgentName), deref(b.AgentName)
		if na != nb {
			return na < nb
		}
		if *a.AgentID != *b.AgentID {
			return a.AgentID.String() < b.AgentID.String()
		}
	}
	return a.Day.Before(*b.Day)
}

// agentRank: 0 a named agent, 1 service-managed agents summed, 2 deleted
// agents.
func agentRank(r UsageRow) int {
	switch {
	case r.AgentID != nil:
		return 0
	case r.Managed:
		return 1
	}
	return 2
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func addFact(dst *UsageRow, f dbgen.UsageFactsRow) {
	dst.Requests += f.Requests
	dst.Input += f.InputTokens
	dst.Output += f.OutputTokens
	dst.CacheRead += f.CacheReadTokens
	dst.CacheWrite += f.CacheWriteTokens
	if !f.InputPrice.Valid && !f.OutputPrice.Valid {
		dst.UnpricedRequests += f.Requests
		return
	}
	if f.InputPrice.Valid {
		dst.CostUSD += float64(f.InputTokens) * f.InputPrice.Float64 / 1e6
	}
	if f.OutputPrice.Valid {
		dst.CostUSD += float64(f.OutputTokens) * f.OutputPrice.Float64 / 1e6
	}
}

func groupKey(groupBy string, f dbgen.UsageFactsRow, seeManaged bool) (string, UsageRow) {
	switch groupBy {
	case GroupModel:
		if !f.ModelID.Valid {
			return "default", UsageRow{}
		}
		id := f.ModelID.UUID
		return id.String(), UsageRow{ModelID: &id, ModelName: f.ModelName, ProviderName: f.ProviderName}
	case GroupAgent:
		return agentKey(f, seeManaged)
	case GroupAgentDay:
		key, row := agentKey(f, seeManaged)
		day := f.Day.Time.UTC()
		row.Day = &day
		return key + "/" + day.Format(time.DateOnly), row
	case GroupTeam:
		id, slug, name := f.TeamID, f.TeamSlug, f.TeamName
		return id.String(), UsageRow{TeamID: &id, TeamSlug: &slug, TeamName: &name}
	default:
		day := f.Day.Time.UTC()
		return day.Format(time.DateOnly), UsageRow{Day: &day}
	}
}

// agentKey is a fact's agent group: the agent, service-managed agents summed
// (for callers who may not see them), or deleted agents.
func agentKey(f dbgen.UsageFactsRow, seeManaged bool) (string, UsageRow) {
	if f.AgentManaged && !seeManaged {
		return "managed", UsageRow{Managed: true}
	}
	if !f.AgentID.Valid {
		return "deleted", UsageRow{}
	}
	id := f.AgentID.UUID
	return id.String(), UsageRow{AgentID: &id, AgentName: f.AgentName, Managed: f.AgentManaged}
}

// ReportedUsage is a batch an agent reports for models the proxy does not
// see (direct-mode providers, the platform's default model).
type ReportedUsage struct {
	Requests, Input, Output, CacheRead, CacheWrite int64
}

const maxReported = 100_000_000

func (u ReportedUsage) valid() bool {
	for _, v := range []int64{u.Requests, u.Input, u.Output, u.CacheRead, u.CacheWrite} {
		if v < 0 || v > maxReported {
			return false
		}
	}
	return u.Requests > 0 && u.Requests <= 10_000
}

// RecordReported stores an agent's batch. It is ignored (false) when the
// agent's model is proxied: the proxy counts those requests itself.
func (s *Service) RecordReported(ctx context.Context, teamID, agentID, modelID uuid.UUID, u ReportedUsage) (bool, error) {
	if !u.valid() {
		return false, apperr.Invalid("invalid_usage", "Counts must be zero or more, with 1-10000 requests")
	}
	if modelID != uuid.Nil {
		r, err := s.q.GetModelProvider(ctx, s.providerOf(ctx, modelID))
		if err == nil && r.AccessMode == ModeProxy {
			return false, nil
		}
	}
	err := s.q.InsertModelUsageBatch(ctx, dbgen.InsertModelUsageBatchParams{TeamID: teamID,
		AgentID: uuid.NullUUID{UUID: agentID, Valid: agentID != uuid.Nil}, ModelID: uuid.NullUUID{UUID: modelID, Valid: modelID != uuid.Nil},
		Source: "agent", Requests: int32(u.Requests), InputTokens: u.Input, OutputTokens: u.Output, CacheReadTokens: u.CacheRead,
		CacheWriteTokens: u.CacheWrite})
	return err == nil, err
}

func (s *Service) providerOf(ctx context.Context, modelID uuid.UUID) uuid.UUID {
	m, err := s.q.GetModel(ctx, modelID)
	if err != nil {
		return uuid.Nil
	}
	return m.ProviderID
}
