package httpapi

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ncecere/pi-durable-platform/internal/httpapi/apitypes"
	"github.com/ncecere/pi-durable-platform/internal/httpx"
	"github.com/ncecere/pi-durable-platform/internal/store"
	"github.com/ncecere/pi-durable-platform/internal/store/dbgen"
)

// Audit log reads: the team and platform logs share these filters and the
// entry format, with the actor and target names resolved at read time.

var (
	auditActionRe     = regexp.MustCompile(`^[a-z][a-z_]*\.[a-z_]*$`)
	auditExcludeRe    = regexp.MustCompile(`^[a-z][a-z_]*\.(,[a-z][a-z_]*\.)*$`)
	auditTargetTypeRe = regexp.MustCompile(`^[a-z_]{1,64}$`)
)

// auditFilters reads ?action=, ?excludeAction=, ?actorUserId=, ?actorKind=,
// ?targetType=, ?from= and ?to= into p. An action ending in "." matches every
// action in that group; excludeAction is one or more groups, comma-separated
// ("auth." hides sign-ins).
func auditFilters(w http.ResponseWriter, r *http.Request, p *dbgen.ListAuditParams) bool {
	q := r.URL.Query()
	if v := q.Get("action"); v != "" {
		if len(v) > 100 || !auditActionRe.MatchString(v) {
			httpx.Error(w, http.StatusBadRequest, "invalid_action", "action must be an action such as team.member_add, or a group prefix such as team.")
			return false
		}
		if strings.HasSuffix(v, ".") {
			prefix := store.EscapeLike(v)
			p.ActionPrefix = &prefix
		} else {
			p.Action = &v
		}
	}
	if v := q.Get("excludeAction"); v != "" {
		if len(v) > 100 || !auditExcludeRe.MatchString(v) {
			httpx.Error(w, http.StatusBadRequest, "invalid_action", "excludeAction must be action groups such as auth. or auth.,team.")
			return false
		}
		for _, group := range strings.Split(v, ",") {
			p.ExcludePrefixes = append(p.ExcludePrefixes, store.EscapeLike(group))
		}
	}
	if v := q.Get("actorUserId"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, "invalid_actor", "actorUserId must be a user ID")
			return false
		}
		p.ActorUserID = uuid.NullUUID{UUID: id, Valid: true}
	}
	switch v := q.Get("actorKind"); v {
	case "":
	case "system":
		p.ActorKind = &v
	default:
		httpx.Error(w, http.StatusBadRequest, "invalid_actor_kind", "actorKind must be system")
		return false
	}
	if v := q.Get("targetType"); v != "" {
		if !auditTargetTypeRe.MatchString(v) {
			httpx.Error(w, http.StatusBadRequest, "invalid_target_type", "targetType must be a target type such as team")
			return false
		}
		p.TargetType = &v
	}
	return auditRange(w, r, p)
}

// auditRange reads the time window: from is inclusive, to exclusive.
func auditRange(w http.ResponseWriter, r *http.Request, p *dbgen.ListAuditParams) bool {
	for _, f := range []struct {
		name string
		dst  **time.Time
	}{{"from", &p.OccurredFrom}, {"to", &p.OccurredTo}} {
		raw := r.URL.Query().Get(f.name)
		if raw == "" {
			continue
		}
		t, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, "invalid_range", f.name+" must be an RFC 3339 date-time")
			return false
		}
		*f.dst = &t
	}
	if p.OccurredFrom != nil && p.OccurredTo != nil && !p.OccurredFrom.Before(*p.OccurredTo) {
		httpx.Error(w, http.StatusBadRequest, "invalid_range", "from must be before to")
		return false
	}
	return true
}

// writeAuditPage serves one page of audit entries, newest first, within
// scope (a team, or the whole platform).
func (a *api) writeAuditPage(w http.ResponseWriter, r *http.Request, scope dbgen.ListAuditParams) {
	limit, ok := pageLimit(w, r)
	if !ok {
		return
	}
	keys, ok := decodeCursor(w, r, 1)
	if !ok {
		return
	}
	p := dbgen.ListAuditParams{TeamID: scope.TeamID, PageSize: limit + 1, ExcludePrefixes: []string{}}
	if keys != nil {
		before, err := strconv.ParseInt(keys[0], 10, 64)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, "invalid_cursor", "Invalid cursor")
			return
		}
		p.BeforeID = &before
	}
	if !auditFilters(w, r, &p) {
		return
	}
	rows, err := a.q.ListAudit(r.Context(), p)
	if err != nil {
		httpx.Internal(w, r, err)
		return
	}
	page := apitypes.AuditPage{Items: []apitypes.AuditEntry{}}
	if len(rows) > int(limit) {
		rows = rows[:limit]
		page.NextCursor = encodeCursor(strconv.FormatInt(rows[len(rows)-1].ID, 10))
	}
	for _, e := range rows {
		page.Items = append(page.Items, toAPIAudit(e, !scope.TeamID.Valid))
	}
	httpx.JSON(w, http.StatusOK, page)
}

// writeAuditEntry serves the entry with id if it is within scope.
func (a *api) writeAuditEntry(w http.ResponseWriter, r *http.Request, scope dbgen.ListAuditParams, id int64) {
	// The list query, newest first below id+1, one row: exactly this entry if it is in scope.
	before := id + 1
	rows, err := a.q.ListAudit(r.Context(), dbgen.ListAuditParams{
		TeamID: scope.TeamID, BeforeID: &before, PageSize: 1, ExcludePrefixes: []string{},
	})
	if err != nil {
		httpx.Internal(w, r, err)
		return
	}
	if len(rows) == 0 || rows[0].ID != id {
		httpx.Error(w, http.StatusNotFound, "not_found", "Audit entry not found")
		return
	}
	httpx.JSON(w, http.StatusOK, toAPIAudit(rows[0], !scope.TeamID.Valid))
}

// toAPIAudit converts one entry; platform is whether it is read in the
// platform log (the caller's address is shown there only).
func toAPIAudit(e dbgen.ListAuditRow, platform bool) apitypes.AuditEntry {
	out := apitypes.AuditEntry{
		Id: e.ID, OccurredAt: e.OccurredAt, ActorKind: apitypes.AuditEntryActorKind(e.ActorKind),
		Actor:  toAPIAuditActor(e),
		Action: e.Action, TargetType: e.TargetType, TargetId: e.TargetID, RequestId: e.RequestID,
		ActorUserId: nullUUID(e.ActorUserID), TeamId: nullUUID(e.TeamID),
		TargetExists: e.LiveLabel != "",
		Metadata:     map[string]any{},
	}
	if e.TeamName != "" {
		out.TeamName, out.TeamSlug = &e.TeamName, &e.TeamSlug
	}
	// Names are never empty, so "" means the label is unknown.
	if e.LiveLabel != "" {
		out.TargetLabel = &e.LiveLabel
	} else if e.RecordedLabel != "" {
		out.TargetLabel = &e.RecordedLabel
	}
	if len(e.BeforeState) > 0 {
		out.Before = e.BeforeState
	}
	if len(e.AfterState) > 0 {
		out.After = e.AfterState
	}
	_ = json.Unmarshal(e.Metadata, &out.Metadata)
	switch {
	case e.ActorKind == "user" && e.ActorUserID.Valid:
		out.Via = &apitypes.AuditVia{Kind: apitypes.AuditViaKindSession}
	case e.ActorKind == "api_key":
		v := apitypes.AuditVia{Kind: apitypes.AuditViaKindApiKey}
		if name, ok := out.Metadata["apiKeyName"].(string); ok && name != "" {
			v.Name = &name
		}
		out.Via = &v
	case e.ActorKind == "oauth":
		v := apitypes.AuditVia{Kind: apitypes.AuditViaKindOauth}
		if name, ok := out.Metadata["oauthClient"].(string); ok && name != "" {
			v.Name = &name
		}
		out.Via = &v
	}
	if platform && e.ClientIP != "" {
		out.ClientIp = &e.ClientIP
	}
	return out
}

func toAPIAuditActor(e dbgen.ListAuditRow) apitypes.AuditActor {
	actor := apitypes.AuditActor{Kind: apitypes.AuditActorKind(e.ActorKind), UserId: nullUUID(e.ActorUserID)}
	if e.ActorEmail != "" {
		actor.Email = &e.ActorEmail
	}
	if e.ActorDisplayName != nil && *e.ActorDisplayName != "" {
		actor.DisplayName = e.ActorDisplayName
	}
	var meta map[string]any
	_ = json.Unmarshal(e.Metadata, &meta)
	if e.ActorKind == "service_account" {
		if name, ok := meta["serviceAccountName"].(string); ok && name != "" {
			actor.DisplayName = &name
		}
	}
	if e.ActorKind == "api_key" {
		if name, ok := meta["apiKeyName"].(string); ok && name != "" {
			actor.ApiKeyName = &name
		}
	}
	if e.ActorKind == "oauth" {
		if name, ok := meta["oauthClient"].(string); ok && name != "" {
			actor.OauthClient = &name
		}
	}
	if e.ActorKind == "agent" { // an agent changing its own schedules
		if name, ok := meta["agentActorName"].(string); ok && name != "" {
			actor.DisplayName = &name
		}
		if raw, ok := meta["agentActorId"].(string); ok {
			if id, err := uuid.Parse(raw); err == nil {
				actor.AgentId = &id
			}
		}
	}
	return actor
}
