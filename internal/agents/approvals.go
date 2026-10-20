package agents

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ncecere/pi-durable-platform/internal/apperr"
	"github.com/ncecere/pi-durable-platform/internal/audit"
	"github.com/ncecere/pi-durable-platform/internal/authz"
	"github.com/ncecere/pi-durable-platform/internal/store"
	"github.com/ncecere/pi-durable-platform/internal/store/dbgen"
)

// Tool approvals (docs/approvals.md): the agent's runtime reports a call
// that waits for a person (ReportApproval, through the gateway); a person
// decides (DecideApproval); the decision, or a time-out (RunApprovals), is
// delivered to the runtime, waking the agent if it is hibernated. The
// runtime keeps the wait itself (in the tool call's beforeTool hook, before
// the call's intent is recorded), so it survives restarts and hibernation.

var (
	errApprovalNotFound = apperr.NotFound("approval_not_found", "Approval not found")
	errApprovalDecided  = apperr.Conflict("approval_decided", "This approval was already decided, timed out or cancelled")
	errBadDecision      = apperr.Invalid("invalid_decision", "decision must be approve or deny")
	errBadApprovalWhy   = apperr.Invalid("invalid_reason", "reason must be at most 1000 characters")
	errBadApprovalState = apperr.Invalid("invalid_status", "status must be pending, decided or all")
	errBadApprovals     = apperr.Invalid("invalid_approvals",
		"approvals.tools are at most 100 tool names or patterns of 1-200 letters, digits, _ . - or *; role is admin, editor or member; timeoutSeconds is 60 to 2592000")
	errTooManyApprovals = errors.New("too many pending approvals")
	approvalIDRE        = regexp.MustCompile(`^ap_[A-Za-z0-9_-]{1,64}$`)
	toolPatternRE       = regexp.MustCompile(`^[A-Za-z0-9_.*-]{1,200}$`)
	approvalClient      = &http.Client{Timeout: 15 * time.Second}
)

// Approval statuses.
const (
	ApprovalPending   = "pending"
	ApprovalApproved  = "approved"
	ApprovalDenied    = "denied"
	ApprovalTimedOut  = "timed_out"
	ApprovalCancelled = "cancelled"
)

// MaxPendingApprovals is how many requests may wait for a decision per
// agent. The runtime reports requests and is not trusted: beyond it a new
// request is refused, not recorded (no row, no audit entry), and the answer
// denies it, which the runtime applies like a person's denial (the call
// doesn't run, and the model reads why).
const MaxPendingApprovals = 100

// tooManyApprovals is the denial's reason, for the model.
const tooManyApprovals = "too many approval requests of this agent are waiting for a decision; ask again once people have decided some"

// DefaultApprovalTimeout is how long a request waits for a decision unless
// the version says otherwise.
const DefaultApprovalTimeout = 24 * 60 * 60

// ApprovalPolicy is a template version's: which tools ask first, the lowest
// team role that may decide in shared threads, and how long a request waits.
type ApprovalPolicy struct {
	Tools          []string
	Role           string
	TimeoutSeconds int32
}

// ApprovalPolicyInput changes a policy; nil fields keep the base's.
type ApprovalPolicyInput struct {
	Tools          *[]string
	Role           *string
	TimeoutSeconds *int32
}

// DefaultApprovalPolicy asks for nothing.
func DefaultApprovalPolicy() ApprovalPolicy {
	return ApprovalPolicy{Tools: []string{}, Role: authz.RoleAdmin, TimeoutSeconds: DefaultApprovalTimeout}
}

// PolicyOf is a version's policy.
func PolicyOf(tv dbgen.AgentTemplateVersion) ApprovalPolicy {
	tools := tv.ApprovalTools
	if tools == nil {
		tools = []string{}
	}
	return ApprovalPolicy{Tools: tools, Role: tv.ApprovalRole, TimeoutSeconds: tv.ApprovalTimeoutSeconds}
}

// apply returns base with the input's changes, checked.
func (in *ApprovalPolicyInput) apply(base ApprovalPolicy) (ApprovalPolicy, error) {
	out := base
	if in == nil {
		return out, nil
	}
	if in.Tools != nil {
		seen := map[string]bool{}
		out.Tools = []string{}
		for _, t := range *in.Tools {
			t = strings.TrimSpace(t)
			if !toolPatternRE.MatchString(t) {
				return base, errBadApprovals
			}
			if !seen[t] {
				seen[t] = true
				out.Tools = append(out.Tools, t)
			}
		}
		if len(out.Tools) > 100 {
			return base, errBadApprovals
		}
	}
	if in.Role != nil {
		switch *in.Role {
		case authz.RoleAdmin, authz.RoleEditor, authz.RoleMember:
			out.Role = *in.Role
		default:
			return base, errBadApprovals
		}
	}
	if in.TimeoutSeconds != nil {
		if *in.TimeoutSeconds < 60 || *in.TimeoutSeconds > 30*24*60*60 {
			return base, errBadApprovals
		}
		out.TimeoutSeconds = *in.TimeoutSeconds
	}
	return out, nil
}

// approvalEnv is the runtime's TOOL_APPROVALS ("" when nothing asks).
func approvalEnv(p ApprovalPolicy) string {
	if len(p.Tools) == 0 {
		return ""
	}
	b, _ := json.Marshal(map[string]any{"tools": p.Tools, "timeoutSeconds": p.TimeoutSeconds})
	return string(b)
}

// ApprovalView is a request with what a viewer is shown: the agent's name, the
// thread's title (absent: the platform has no record of the thread) and
// whether the viewer may decide it.
type ApprovalView struct {
	Approval    dbgen.ToolApproval
	AgentName   string
	ThreadTitle *string
	CanDecide   bool
}

// approvalThread is what the rules need of an approval's thread.
type approvalThread struct {
	visibility    *string
	user, service uuid.NullUUID
	title         *string
	agentName     string
}

// approvalSubject is the approval as the actor sees it.
func approvalSubject(a authz.Actor, sub authz.AgentSubject, t dbgen.ToolApproval, th approvalThread) authz.ApprovalSubject {
	private := th.visibility != nil && *th.visibility == VisibilityPrivate
	owner := false
	if a.Service != nil {
		owner = th.service.Valid && th.service.UUID == a.Service.AccountID
	} else {
		owner = a.UserID != uuid.Nil && th.user.Valid && th.user.UUID == a.UserID
	}
	author := false
	if t.RequestedForKind != nil && t.RequestedForID.Valid {
		switch *t.RequestedForKind {
		case PrincipalUser:
			author = a.Service == nil && a.UserID == t.RequestedForID.UUID
		case PrincipalService:
			author = a.Service != nil && a.Service.AccountID == t.RequestedForID.UUID
		}
	}
	return authz.ApprovalSubject{Agent: sub, SeesThread: !private || owner, PrivateThread: private, RunAuthor: author,
		Unattended: t.RequestedForKind == nil, MinRole: t.ApproverRole}
}

func (s *Service) approvalView(a authz.Actor, sub authz.AgentSubject, t dbgen.ToolApproval, th approvalThread) (ApprovalView, error) {
	as := approvalSubject(a, sub, t, th)
	if err := authz.CheckApprovalSee(as); err != nil {
		return ApprovalView{}, err
	}
	return ApprovalView{Approval: t, AgentName: th.agentName, ThreadTitle: th.title,
		CanDecide: t.Status == ApprovalPending && authz.CheckApprovalDecide(as) == nil}, nil
}

// Approvals lists an agent's requests the actor may see, newest first:
// status pending (""), decided or all; thread "" for every thread; limit
// 1-200 (0: 100).
func (s *Service) Approvals(ctx context.Context, a authz.Actor, agentID uuid.UUID, thread, status string, limit int32) ([]ApprovalView, error) {
	switch status {
	case "":
		status = ApprovalPending
	case ApprovalPending, "decided", "all":
	default:
		return nil, errBadApprovalState
	}
	if thread != "" && !threadIDRE.MatchString(thread) {
		return nil, apperr.Invalid("invalid_thread_id", "Thread IDs are 1-64 letters, digits, dashes or underscores")
	}
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	ag, err := s.usable(ctx, a, agentID)
	if err != nil {
		return nil, err
	}
	sub, err := subject(ctx, s.q, a, ag)
	if err != nil {
		return nil, err
	}
	user, service := creator(a)
	p := dbgen.ListAgentToolApprovalsParams{AgentID: agentID, Status: status, ViewerUser: user, ViewerService: service, RowLimit: limit}
	if thread != "" {
		p.Thread = &thread
	}
	rows, err := s.q.ListAgentToolApprovals(ctx, p)
	if err != nil {
		return nil, err
	}
	out := make([]ApprovalView, 0, len(rows))
	for _, r := range rows {
		v, err := s.approvalView(a, sub, r.ToolApproval, approvalThread{visibility: r.ThreadVisibility, user: r.ThreadUser,
			service: r.ThreadService, title: r.ThreadTitle, agentName: r.AgentName})
		if err == nil {
			out = append(out, v)
		}
	}
	return out, nil
}

// Approval returns one request the actor may see.
func (s *Service) Approval(ctx context.Context, a authz.Actor, agentID uuid.UUID, id string) (ApprovalView, error) {
	ag, err := s.usable(ctx, a, agentID)
	if err != nil {
		return ApprovalView{}, err
	}
	return s.approvalOf(ctx, s.q, a, ag, id)
}

func (s *Service) approvalOf(ctx context.Context, q *dbgen.Queries, a authz.Actor, ag dbgen.Agent, id string) (ApprovalView, error) {
	if !approvalIDRE.MatchString(id) {
		return ApprovalView{}, errApprovalNotFound
	}
	r, err := q.GetToolApprovalView(ctx, dbgen.GetToolApprovalViewParams{AgentID: ag.ID, ID: id})
	if errors.Is(store.NotFound(err), store.ErrNotFound) {
		return ApprovalView{}, errApprovalNotFound
	} else if err != nil {
		return ApprovalView{}, err
	}
	sub, err := subject(ctx, q, a, ag)
	if err != nil {
		return ApprovalView{}, err
	}
	return s.approvalView(a, sub, r.ToolApproval, approvalThread{visibility: r.ThreadVisibility, user: r.ThreadUser,
		service: r.ThreadService, title: r.ThreadTitle, agentName: r.AgentName})
}

// MyApprovals is the actor's inbox: pending requests, across the agents of
// their teams, that they may decide (newest first, at most 200).
func (s *Service) MyApprovals(ctx context.Context, a authz.Actor) ([]ApprovalView, error) {
	type row struct {
		t        dbgen.ToolApproval
		th       approvalThread
		role     string
		managed  bool
		agentRef uuid.UUID
	}
	var rows []row
	if a.Service != nil {
		rs, err := s.q.ListPendingToolApprovalsForTeam(ctx, a.Service.TeamID)
		if err != nil {
			return nil, err
		}
		for _, r := range rs {
			rows = append(rows, row{t: r.ToolApproval, role: a.Service.Role, managed: r.ServiceManaged,
				th: approvalThread{visibility: r.ThreadVisibility, user: r.ThreadUser, service: r.ThreadService, title: r.ThreadTitle, agentName: r.AgentName}})
		}
	} else if a.UserID != uuid.Nil {
		rs, err := s.q.ListPendingToolApprovalsForUser(ctx, a.UserID)
		if err != nil {
			return nil, err
		}
		for _, r := range rs {
			rows = append(rows, row{t: r.ToolApproval, role: r.MemberRole, managed: r.ServiceManaged,
				th: approvalThread{visibility: r.ThreadVisibility, user: r.ThreadUser, service: r.ThreadService, title: r.ThreadTitle, agentName: r.AgentName}})
		}
	}
	out := []ApprovalView{}
	for _, r := range rows {
		sub := authz.AgentSubject{Role: r.role, IsService: a.Service != nil, ServiceManaged: r.managed}
		v, err := s.approvalView(a, sub, r.t, r.th)
		if err != nil || !v.CanDecide {
			continue
		}
		out = append(out, v)
		if len(out) == 200 {
			break
		}
	}
	return out, nil
}

// DecideApproval approves or denies a pending request (reason: at most 1000
// characters, given to the model on denial), audited in the same
// transaction, then delivers the decision to the agent's runtime (waking it)
// in the background; the approvals loop retries until it lands.
func (s *Service) DecideApproval(ctx context.Context, a authz.Actor, agentID uuid.UUID, id, decision, reason string) (ApprovalView, error) {
	status := ""
	switch decision {
	case "approve":
		status = ApprovalApproved
	case "deny":
		status = ApprovalDenied
	default:
		return ApprovalView{}, errBadDecision
	}
	reason = strings.TrimSpace(reason)
	if utf8.RuneCountInString(reason) > 1000 {
		return ApprovalView{}, errBadApprovalWhy
	}
	ag, err := s.usable(ctx, a, agentID)
	if err != nil {
		return ApprovalView{}, err
	}
	who, err := s.author(ctx, a)
	if err != nil {
		return ApprovalView{}, err
	}
	var out ApprovalView
	err = store.InTx(ctx, s.pool, func(q *dbgen.Queries, _ pgx.Tx) error {
		if !approvalIDRE.MatchString(id) {
			return errApprovalNotFound
		}
		if _, err := q.LockToolApproval(ctx, dbgen.LockToolApprovalParams{AgentID: agentID, ID: id}); errors.Is(store.NotFound(err), store.ErrNotFound) {
			return errApprovalNotFound
		} else if err != nil {
			return err
		}
		r, err := q.GetToolApprovalView(ctx, dbgen.GetToolApprovalViewParams{AgentID: agentID, ID: id})
		if err != nil {
			return err
		}
		sub, err := subject(ctx, q, a, ag)
		if err != nil {
			return err
		}
		th := approvalThread{visibility: r.ThreadVisibility, user: r.ThreadUser, service: r.ThreadService, title: r.ThreadTitle, agentName: r.AgentName}
		v, err := s.approvalView(a, sub, r.ToolApproval, th)
		if err != nil {
			return err // not found
		}
		if err := authz.CheckApprovalDecide(approvalSubject(a, sub, r.ToolApproval, th)); err != nil {
			return err
		}
		if r.ToolApproval.Status != ApprovalPending {
			return errApprovalDecided
		}
		user, service := creator(a)
		t, err := q.DecideToolApproval(ctx, dbgen.DecideToolApprovalParams{AgentID: agentID, ID: id, Status: status,
			DecidedByUser: user, DecidedByService: service, DecidedByName: clip(who.Name, 200), Reason: reason})
		if errors.Is(store.NotFound(err), store.ErrNotFound) {
			return errApprovalDecided
		} else if err != nil {
			return err
		}
		e := a.Audit("tool_approval.decide", "tool_approval", agentID.String()+"/"+id)
		e.TeamID = ag.TeamID
		if e.Metadata == nil {
			e.Metadata = map[string]any{}
		}
		e.Metadata["decision"], e.Metadata["tool"], e.Metadata["thread"] = decision, t.Tool, t.ThreadID
		if reason != "" {
			e.Metadata["reason"] = reason
		}
		if err := audit.Record(ctx, q, e); err != nil {
			return err
		}
		v.Approval, v.CanDecide = t, false
		out = v
		return nil
	})
	if err != nil {
		return ApprovalView{}, err
	}
	s.deliverSoon(out.Approval)
	return out, nil
}

func clip(s string, n int) string {
	if utf8.RuneCountInString(s) > n {
		return string([]rune(s)[:n])
	}
	return s
}

// ApprovalPrincipal is who a run is for, or who decided, as the runtime and
// the API name them.
type ApprovalPrincipal struct {
	Kind string `json:"kind"`
	ID   string `json:"id,omitempty"`
	Name string `json:"name"`
}

// ApprovalReport is a request as the agent's runtime reports it (untrusted).
type ApprovalReport struct {
	ID           string             `json:"id"`
	ThreadID     string             `json:"threadId"`
	Tool         string             `json:"tool"`
	CallID       string             `json:"callId"`
	Arguments    json.RawMessage    `json:"arguments"`
	Summary      string             `json:"summary"`
	RequestedFor *ApprovalPrincipal `json:"requestedFor"`
	RequestedAt  int64              `json:"requestedAt"`
	Status       string             `json:"status"`
}

// ApprovalAnswer tells the runtime what the platform knows of a request:
// its expiry, and its decision once there is one.
type ApprovalAnswer struct {
	ID        string             `json:"id"`
	Status    string             `json:"status"`
	Reason    string             `json:"reason,omitempty"`
	DecidedBy *ApprovalPrincipal `json:"decidedBy,omitempty"`
	ExpiresAt int64              `json:"expiresAt"`
}

func answerOf(t dbgen.ToolApproval) ApprovalAnswer {
	out := ApprovalAnswer{ID: t.ID, Status: t.Status, Reason: t.Reason, ExpiresAt: t.ExpiresAt.UnixMilli()}
	out.DecidedBy = DeciderOf(t)
	return out
}

// DeciderOf is who decided (the system for a time-out or a cancellation).
func DeciderOf(t dbgen.ToolApproval) *ApprovalPrincipal {
	switch {
	case t.DecidedByUser.Valid:
		return &ApprovalPrincipal{Kind: PrincipalUser, ID: t.DecidedByUser.UUID.String(), Name: t.DecidedByName}
	case t.DecidedByService.Valid:
		return &ApprovalPrincipal{Kind: PrincipalService, ID: t.DecidedByService.UUID.String(), Name: t.DecidedByName}
	case t.Status == ApprovalTimedOut || t.Status == ApprovalCancelled:
		return &ApprovalPrincipal{Kind: "system", Name: "System"}
	case t.DecidedByName != "":
		return &ApprovalPrincipal{Kind: PrincipalUser, Name: t.DecidedByName} // the person's account was deleted since
	}
	return nil
}

// ReportApproval records a request the agent's runtime reported (once:
// every rerun of its hook reports again) and answers with what the platform
// knows of it. The expiry and who may decide come from the agent's version,
// not from the runtime. A report with status cancelled cancels it.
func (s *Service) ReportApproval(ctx context.Context, agentID uuid.UUID, r ApprovalReport) (ApprovalAnswer, error) {
	if !approvalIDRE.MatchString(r.ID) || (r.ThreadID != "" && !threadIDRE.MatchString(r.ThreadID)) ||
		r.Tool == "" || utf8.RuneCountInString(r.Tool) > 200 || utf8.RuneCountInString(r.CallID) > 200 {
		return ApprovalAnswer{}, apperr.Invalid("invalid_approval", "id, tool and callId are required")
	}
	var out ApprovalAnswer
	err := store.InTx(ctx, s.pool, func(q *dbgen.Queries, _ pgx.Tx) error {
		ag, err := s.load(ctx, q, agentID)
		if err != nil {
			return err
		}
		t, err := s.recordRequest(ctx, q, ag, r)
		if errors.Is(err, errTooManyApprovals) {
			out = ApprovalAnswer{ID: r.ID, Status: ApprovalDenied, Reason: tooManyApprovals,
				DecidedBy: &ApprovalPrincipal{Kind: "system", Name: "Platform"}}
			return nil
		} else if err != nil {
			return err
		}
		if r.Status == ApprovalCancelled && t.Status == ApprovalPending {
			if t, err = q.CancelToolApproval(ctx, dbgen.CancelToolApprovalParams{AgentID: agentID, ID: r.ID}); err != nil {
				return err
			}
			if err := auditSystem(ctx, q, ag, t, "tool_approval.cancel"); err != nil {
				return err
			}
		}
		out = answerOf(t)
		return nil
	})
	return out, err
}

// recordRequest inserts a reported request (audited) or returns the one
// already recorded; errTooManyApprovals when the agent has
// MaxPendingApprovals waiting.
func (s *Service) recordRequest(ctx context.Context, q *dbgen.Queries, ag dbgen.Agent, r ApprovalReport) (dbgen.ToolApproval, error) {
	existing, err := q.GetToolApproval(ctx, dbgen.GetToolApprovalParams{AgentID: ag.ID, ID: r.ID})
	if err == nil {
		return existing, nil
	} else if !errors.Is(store.NotFound(err), store.ErrNotFound) {
		return existing, err
	}
	// One new request of the agent at a time (the count holds).
	if _, err := q.LockAgentRuntime(ctx, ag.ID); err != nil {
		return dbgen.ToolApproval{}, err
	}
	if n, err := q.CountPendingToolApprovals(ctx, dbgen.CountPendingToolApprovalsParams{AgentID: ag.ID, Bound: MaxPendingApprovals}); err != nil {
		return dbgen.ToolApproval{}, err
	} else if n >= MaxPendingApprovals {
		slog.Warn("approvals: a request refused, the agent has too many waiting", "agent", ag.ID, "max", MaxPendingApprovals)
		return dbgen.ToolApproval{}, errTooManyApprovals
	}
	tv, err := version(ctx, q, ag.TemplateID, ag.TemplateVersion)
	if err != nil {
		return dbgen.ToolApproval{}, err
	}
	policy := PolicyOf(tv)
	now := time.Now()
	at := time.UnixMilli(r.RequestedAt)
	if r.RequestedAt <= 0 || at.After(now.Add(time.Minute)) || at.Before(now.Add(-24*time.Hour)) {
		at = now
	}
	p := dbgen.InsertToolApprovalParams{AgentID: ag.ID, ID: r.ID, ThreadID: r.ThreadID, Tool: r.Tool, CallID: r.CallID,
		Arguments: redactArguments(r.Arguments), Summary: clip(strings.TrimSpace(r.Summary), 2000), ApproverRole: policy.Role,
		RequestedAt: at, ExpiresAt: at.Add(time.Duration(policy.TimeoutSeconds) * time.Second)}
	if f := r.RequestedFor; f != nil && (f.Kind == PrincipalUser || f.Kind == PrincipalService) {
		if id, err := uuid.Parse(f.ID); err == nil {
			kind := f.Kind
			p.RequestedForKind, p.RequestedForID = &kind, uuid.NullUUID{UUID: id, Valid: true}
			p.RequestedForName = clip(strings.TrimSpace(f.Name), 200)
		}
	}
	t, err := q.InsertToolApproval(ctx, p)
	if errors.Is(store.NotFound(err), store.ErrNotFound) { // reported twice at once
		return q.GetToolApproval(ctx, dbgen.GetToolApprovalParams{AgentID: ag.ID, ID: r.ID})
	} else if err != nil {
		return t, err
	}
	e := audit.Entry{ActorKind: audit.ActorSystem, TeamID: ag.TeamID, Action: "tool_approval.request",
		TargetType: "tool_approval", TargetID: ag.ID.String() + "/" + t.ID,
		Metadata: map[string]any{"tool": t.Tool, "thread": t.ThreadID, "expiresAt": t.ExpiresAt.UTC().Format(time.RFC3339)}}
	if t.RequestedForKind != nil {
		e.Metadata["requestedForKind"], e.Metadata["requestedForId"] = *t.RequestedForKind, t.RequestedForID.UUID.String()
	}
	return t, audit.Record(ctx, q, e)
}

// auditSystem records a change the platform made on its own (a time-out, a
// cancellation the runtime reported).
func auditSystem(ctx context.Context, q *dbgen.Queries, ag dbgen.Agent, t dbgen.ToolApproval, action string) error {
	return audit.Record(ctx, q, audit.Entry{ActorKind: audit.ActorSystem, TeamID: ag.TeamID, Action: action,
		TargetType: "tool_approval", TargetID: ag.ID.String() + "/" + t.ID,
		Metadata: map[string]any{"tool": t.Tool, "thread": t.ThreadID}})
}

// SyncApprovals takes the runtime's pending requests when it starts:
// records any the platform missed, answers with the decisions it already has
// for them, and cancels pending requests the runtime no longer has (asked
// before its list was taken: at, the runtime's clock).
func (s *Service) SyncApprovals(ctx context.Context, agentID uuid.UUID, reports []ApprovalReport, at int64) ([]ApprovalAnswer, error) {
	keep := []string{}
	out := []ApprovalAnswer{}
	for _, r := range reports {
		ans, err := s.ReportApproval(ctx, agentID, r)
		if ae, ok := apperr.As(err); ok && ae.Status == http.StatusBadRequest {
			continue
		} else if err != nil {
			return nil, err
		}
		keep = append(keep, r.ID)
		if ans.Status != ApprovalPending {
			out = append(out, ans)
		}
	}
	if at <= 0 {
		return out, nil
	}
	err := store.InTx(ctx, s.pool, func(q *dbgen.Queries, _ pgx.Tx) error {
		ag, err := s.load(ctx, q, agentID)
		if err != nil {
			return err
		}
		gone, err := q.CancelToolApprovalsNotIn(ctx, dbgen.CancelToolApprovalsNotInParams{AgentID: agentID, Before: time.UnixMilli(at), Keep: keep})
		if err != nil {
			return err
		}
		for _, t := range gone {
			if err := auditSystem(ctx, q, ag, t, "tool_approval.cancel"); err != nil {
				return err
			}
		}
		return nil
	})
	return out, err
}

// RunApprovals times out pending requests and delivers decisions every
// interval until ctx ends. Every replica runs it: a time-out changes each row
// once, and a delivery is claimed by one replica (and is idempotent anyway).
func (s *Service) RunApprovals(ctx context.Context, interval time.Duration, log *slog.Logger) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		done := s.Metrics.Loop("approvals")
		err := s.ApprovalsOnce(ctx)
		if err != nil && ctx.Err() == nil {
			log.Warn("approvals: time-outs and deliveries", "err", err)
		}
		if ctx.Err() == nil {
			done(err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// ApprovalsOnce is one pass: pending requests past their expiry become
// timed_out (audited), then due deliveries are attempted.
func (s *Service) ApprovalsOnce(ctx context.Context) error {
	err := store.InTx(ctx, s.pool, func(q *dbgen.Queries, _ pgx.Tx) error {
		expired, err := q.ExpireToolApprovals(ctx)
		if err != nil {
			return err
		}
		agents := map[uuid.UUID]dbgen.Agent{}
		for _, t := range expired {
			ag, ok := agents[t.AgentID]
			if !ok {
				if ag, err = s.load(ctx, q, t.AgentID); err != nil {
					return err
				}
				agents[t.AgentID] = ag
			}
			if err := auditSystem(ctx, q, ag, t, "tool_approval.timeout"); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	due, err := s.q.ClaimApprovalDeliveries(ctx)
	if err != nil {
		return err
	}
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for _, t := range due {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			if err := s.deliver(ctx, t); err != nil {
				slog.Debug("approval delivery failed; retrying later", "agent", t.AgentID, "approval", t.ID, "err", err)
			}
		}()
	}
	wg.Wait()
	return nil
}

// deliverSoon delivers a decision now, in the background (the person's
// request doesn't wait for a cold start); the approvals loop retries.
func (s *Service) deliverSoon(t dbgen.ToolApproval) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		if err := s.deliver(ctx, t); err != nil {
			slog.Debug("approval delivery failed; the approvals loop retries", "agent", t.AgentID, "approval", t.ID, "err", err)
		}
	}()
}

// deliver tells the agent's runtime the decision (waking it): POST
// /approvals/{id}. Delivered once the runtime has it, or has no such request
// (404: the call is gone).
func (s *Service) deliver(ctx context.Context, t dbgen.ToolApproval) error {
	decision := map[string]string{ApprovalApproved: "approve", ApprovalDenied: "deny", ApprovalTimedOut: "timeout", ApprovalCancelled: "cancel"}[t.Status]
	if decision == "" {
		return nil
	}
	ag, err := s.load(ctx, s.q, t.AgentID)
	if err != nil {
		return err
	}
	if !ag.Enabled {
		return errDisabled
	}
	if err := s.checkRunning(ctx, ag); err != nil {
		return err
	}
	spec, err := s.spec(ctx, ag)
	if err != nil {
		return err
	}
	ep, err := s.host.Wake(ctx, spec)
	if err != nil {
		return err
	}
	body := map[string]any{"decision": decision}
	if t.Reason != "" {
		body["reason"] = t.Reason
	}
	if d := DeciderOf(t); d != nil {
		body["decidedBy"] = d
	}
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(ep.BaseURL, "/")+"/approvals/"+t.ID, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+ep.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := approvalClient.Do(req)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	if resp.StatusCode/100 != 2 && resp.StatusCode != http.StatusNotFound {
		return fmt.Errorf("runtime answered %d", resp.StatusCode)
	}
	return s.q.MarkToolApprovalDelivered(context.WithoutCancel(ctx), dbgen.MarkToolApprovalDeliveredParams{AgentID: t.AgentID, ID: t.ID})
}
