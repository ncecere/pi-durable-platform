package agents

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/ncecere/pi-durable-platform/internal/agenthost"
	"github.com/ncecere/pi-durable-platform/internal/authz"
	"github.com/ncecere/pi-durable-platform/internal/store"
	"github.com/ncecere/pi-durable-platform/internal/store/dbgen"
	"github.com/ncecere/pi-durable-platform/internal/teams"
)

// Runs that triggers start (internal/triggers; docs/triggers.md). A
// triggered run acts as no person: its message's author is the trigger, its
// thread is shared and started by nobody, it never reaches a thread that is
// someone's (private or reserved) or that an app gave MCP servers (its
// person's credentials), and its sender is recorded as not through an app,
// so no connected app is credited with its usage. The agent uses its team's
// model providers and MCP servers through the gateway, as always.

// AuthorTrigger is the author kind of a triggered run's message.
const AuthorTrigger = "trigger"

// TriggerMessage is one fire's message to an agent's thread.
type TriggerMessage struct {
	AgentID  uuid.UUID
	ThreadID string
	Text     string
	// RequestID is the fire's ID: pi-durable runs a requestId once per
	// conversation, so delivering again never runs it twice.
	RequestID   string
	TriggerID   uuid.UUID
	TriggerName string
	// At is when the message is sent (the thread's status time).
	At time.Time
	// AgentMade: the trigger is one the agent made itself (its schedule
	// tool), which may also reach its personal workspace owner's private
	// threads (TriggerThreadRefusal).
	AgentMade bool
}

// TriggerThreadRefusal is why one of an agent's threads may not take a
// triggered message ("" when it may), from what the platform records of it
// (TriggerThreadInfo): a thread that is someone's (private, or reserved for
// its creator's first message) or that an app gave MCP servers (its person's
// credentials) never does. agentMade relaxes one rule: in a personal
// workspace, a private thread its owner started may take a message from a
// schedule the agent made itself (the workspace and the agent are the
// owner's; the MCP identity token names them with person_present false).
// known is required of an agent-made trigger's thread: it was set in a
// thread the platform knows.
func TriggerThreadRefusal(info dbgen.TriggerThreadInfoRow, agentMade bool) string {
	switch {
	case !info.Known:
		if agentMade {
			return "thread_unknown"
		}
		return "" // a fixed thread not used yet: the run starts it, shared
	case info.Reserved:
		return "thread_reserved"
	case info.HasServers:
		return "thread_mcp_servers"
	case info.Visibility != nil && *info.Visibility == VisibilityPrivate:
		if agentMade && info.TeamKind == teams.KindPersonal && info.PersonalOwner.Valid && info.CreatedByUser.Valid &&
			info.CreatedByUser.UUID == info.PersonalOwner.UUID {
			return ""
		}
		return "thread_private"
	}
	return ""
}

// TriggerError is why a triggered message wasn't delivered: Permanent when
// trying again can't help (the agent was turned off, the thread is
// someone's), else the agent could not be reached now.
type TriggerError struct {
	Code      string
	Permanent bool
}

func (e *TriggerError) Error() string { return "triggered run: " + e.Code }

func permanent(code string) error { return &TriggerError{Code: code, Permanent: true} }
func transient(code string) error { return &TriggerError{Code: code} }

// Subject resolves the actor's relationship to an agent (for other packages
// that authorize work on agents: internal/triggers).
func Subject(ctx context.Context, q *dbgen.Queries, a authz.Actor, ag dbgen.Agent) (authz.AgentSubject, error) {
	return subject(ctx, q, a, ag)
}

// SendTriggered delivers a triggered run's message: it records the thread
// (shared, started by nobody) if it is new, wakes the agent and relays the
// message with the fire's ID as its requestId and the trigger as its author.
// A retry with the same message delivers it at most once.
func (s *Service) SendTriggered(ctx context.Context, m TriggerMessage) error {
	ag, err := s.load(ctx, s.q, m.AgentID)
	if errors.Is(err, errNotFound) {
		return permanent("agent_not_found")
	} else if err != nil {
		return err
	}
	switch {
	case !ag.Enabled:
		return permanent("agent_disabled")
	case ag.ServiceManaged:
		return permanent("agent_service_managed")
	}
	if err := s.checkRunning(ctx, ag); err != nil {
		return transient("too_many_running")
	}
	title := m.TriggerName
	if utf8.RuneCountInString(title) > 60 {
		title = string([]rune(title)[:59]) + "…"
	}
	t, err := s.q.ClaimAgentThread(ctx, dbgen.ClaimAgentThreadParams{AgentID: ag.ID, ThreadID: m.ThreadID, Title: title,
		At: m.At, Visibility: VisibilityShared})
	created := err == nil
	if errors.Is(store.NotFound(err), store.ErrNotFound) {
		if t, err = s.q.GetAgentThread(ctx, dbgen.GetAgentThreadParams{AgentID: ag.ID, ThreadID: m.ThreadID}); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	unclaim := func() {
		if created {
			_, _ = s.q.UnclaimAgentThread(context.WithoutCancel(ctx), dbgen.UnclaimAgentThreadParams{AgentID: ag.ID, ThreadID: m.ThreadID, At: t.StatusAt})
		}
	}
	switch {
	case t.Visibility == VisibilityPrivate && m.AgentMade:
		info, err := s.q.TriggerThreadInfo(ctx, dbgen.TriggerThreadInfoParams{AgentID: ag.ID, ThreadID: m.ThreadID})
		if err != nil {
			return err
		}
		if code := TriggerThreadRefusal(info, true); code != "" {
			return permanent(code)
		}
	case t.Visibility == VisibilityPrivate:
		return permanent("thread_private")
	}
	switch {
	case t.Reserved:
		return permanent("thread_reserved")
	}
	servers, err := s.q.ListThreadMCPServers(ctx, dbgen.ListThreadMCPServersParams{AgentID: ag.ID, ThreadID: &m.ThreadID})
	if err != nil {
		unclaim()
		return err
	}
	if len(servers) > 0 {
		unclaim()
		return permanent("thread_mcp_servers")
	}
	// Not through an app: the run's usage is no connected app's.
	if err := s.q.RecordRunSender(ctx, dbgen.RecordRunSenderParams{AgentID: ag.ID, ThreadID: m.ThreadID, SentAt: m.At}); err != nil {
		slog.WarnContext(ctx, "record a triggered run's sender", "agent", ag.ID, "err", err)
	}
	spec, err := s.spec(ctx, ag)
	if err != nil {
		return err
	}
	body, _ := json.Marshal(map[string]any{
		"text": m.Text, "requestId": m.RequestID, "announceAuthor": true,
		"author": Author{Kind: AuthorTrigger, ID: m.TriggerID.String(), Name: m.TriggerName},
	})
	status, _, err := s.host.Call(ctx, spec, http.MethodPost, "/threads/"+m.ThreadID+"/messages", body)
	switch {
	case errors.Is(err, agenthost.ErrDisabled):
		unclaim()
		return permanent("agent_disabled")
	case err != nil, status >= 500:
		// Maybe delivered: the record stays, and a retry goes to the same thread.
		return transient("agent_unavailable")
	case status >= 400:
		unclaim()
		return permanent("message_refused")
	}
	_ = s.q.TouchAgent(ctx, ag.ID)
	sent, err := s.q.ThreadSent(context.WithoutCancel(ctx), dbgen.ThreadSentParams{AgentID: ag.ID, ThreadID: m.ThreadID, Title: title,
		At: m.At, Visibility: VisibilityShared})
	if err != nil {
		slog.WarnContext(ctx, "record a triggered thread", "agent", ag.ID, "err", err)
	} else {
		s.announce(ctx, sent)
	}
	return nil
}

// StopThread aborts a thread's reply in progress (a triggered run past a
// limit). A hibernated agent has nothing in progress: it is not woken.
func (s *Service) StopThread(ctx context.Context, agentID uuid.UUID, threadID string) error {
	if !s.host.Running(agentID.String()) {
		return nil
	}
	ag, err := s.load(ctx, s.q, agentID)
	if err != nil {
		return err
	}
	spec, err := s.spec(ctx, ag)
	if err != nil {
		return err
	}
	status, _, err := s.host.Call(ctx, spec, http.MethodPost, "/threads/"+threadID+"/stop", nil)
	if err == nil && status >= 500 {
		err = errors.New("the runtime refused to stop the thread")
	}
	return err
}

// ThreadExcerpt is the start (at most max characters) of the thread's last
// assistant message ("" when there is none), read from the agent's runtime,
// for a run's completion callback. It never returns more of the transcript.
func (s *Service) ThreadExcerpt(ctx context.Context, agentID uuid.UUID, threadID string, max int) (string, error) {
	ag, err := s.load(ctx, s.q, agentID)
	if err != nil {
		return "", err
	}
	if !ag.Enabled {
		return "", errDisabled
	}
	spec, err := s.spec(ctx, ag)
	if err != nil {
		return "", err
	}
	status, raw, err := s.host.Call(ctx, spec, http.MethodGet, "/threads/"+threadID+"?limit=20", nil)
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		return "", errors.New("the runtime did not return the thread")
	}
	var view struct {
		Data struct {
			Messages []struct {
				Role string `json:"role"`
				Text string `json:"text"`
			} `json:"messages"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &view); err != nil {
		return "", err
	}
	for i := len(view.Data.Messages) - 1; i >= 0; i-- {
		msg := view.Data.Messages[i]
		if msg.Role != "assistant" || strings.TrimSpace(msg.Text) == "" {
			continue
		}
		text := strings.TrimSpace(msg.Text)
		if r := []rune(text); len(r) > max {
			text = string(r[:max-1]) + "…"
		}
		return text, nil
	}
	return "", nil
}

// TeamSlug is the slug of the agent's team (a run's console link).
func (s *Service) TeamSlug(ctx context.Context, agentID uuid.UUID) (string, error) {
	return s.q.AgentTeamSlug(ctx, agentID)
}
