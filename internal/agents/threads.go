package agents

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/ncecere/pi-durable-platform/internal/apperr"
	"github.com/ncecere/pi-durable-platform/internal/authz"
	"github.com/ncecere/pi-durable-platform/internal/store"
	"github.com/ncecere/pi-durable-platform/internal/store/dbgen"
)

// The platform's record of each agent conversation (agent_threads): read
// without waking the agent, updated when the platform relays a message and
// when the agent's runtime reports a reply ending, and announced on
// PostgreSQL channel pdp_threads for event streams (every replica hears it).

var (
	threadIDRE        = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	errThreadNotFound = apperr.NotFound("thread_not_found", "Thread not found")
	errBadTitle       = apperr.Invalid("invalid_title", "Title must be 1-200 characters on one line")
)

// Thread is one conversation's record.
type Thread = dbgen.AgentThread

// TitleFrom is a thread's default title: its first message's first line.
func TitleFrom(text string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(text), "\n")
	line = strings.TrimSpace(line)
	if line == "" {
		return "New chat"
	}
	if r := []rune(line); len(r) > 60 {
		return string(r[:59]) + "…"
	}
	return line
}

// usable loads an agent the actor may use (converse with).
func (s *Service) usable(ctx context.Context, a authz.Actor, id uuid.UUID) (dbgen.Agent, error) {
	ag, err := s.load(ctx, s.q, id)
	if err != nil {
		return ag, err
	}
	sub, err := subject(ctx, s.q, a, ag)
	if err != nil {
		return ag, err
	}
	return ag, authz.CheckAgentUse(sub)
}

// Threads lists an agent's conversations, most recently used first, without
// waking it.
func (s *Service) Threads(ctx context.Context, a authz.Actor, id uuid.UUID) ([]Thread, error) {
	if _, err := s.usable(ctx, a, id); err != nil {
		return nil, err
	}
	return s.q.ListAgentThreads(ctx, id)
}

// RenameThread sets a thread's title.
func (s *Service) RenameThread(ctx context.Context, a authz.Actor, id uuid.UUID, threadID, title string) (Thread, error) {
	if _, err := s.usable(ctx, a, id); err != nil {
		return Thread{}, err
	}
	title = strings.TrimSpace(title)
	if n := utf8.RuneCountInString(title); n < 1 || n > 200 || strings.ContainsAny(title, "\r\n\x00") {
		return Thread{}, errBadTitle
	}
	t, err := s.q.RenameAgentThread(ctx, dbgen.RenameAgentThreadParams{AgentID: id, ThreadID: threadID, Title: title})
	if errors.Is(store.NotFound(err), store.ErrNotFound) {
		return t, errThreadNotFound
	} else if err != nil {
		return t, err
	}
	s.announce(ctx, t)
	return t, nil
}

// ThreadSent records that a message was relayed to a thread at at (call it
// after the runtime accepted the message; at is when the platform received
// it). It never fails the request: the runtime reports the thread anyway.
func (s *Service) ThreadSent(ctx context.Context, a authz.Actor, id uuid.UUID, threadID, text string, at time.Time) {
	p := dbgen.ThreadSentParams{AgentID: id, ThreadID: threadID, Title: TitleFrom(text), At: at}
	if a.Service != nil {
		p.CreatedByService = uuid.NullUUID{UUID: a.Service.AccountID, Valid: true}
	} else if a.UserID != uuid.Nil {
		p.CreatedByUser = uuid.NullUUID{UUID: a.UserID, Valid: true}
	}
	t, err := s.q.ThreadSent(context.WithoutCancel(ctx), p)
	if err != nil {
		slog.Warn("record thread", "agent", id, "err", err)
		return
	}
	s.announce(ctx, t)
}

// ThreadRemoved forgets a deleted thread.
func (s *Service) ThreadRemoved(ctx context.Context, id uuid.UUID, threadID string) {
	ctx = context.WithoutCancel(ctx)
	if err := s.q.DeleteAgentThread(ctx, dbgen.DeleteAgentThreadParams{AgentID: id, ThreadID: threadID}); err != nil {
		slog.Warn("forget thread", "agent", id, "err", err)
		return
	}
	s.announce(ctx, Thread{AgentID: id, ThreadID: threadID})
}

// ThreadReport is a thread's status as the runtime reports it.
type ThreadReport struct {
	ThreadID    string     `json:"threadId"`
	Title       string     `json:"title"`
	CreatedAt   int64      `json:"createdAt"`
	Busy        bool       `json:"busy"`
	LastReplyAt *int64     `json:"lastReplyAt"`
	LastError   *string    `json:"lastError"`
	At          int64      `json:"at"`
	createdAt   time.Time  `json:"-"`
	lastReply   *time.Time `json:"-"`
}

func (r *ThreadReport) check() bool {
	if !threadIDRE.MatchString(r.ThreadID) || r.At <= 0 {
		return false
	}
	r.Title = strings.TrimSpace(r.Title)
	if n := utf8.RuneCountInString(r.Title); n < 1 || n > 200 {
		r.Title = "New chat"
	}
	if r.LastError != nil && utf8.RuneCountInString(*r.LastError) > 2000 {
		e := string([]rune(*r.LastError)[:2000])
		r.LastError = &e
	}
	r.createdAt = time.UnixMilli(r.CreatedAt)
	if r.CreatedAt <= 0 {
		r.createdAt = time.UnixMilli(r.At)
	}
	if r.LastReplyAt != nil && *r.LastReplyAt > 0 {
		t := time.UnixMilli(*r.LastReplyAt)
		r.lastReply = &t
	}
	return true
}

func (s *Service) applyReport(ctx context.Context, agentID uuid.UUID, r ThreadReport) error {
	t, err := s.q.ThreadReported(ctx, dbgen.ThreadReportedParams{AgentID: agentID, ThreadID: r.ThreadID, Title: r.Title,
		CreatedAt: r.createdAt, Busy: r.Busy, LastReplyAt: r.lastReply, LastError: r.LastError, At: time.UnixMilli(r.At)})
	if err != nil {
		return err
	}
	s.announce(ctx, t)
	return nil
}

// ReportThread records one thread's status from the agent's runtime.
func (s *Service) ReportThread(ctx context.Context, agentID uuid.UUID, r ThreadReport) error {
	if !r.check() {
		return apperr.Invalid("invalid_thread", "threadId and at are required")
	}
	return s.applyReport(ctx, agentID, r)
}

// SyncThreads records every thread the runtime has (when it starts), and
// forgets threads recorded before at that it no longer has.
func (s *Service) SyncThreads(ctx context.Context, agentID uuid.UUID, threads []ThreadReport, at int64) error {
	keep := make([]string, 0, len(threads))
	for _, r := range threads {
		if !r.check() {
			continue
		}
		if err := s.applyReport(ctx, agentID, r); err != nil {
			return err
		}
		keep = append(keep, r.ThreadID)
	}
	if at <= 0 {
		return nil
	}
	n, err := s.q.DeleteAgentThreadsNotIn(ctx, dbgen.DeleteAgentThreadsNotInParams{AgentID: agentID, Before: time.UnixMilli(at), Keep: keep})
	if n > 0 {
		s.announce(ctx, Thread{AgentID: agentID})
	}
	return err
}

// ThreadEvent is one change: the thread as it is now, or Deleted.
type ThreadEvent struct {
	AgentID uuid.UUID
	Thread  Thread
	Deleted bool
}

type threadNote struct {
	Agent  string `json:"a"`
	Thread string `json:"t,omitempty"`
}

// announce tells every replica's event streams about a change (best effort).
func (s *Service) announce(ctx context.Context, t Thread) {
	b, _ := json.Marshal(threadNote{Agent: t.AgentID.String(), Thread: t.ThreadID})
	if err := s.q.NotifyThreads(context.WithoutCancel(ctx), string(b)); err != nil {
		slog.Warn("notify thread change", "err", err)
	}
}

// threadHub fans thread changes out to this replica's event streams.
type threadHub struct {
	mu   sync.Mutex
	subs map[chan ThreadEvent]struct{}
}

// SubscribeThreads returns changes to threads of agents the actor may use (or
// one of them, with only), until ctx ends.
func (s *Service) SubscribeThreads(ctx context.Context, a authz.Actor, only *uuid.UUID) <-chan ThreadEvent {
	raw := make(chan ThreadEvent, 64)
	s.hub.mu.Lock()
	s.hub.subs[raw] = struct{}{}
	s.hub.mu.Unlock()
	out := make(chan ThreadEvent, 64)
	go func() {
		defer close(out)
		defer func() {
			s.hub.mu.Lock()
			delete(s.hub.subs, raw)
			s.hub.mu.Unlock()
		}()
		allowed := map[uuid.UUID]bool{} // per agent, decided once per stream
		for {
			select {
			case <-ctx.Done():
				return
			case ev := <-raw:
				if only != nil && ev.AgentID != *only {
					continue
				}
				ok, seen := allowed[ev.AgentID]
				if !seen {
					_, err := s.usable(ctx, a, ev.AgentID)
					ok = err == nil
					allowed[ev.AgentID] = ok
				}
				if !ok {
					continue
				}
				select {
				case out <- ev:
				default: // a slow reader misses events, never blocks others
				}
			}
		}
	}()
	return out
}

func (s *Service) publish(ev ThreadEvent) {
	s.hub.mu.Lock()
	defer s.hub.mu.Unlock()
	for ch := range s.hub.subs {
		select {
		case ch <- ev:
		default:
		}
	}
}

// RunThreadEvents listens on pdp_threads and feeds this replica's event
// streams until ctx ends (reconnecting after failures).
func (s *Service) RunThreadEvents(ctx context.Context, log *slog.Logger) {
	for ctx.Err() == nil {
		if err := s.listenThreads(ctx); err != nil && ctx.Err() == nil {
			log.Warn("thread events: listener stopped; retrying", "err", err)
			select {
			case <-ctx.Done():
			case <-time.After(2 * time.Second):
			}
		}
	}
}

func (s *Service) listenThreads(ctx context.Context) error {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, "LISTEN pdp_threads"); err != nil {
		return err
	}
	defer func() { _, _ = conn.Exec(context.WithoutCancel(ctx), "UNLISTEN pdp_threads") }()
	for {
		n, err := conn.Conn().WaitForNotification(ctx)
		if err != nil {
			return err
		}
		var note threadNote
		if json.Unmarshal([]byte(n.Payload), &note) != nil {
			continue
		}
		agentID, err := uuid.Parse(note.Agent)
		if err != nil {
			continue
		}
		if note.Thread == "" {
			continue // a bulk change: nothing to show per thread
		}
		t, err := s.q.GetAgentThread(ctx, dbgen.GetAgentThreadParams{AgentID: agentID, ThreadID: note.Thread})
		if errors.Is(store.NotFound(err), store.ErrNotFound) {
			s.publish(ThreadEvent{AgentID: agentID, Thread: Thread{AgentID: agentID, ThreadID: note.Thread}, Deleted: true})
			continue
		} else if err != nil {
			return err
		}
		s.publish(ThreadEvent{AgentID: agentID, Thread: t})
	}
}

// TeamRunning counts a team's agents that are running now.
func (s *Service) TeamRunning(ctx context.Context, teamID uuid.UUID) (int64, int64, error) {
	ids, err := s.q.TeamAgentIDs(ctx, teamID)
	if err != nil {
		return 0, 0, err
	}
	running := s.host.Launcher().Running()
	n := int64(0)
	for _, id := range ids {
		if _, ok := running[id.String()]; ok {
			n++
		}
	}
	return int64(len(ids)), n, nil
}
