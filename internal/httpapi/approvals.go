package httpapi

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/google/uuid"

	"github.com/ncecere/pi-durable-platform/internal/agents"
	"github.com/ncecere/pi-durable-platform/internal/httpapi/apitypes"
	"github.com/ncecere/pi-durable-platform/internal/httpx"
	"github.com/ncecere/pi-durable-platform/internal/llm"
)

// Tool approvals (docs/approvals.md): an agent's requests, a decision, and
// the caller's inbox. internal/agents authorizes every call.

func (a *api) approvalRoutes() []route {
	const one = "/v1/agents/{agentId}/approvals"
	k := a.sessionOrKey
	return []route{
		{"GET", one, k(a.listAgentApprovals)},
		{"GET", one + "/{approvalId}", k(a.getAgentApproval)},
		{"POST", one + "/{approvalId}", k(a.decideAgentApproval)},
		{"GET", "/v1/me/approvals", k(a.listMyApprovals)},
	}
}

func toAPIApproval(v agents.ApprovalView) apitypes.ToolApproval {
	t := v.Approval
	args := map[string]any{}
	_ = json.Unmarshal(t.Arguments, &args)
	out := apitypes.ToolApproval{Id: t.ID, AgentId: t.AgentID, AgentName: v.AgentName, ThreadId: t.ThreadID, ThreadTitle: v.ThreadTitle,
		Tool: t.Tool, CallId: t.CallID, Arguments: args, Status: apitypes.ToolApprovalStatus(t.Status), RequestedAt: t.RequestedAt,
		ExpiresAt: t.ExpiresAt, DecidedAt: t.DecidedAt, CanDecide: v.CanDecide}
	if t.Summary != "" {
		out.Summary = &t.Summary
	}
	if t.Reason != "" {
		out.Reason = &t.Reason
	}
	if t.RequestedForKind != nil && t.RequestedForID.Valid {
		id := t.RequestedForID.UUID.String()
		out.RequestedFor = &apitypes.ApprovalDecider{Kind: apitypes.ApprovalDeciderKind(*t.RequestedForKind), Id: &id, Name: t.RequestedForName}
	}
	if d := agents.DeciderOf(t); d != nil {
		dd := apitypes.ApprovalDecider{Kind: apitypes.ApprovalDeciderKind(d.Kind), Name: d.Name}
		if d.ID != "" {
			dd.Id = &d.ID
		}
		out.DecidedBy = &dd
	}
	return out
}

func (a *api) listAgentApprovals(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "agentId")
	if !ok {
		return
	}
	q := r.URL.Query()
	var limit int32
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 200 {
			httpx.Error(w, http.StatusBadRequest, "invalid_limit", "limit must be between 1 and 200")
			return
		}
		limit = int32(n)
	}
	rows, err := a.Agents.Approvals(r.Context(), a.actor(r), id, q.Get("thread"), q.Get("status"), limit)
	writeList(w, r, rows, err, toAPIApproval)
}

func (a *api) getAgentApproval(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "agentId")
	if !ok {
		return
	}
	v, err := a.Agents.Approval(r.Context(), a.actor(r), id, r.PathValue("approvalId"))
	if failed(w, r, err) {
		return
	}
	httpx.JSON(w, http.StatusOK, toAPIApproval(v))
}

func (a *api) decideAgentApproval(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "agentId")
	if !ok {
		return
	}
	var in apitypes.ToolApprovalDecision
	if !httpx.Decode(w, r, &in) {
		return
	}
	reason := ""
	setIf(&reason, in.Reason)
	v, err := a.Agents.DecideApproval(r.Context(), a.actor(r), id, r.PathValue("approvalId"), string(in.Decision), reason)
	if failed(w, r, err) {
		return
	}
	httpx.JSON(w, http.StatusOK, toAPIApproval(v))
}

func (a *api) listMyApprovals(w http.ResponseWriter, r *http.Request) {
	rows, err := a.Agents.MyApprovals(r.Context(), a.actor(r))
	writeList(w, r, rows, err, toAPIApproval)
}

// registerApprovalReports takes the runtime's approval requests: one
// (/approvals/report, answered with what the platform knows of it) or all of
// its pending ones when it starts (/approvals/sync, answered with the
// decisions the platform already has).
func registerApprovalReports(mux *http.ServeMux, ag *agents.Service, log *slog.Logger) {
	authed := func(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
		id, token, ok := llm.Credential(r)
		if !ok {
			httpx.Error(w, http.StatusUnauthorized, "unauthorized", "A gateway credential is required")
			return uuid.Nil, false
		}
		if _, _, err := ag.AuthenticateProxy(r.Context(), id, token); err != nil {
			httpx.Error(w, http.StatusUnauthorized, "unauthorized", "Invalid gateway credential")
			return uuid.Nil, false
		}
		return id, true
	}
	mux.HandleFunc("POST /approvals/report", func(w http.ResponseWriter, r *http.Request) {
		id, ok := authed(w, r)
		if !ok {
			return
		}
		var in agents.ApprovalReport
		if err := json.NewDecoder(io.LimitReader(r.Body, 256<<10)).Decode(&in); err != nil {
			httpx.Error(w, http.StatusBadRequest, "invalid_request", "The body must be an approval request")
			return
		}
		ans, err := ag.ReportApproval(r.Context(), id, in)
		if failed(w, r, err) {
			return
		}
		httpx.JSON(w, http.StatusOK, ans)
	})
	mux.HandleFunc("POST /approvals/sync", func(w http.ResponseWriter, r *http.Request) {
		id, ok := authed(w, r)
		if !ok {
			return
		}
		var in struct {
			Approvals []agents.ApprovalReport `json:"approvals"`
			At        int64                   `json:"at"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 8<<20)).Decode(&in); err != nil || len(in.Approvals) > 10_000 {
			httpx.Error(w, http.StatusBadRequest, "invalid_request", "The body must be the runtime's pending approvals")
			return
		}
		out, err := ag.SyncApprovals(r.Context(), id, in.Approvals, in.At)
		if err != nil {
			log.Warn("sync approvals", "agent", id, "err", err)
			failed(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusOK, out)
	})
}
