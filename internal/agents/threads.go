package agents

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ncecere/pi-durable-platform/internal/agenthost/launcher"
	"github.com/ncecere/pi-durable-platform/internal/apperr"
	"github.com/ncecere/pi-durable-platform/internal/audit"
	"github.com/ncecere/pi-durable-platform/internal/authz"
	"github.com/ncecere/pi-durable-platform/internal/store"
	"github.com/ncecere/pi-durable-platform/internal/store/dbgen"
	"github.com/ncecere/pi-durable-platform/internal/teams"
)

// The platform's record of each agent conversation (agent_threads): read
// without waking the agent, updated when the platform relays a message and
// when the agent's runtime reports a reply ending, and announced on
// PostgreSQL channel pdp_threads for event streams (every replica hears it).
//
// A thread is shared (everyone who may use the agent sees it) or private
// (only the person or service account that started it). The record is
// written before the first message is relayed (ClaimThread), so a private
// thread is never briefly shared; a thread the runtime has but the platform
// has no record of is shared. Others' private threads read as not found on
// every route.

var (
	threadIDRE         = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	errThreadNotFound  = apperr.NotFound("thread_not_found", "Thread not found")
	errBadTitle        = apperr.Invalid("invalid_title", "Title must be 1-200 characters on one line")
	errBadVisibility   = apperr.Invalid("invalid_visibility", "visibility must be shared or private")
	errVisibilityFixed = apperr.Conflict("visibility_fixed", "A thread's visibility is chosen when it starts and can't change")
	errBadReason       = apperr.Invalid("invalid_reason", "Give a reason of 10-500 characters")
	errAdminOnly       = apperr.Forbidden("Only platform admins can open conversations, with a reason")
)

// Thread visibilities.
const (
	VisibilityShared  = "shared"
	VisibilityPrivate = "private"
)

// Principal kinds: who started or sent to a thread.
const (
	PrincipalUser    = "user"
	PrincipalService = "service_account"
)

// Thread is one conversation's record.
type Thread = dbgen.AgentThread

// Principal is a person or service account, with the name threads show.
type Principal struct {
	Kind string
	ID   uuid.UUID
	Name string
}

// Participant has sent to a thread, last at LastSentAt.
type Participant struct {
	Principal
	LastSentAt time.Time
}

// ThreadView is a thread's record with who started it (nil: unknown, such
// as threads begun before the record, or a deleted account) and who has
// sent to it. TitleHidden: the title was withheld (platform admins' list).
type ThreadView struct {
	Thread
	CreatedBy    *Principal
	Participants []Participant
	TitleHidden  bool
}

// owns reports whether the actor started the thread.
func owns(a authz.Actor, t Thread) bool {
	if a.Service != nil {
		return t.CreatedByService.Valid && t.CreatedByService.UUID == a.Service.AccountID
	}
	return a.UserID != uuid.Nil && t.CreatedByUser.Valid && t.CreatedByUser.UUID == a.UserID
}

// canSee reports whether the actor (who may use the agent) may see the thread.
func canSee(a authz.Actor, t Thread) bool { return t.Visibility != VisibilityPrivate || owns(a, t) }

// listable reports whether the thread appears in the actor's lists and event
// streams: one they canSee, and not another's reserved thread (started
// without a message yet; anyone may still reach it by its ID).
func listable(a authz.Actor, t Thread) bool { return canSee(a, t) && (!t.Reserved || owns(a, t)) }

// creator is the actor as a thread's creator (or participant).
func creator(a authz.Actor) (user, service uuid.NullUUID) {
	if a.Service != nil {
		return uuid.NullUUID{}, uuid.NullUUID{UUID: a.Service.AccountID, Valid: true}
	}
	return uuid.NullUUID{UUID: a.UserID, Valid: a.UserID != uuid.Nil}, uuid.NullUUID{}
}

// views adds creators and participants, with their names, to threads of
// one agent.
func (s *Service) views(ctx context.Context, agentID uuid.UUID, rows []Thread) ([]ThreadView, error) {
	out := make([]ThreadView, len(rows))
	if len(rows) == 0 {
		return out, nil
	}
	ids := make([]string, len(rows))
	for i, t := range rows {
		ids[i] = t.ThreadID
	}
	parts, err := s.q.ListThreadParticipants(ctx, dbgen.ListThreadParticipantsParams{AgentID: agentID, ThreadIds: ids})
	if err != nil {
		return nil, err
	}
	users, services := map[uuid.UUID]string{}, map[uuid.UUID]string{}
	note := func(u, sa uuid.NullUUID) {
		if u.Valid {
			users[u.UUID] = ""
		}
		if sa.Valid {
			services[sa.UUID] = ""
		}
	}
	for _, t := range rows {
		note(t.CreatedByUser, t.CreatedByService)
	}
	for _, p := range parts {
		note(p.UserID, p.ServiceAccountID)
	}
	if len(users) > 0 {
		names, err := s.q.UserNames(ctx, keys(users))
		if err != nil {
			return nil, err
		}
		for _, n := range names {
			users[n.ID] = n.Name
		}
	}
	if len(services) > 0 {
		names, err := s.q.ServiceAccountNames(ctx, keys(services))
		if err != nil {
			return nil, err
		}
		for _, n := range names {
			services[n.ID] = n.Name
		}
	}
	principal := func(u, sa uuid.NullUUID) *Principal {
		switch {
		case u.Valid:
			return &Principal{Kind: PrincipalUser, ID: u.UUID, Name: users[u.UUID]}
		case sa.Valid:
			return &Principal{Kind: PrincipalService, ID: sa.UUID, Name: services[sa.UUID]}
		}
		return nil
	}
	by := map[string][]Participant{}
	for _, p := range parts {
		if pr := principal(p.UserID, p.ServiceAccountID); pr != nil {
			by[p.ThreadID] = append(by[p.ThreadID], Participant{Principal: *pr, LastSentAt: p.LastSentAt})
		}
	}
	for i, t := range rows {
		out[i] = ThreadView{Thread: t, CreatedBy: principal(t.CreatedByUser, t.CreatedByService), Participants: by[t.ThreadID]}
		if out[i].Participants == nil {
			out[i].Participants = []Participant{}
		}
	}
	return out, nil
}

func keys(m map[uuid.UUID]string) []uuid.UUID {
	out := make([]uuid.UUID, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func (s *Service) view1(ctx context.Context, t Thread) (ThreadView, error) {
	v, err := s.views(ctx, t.AgentID, []Thread{t})
	if err != nil {
		return ThreadView{}, err
	}
	return v[0], nil
}

// thread is the agent's record of a thread the actor may see; found is
// false when there is none (a thread the runtime may have: shared).
// Another's private thread is not found.
func (s *Service) thread(ctx context.Context, q *dbgen.Queries, a authz.Actor, agentID uuid.UUID, threadID string) (Thread, bool, error) {
	t, err := q.GetAgentThread(ctx, dbgen.GetAgentThreadParams{AgentID: agentID, ThreadID: threadID})
	if errors.Is(store.NotFound(err), store.ErrNotFound) {
		return Thread{}, false, nil
	} else if err != nil {
		return Thread{}, false, err
	}
	if !canSee(a, t) {
		return Thread{}, false, errThreadNotFound
	}
	return t, true, nil
}

// ThreadAccess checks that the actor, who may use the agent, may reach the
// thread: it is shared, theirs, or not recorded. Call it before relaying.
func (s *Service) ThreadAccess(ctx context.Context, a authz.Actor, agentID uuid.UUID, threadID string) error {
	_, _, err := s.thread(ctx, s.q, a, agentID, threadID)
	return err
}

// Author is who sent a message, as the platform tells the agent's runtime
// (never as the client says). Via: the app a person sent it through.
type Author struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	Name string `json:"name"`
	Via  string `json:"via,omitempty"`
}

// Claim is a thread a message is about to be relayed to. Created: this
// request wrote its record (see Unclaim). AnnounceAuthor: the agent tells
// its model who is speaking (shared threads of shared teams' agents).
type Claim struct {
	Thread         Thread
	Created        bool
	Author         Author
	AnnounceAuthor bool
}

// ClaimThread records a thread before its first message is relayed (the
// actor is its creator; visibility "" is shared), or checks the actor may
// send to an existing one: another's private thread is not found, and a
// different visibility is refused. Call it after Spec (which authorizes the
// agent); at is when the platform received the message.
func (s *Service) ClaimThread(ctx context.Context, a authz.Actor, agentID uuid.UUID, threadID, visibility, text string, at time.Time) (Claim, error) {
	if err := checkVisibility(visibility); err != nil {
		return Claim{}, err
	}
	ag, err := s.load(ctx, s.q, agentID)
	if err != nil {
		return Claim{}, err
	}
	team, err := s.q.GetTeamByID(ctx, ag.TeamID)
	if err != nil {
		return Claim{}, err
	}
	var c Claim
	c.Thread, c.Created, err = s.claim(ctx, a, agentID, threadID, visibility, TitleFrom(text), false, at)
	if err != nil {
		return Claim{}, err
	}
	if !c.Created && c.Thread.Reserved && !owns(a, c.Thread) {
		if c.Thread, err = s.takeOverReserved(ctx, a, c.Thread, TitleFrom(text)); err != nil {
			return Claim{}, err
		}
	}
	if c.Author, err = s.author(ctx, a); err != nil {
		s.Unclaim(ctx, c)
		return Claim{}, err
	}
	c.AnnounceAuthor = c.Thread.Visibility == VisibilityShared && team.Kind == teams.KindTeam
	return c, nil
}

// takeOverReserved handles the first message to a reserved thread that
// someone other than its creator sends (a client-chosen ID the creator set MCP
// servers on before anyone had sent). The servers the creator set were not the
// sender's choice and the thread is hidden from them (reserved threads are
// listed for their creator only), so they never become active for the
// sender's turn: they are dropped, and the reservation ends, so the thread is
// listed like any other. Both happen under a lock on the thread's record, so
// a write of servers racing with it is either deleted here or made after, on a
// thread that is no longer hidden. The message is not refused: the sender
// joins the thread as for any shared thread (a private one was refused as not
// found before this point).
func (s *Service) takeOverReserved(ctx context.Context, a authz.Actor, t Thread, title string) (Thread, error) {
	out := t
	var dropped bool
	err := store.InTx(ctx, s.pool, func(q *dbgen.Queries, _ pgx.Tx) error {
		cur, err := q.LockAgentThread(ctx, dbgen.LockAgentThreadParams{AgentID: t.AgentID, ThreadID: t.ThreadID})
		if errors.Is(store.NotFound(err), store.ErrNotFound) {
			return nil // deleted meanwhile: the message starts it as a shared placeholder, as before
		} else if err != nil {
			return err
		}
		out = cur
		if !cur.Reserved || owns(a, cur) {
			return nil
		}
		servers, err := q.ListThreadMCPServers(ctx, dbgen.ListThreadMCPServersParams{AgentID: t.AgentID, ThreadID: &t.ThreadID})
		if err != nil {
			return err
		}
		dropped = len(servers) > 0
		if err := q.DeleteThreadMCPServers(ctx, dbgen.DeleteThreadMCPServersParams{AgentID: t.AgentID, ThreadID: t.ThreadID}); err != nil {
			return err
		}
		out, err = q.EndThreadReservation(ctx, dbgen.EndThreadReservationParams{AgentID: t.AgentID, ThreadID: t.ThreadID, Title: title})
		return err
	})
	if err != nil {
		return t, err
	}
	if !out.Reserved && t.Reserved {
		if dropped {
			slog.Info("a reserved thread's first message came from someone else: the MCP servers set on it were dropped",
				"agent", t.AgentID, "thread", t.ThreadID)
		}
		s.announce(ctx, out)
	}
	return out, nil
}

func checkVisibility(v string) error {
	switch v {
	case "", VisibilityShared, VisibilityPrivate:
		return nil
	}
	return errBadVisibility
}

// claim writes the record of a thread that has none, with the actor as its
// creator (created), or finds the existing one: another's private thread is
// not found and a visibility that differs from the thread's is refused.
// reserved: the thread starts without a message (see Thread.Reserved). A
// record deleted between the two steps is written again; with the thread
// still missing at the end (the message path only) the thread is a shared
// placeholder and the runtime decides.
func (s *Service) claim(ctx context.Context, a authz.Actor, agentID uuid.UUID, threadID, visibility, title string, reserved bool, at time.Time) (Thread, bool, error) {
	vis := visibility
	if vis == "" {
		vis = VisibilityShared
	}
	user, service := creator(a)
	for attempt := 0; ; attempt++ {
		t, err := s.q.ClaimAgentThread(ctx, dbgen.ClaimAgentThreadParams{AgentID: agentID, ThreadID: threadID, Title: title,
			CreatedByUser: user, CreatedByService: service, At: at, Visibility: vis, Reserved: reserved})
		if err == nil {
			return t, true, nil
		} else if !errors.Is(store.NotFound(err), store.ErrNotFound) {
			return Thread{}, false, err
		}
		t, found, err := s.thread(ctx, s.q, a, agentID, threadID)
		if err != nil {
			return Thread{}, false, err
		}
		if !found { // deleted meanwhile
			if attempt < 2 {
				continue
			}
			t = Thread{AgentID: agentID, ThreadID: threadID, Visibility: VisibilityShared}
		}
		if visibility != "" && visibility != t.Visibility {
			return Thread{}, false, errVisibilityFixed
		}
		return t, false, nil
	}
}

// newThreadID is a platform-chosen thread ID: 128 random bits.
func newThreadID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err) // the platform cannot choose safely without randomness
	}
	return "t_" + base64.RawURLEncoding.EncodeToString(b)
}

// DefaultTitle is a new thread's title until its first message names it.
const DefaultTitle = "New chat"

// CreateThread starts a conversation under a platform-chosen, unguessable
// ID (the actor is its creator; visibility "" is shared; title "" is
// DefaultTitle). It is authorized like sending a message: anyone who may use
// the agent, while it is turned on. The thread is reserved until its first
// message: listed for its creator only, and removed after 30 days unused.
func (s *Service) CreateThread(ctx context.Context, a authz.Actor, agentID uuid.UUID, visibility, title string) (ThreadView, error) {
	ag, err := s.usable(ctx, a, agentID)
	if err != nil {
		return ThreadView{}, err
	}
	if !ag.Enabled {
		return ThreadView{}, errDisabled
	}
	if err := checkVisibility(visibility); err != nil {
		return ThreadView{}, err
	}
	if title = strings.TrimSpace(title); title == "" {
		title = DefaultTitle
	} else if title, err = checkTitle(title); err != nil {
		return ThreadView{}, err
	}
	for {
		t, created, err := s.claim(ctx, a, agentID, newThreadID(), visibility, title, true, time.Now())
		if err != nil {
			return ThreadView{}, err
		}
		if !created { // a collision of 128-bit IDs: not in this universe
			continue
		}
		s.announce(ctx, t)
		return s.view1(ctx, t)
	}
}

// author is the actor as a message's author.
func (s *Service) author(ctx context.Context, a authz.Actor) (Author, error) {
	if a.Service != nil {
		return Author{Kind: PrincipalService, ID: a.Service.AccountID.String(), Name: a.Service.Name}, nil
	}
	out := Author{Kind: PrincipalUser, ID: a.UserID.String()}
	names, err := s.q.UserNames(ctx, []uuid.UUID{a.UserID})
	if err != nil {
		return out, err
	}
	if len(names) == 1 {
		out.Name = names[0].Name
	}
	if a.OAuthGrantID != uuid.Nil {
		out.Via = a.OAuthClientName
	}
	return out, nil
}

// Unclaim removes the record ClaimThread wrote when the runtime refused the
// first message, unless the runtime has reported the thread since.
func (s *Service) Unclaim(ctx context.Context, c Claim) {
	if !c.Created {
		return
	}
	ctx = context.WithoutCancel(ctx)
	n, err := s.q.UnclaimAgentThread(ctx, dbgen.UnclaimAgentThreadParams{AgentID: c.Thread.AgentID, ThreadID: c.Thread.ThreadID, At: c.Thread.StatusAt})
	if err != nil {
		slog.Warn("unclaim thread", "agent", c.Thread.AgentID, "err", err)
	} else if n > 0 {
		s.announce(ctx, c.Thread)
	}
}

// TitleFrom is a thread's title from its first message: its first message's first line.
func TitleFrom(text string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(text), "\n")
	line = strings.TrimSpace(line)
	if line == "" {
		return DefaultTitle
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

// Threads lists an agent's conversations the actor may see (shared ones and
// their own private ones), most recently used first, without waking it.
func (s *Service) Threads(ctx context.Context, a authz.Actor, id uuid.UUID) ([]ThreadView, error) {
	if _, err := s.usable(ctx, a, id); err != nil {
		return nil, err
	}
	user, service := creator(a)
	rows, err := s.q.ListVisibleAgentThreads(ctx, dbgen.ListVisibleAgentThreadsParams{AgentID: id, ViewerUser: user, ViewerService: service})
	if err != nil {
		return nil, err
	}
	return s.views(ctx, id, rows)
}

// RenameThread sets a thread's title.
func (s *Service) RenameThread(ctx context.Context, a authz.Actor, id uuid.UUID, threadID, title string) (ThreadView, error) {
	if _, err := s.usable(ctx, a, id); err != nil {
		return ThreadView{}, err
	}
	if _, found, err := s.thread(ctx, s.q, a, id, threadID); err != nil {
		return ThreadView{}, err
	} else if !found {
		return ThreadView{}, errThreadNotFound
	}
	title, err := checkTitle(title)
	if err != nil {
		return ThreadView{}, err
	}
	t, err := s.q.RenameAgentThread(ctx, dbgen.RenameAgentThreadParams{AgentID: id, ThreadID: threadID, Title: title})
	if errors.Is(store.NotFound(err), store.ErrNotFound) {
		return ThreadView{}, errThreadNotFound
	} else if err != nil {
		return ThreadView{}, err
	}
	s.announce(ctx, t)
	return s.view1(ctx, t)
}

// checkTitle trims a title chosen by a client and checks it.
func checkTitle(title string) (string, error) {
	title = strings.TrimSpace(title)
	if n := utf8.RuneCountInString(title); n < 1 || n > 200 || strings.ContainsAny(title, "\r\n\x00") {
		return "", errBadTitle
	}
	return title, nil
}

// ThreadSent records that a message was relayed to a claimed thread at at
// (call it after the runtime accepted the message; at is when the platform
// received it), and that the actor has sent to it. It never fails the request: the
// runtime reports the thread anyway.
func (s *Service) ThreadSent(ctx context.Context, a authz.Actor, c Claim, text string, at time.Time) {
	ctx = context.WithoutCancel(ctx)
	id, threadID := c.Thread.AgentID, c.Thread.ThreadID
	p := dbgen.ThreadSentParams{AgentID: id, ThreadID: threadID, Title: TitleFrom(text), At: at, Visibility: c.Thread.Visibility}
	p.CreatedByUser, p.CreatedByService = creator(a)
	t, err := s.q.ThreadSent(ctx, p)
	if err != nil {
		slog.Warn("record thread", "agent", id, "err", err)
		return
	}
	if p.CreatedByService.Valid {
		err = s.q.ThreadParticipantService(ctx, dbgen.ThreadParticipantServiceParams{AgentID: id, ThreadID: threadID,
			ServiceAccountID: p.CreatedByService, At: at})
	} else if p.CreatedByUser.Valid {
		err = s.q.ThreadParticipantUser(ctx, dbgen.ThreadParticipantUserParams{AgentID: id, ThreadID: threadID, UserID: p.CreatedByUser, At: at})
	}
	if err != nil {
		slog.Warn("record thread participant", "agent", id, "err", err)
	}
	s.announce(ctx, t)
}

// ThreadRemoved forgets a deleted thread.
func (s *Service) ThreadRemoved(ctx context.Context, id uuid.UUID, threadID string) {
	ctx = context.WithoutCancel(ctx)
	rows, err := s.q.DeleteAgentThread(ctx, dbgen.DeleteAgentThreadParams{AgentID: id, ThreadID: threadID})
	if err != nil {
		slog.Warn("forget thread", "agent", id, "err", err)
		return
	}
	t := Thread{AgentID: id, ThreadID: threadID}
	if len(rows) == 1 {
		t = rows[0]
	}
	s.announce(ctx, t)
}

// Cleanup deletes threads the platform created that nobody sent to in 30
// days (reserved: see CreateThread) and client MCP servers expired for more
// than a day. Deletes are idempotent, so every replica may run it.
func (s *Service) Cleanup(ctx context.Context) (int64, error) {
	threads, err := s.q.DeleteStaleReservedThreads(ctx)
	if err != nil {
		return 0, err
	}
	servers, err := s.mcp.PruneExpired(ctx)
	return threads + servers, err
}

// AdminThreads lists every thread of any agent, private ones too, for
// platform admins. The titles of private threads, of every thread of a
// personal workspace's agent, and of every thread of a service-managed agent
// (an app's agent for one of its users) are withheld (TitleHidden): a title
// is content, which they read only with a reason (AdminRead).
func (s *Service) AdminThreads(ctx context.Context, a authz.Actor, agentID uuid.UUID) ([]ThreadView, error) {
	if !a.IsPlatformAdmin() {
		return nil, errAdminOnly
	}
	ag, err := s.load(ctx, s.q, agentID)
	if err != nil {
		return nil, err
	}
	team, err := s.q.GetTeamByID(ctx, ag.TeamID)
	if err != nil {
		return nil, err
	}
	rows, err := s.q.ListAgentThreads(ctx, agentID)
	if err != nil {
		return nil, err
	}
	out, err := s.views(ctx, agentID, rows)
	if err != nil {
		return nil, err
	}
	for i := range out {
		if team.Kind == teams.KindPersonal || ag.ExternalKey != nil || out[i].Visibility == VisibilityPrivate {
			out[i].Title, out[i].TitleHidden = "", true
		}
	}
	return out, nil
}

// AdminRead is a platform admin's reasoned read of a conversation: the
// agent to relay to, and the audit entry to record before the conversation
// is returned (RecordAdminRead).
type AdminRead struct {
	Spec  launcher.Spec
	Entry audit.Entry
}

// AdminRead prepares a platform admin's read of any thread (private ones,
// and those of personal workspaces' agents, are read only this way), with
// a reason of 10-500 characters. The entry goes to the agent's team's audit
// log, where its owners see it.
func (s *Service) AdminRead(ctx context.Context, a authz.Actor, agentID uuid.UUID, threadID, reason string) (AdminRead, error) {
	if !a.IsPlatformAdmin() {
		return AdminRead{}, errAdminOnly
	}
	reason = strings.TrimSpace(reason)
	if n := utf8.RuneCountInString(reason); n < 10 || n > 500 {
		return AdminRead{}, errBadReason
	}
	ag, err := s.load(ctx, s.q, agentID)
	if err != nil {
		return AdminRead{}, err
	}
	if !ag.Enabled {
		return AdminRead{}, errDisabled
	}
	team, err := s.q.GetTeamByID(ctx, ag.TeamID)
	if err != nil {
		return AdminRead{}, err
	}
	visibility := VisibilityShared
	t, err := s.q.GetAgentThread(ctx, dbgen.GetAgentThreadParams{AgentID: agentID, ThreadID: threadID})
	if err == nil {
		visibility = t.Visibility
	} else if !errors.Is(store.NotFound(err), store.ErrNotFound) {
		return AdminRead{}, err
	}
	if err := s.checkRunning(ctx, ag); err != nil {
		return AdminRead{}, err
	}
	spec, err := s.spec(ctx, ag)
	if err != nil {
		return AdminRead{}, err
	}
	e := a.Audit("thread.admin_read", "agent_thread", agentID.String()+"/"+threadID)
	e.TeamID = ag.TeamID
	if e.Metadata == nil {
		e.Metadata = map[string]any{}
	}
	e.Metadata["reason"], e.Metadata["visibility"], e.Metadata["personal"] = reason, visibility, team.Kind == teams.KindPersonal
	return AdminRead{Spec: spec, Entry: e}, nil
}

// RecordAdminRead records an admin read's audit entry.
func (s *Service) RecordAdminRead(ctx context.Context, r AdminRead) error {
	return audit.Record(ctx, s.q, r.Entry)
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
		r.Title = DefaultTitle
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

// ThreadEvent is one change: the thread as it is now, or Deleted (then
// Thread has its ID, and its visibility and creator when it was private).
type ThreadEvent struct {
	AgentID uuid.UUID
	Thread  ThreadView
	Deleted bool
}

// threadNote is a change's notification. A private thread's carries its
// creator (Private, and User or Service), so its deletion reaches only them.
type threadNote struct {
	Agent   string `json:"a"`
	Thread  string `json:"t,omitempty"`
	Private bool   `json:"p,omitempty"`
	User    string `json:"u,omitempty"`
	Service string `json:"s,omitempty"`
}

// announce tells every replica's event streams about a change (best effort).
func (s *Service) announce(ctx context.Context, t Thread) {
	note := threadNote{Agent: t.AgentID.String(), Thread: t.ThreadID}
	if t.Visibility == VisibilityPrivate {
		note.Private = true
		if t.CreatedByUser.Valid {
			note.User = t.CreatedByUser.UUID.String()
		}
		if t.CreatedByService.Valid {
			note.Service = t.CreatedByService.UUID.String()
		}
	}
	b, _ := json.Marshal(note)
	if err := s.q.NotifyThreads(context.WithoutCancel(ctx), string(b)); err != nil {
		slog.Warn("notify thread change", "err", err)
	}
}

// threadHub fans thread changes out to this replica's event streams.
type threadHub struct {
	mu   sync.Mutex
	subs map[chan ThreadEvent]struct{}
	// recheck is how often an open stream checks its person's access again
	// (0: DefaultStreamRecheck).
	recheck time.Duration
}

// DefaultStreamRecheck is how often an open event stream checks again that
// its person may still read it.
const DefaultStreamRecheck = 30 * time.Second

// SetStreamRecheck sets how often an open event stream checks its person's
// access again (non-positive: DefaultStreamRecheck). Call it before streams
// open.
func (s *Service) SetStreamRecheck(d time.Duration) { s.hub.recheck = d }

func (s *Service) streamRecheck() time.Duration {
	if s.hub.recheck > 0 {
		return s.hub.recheck
	}
	return DefaultStreamRecheck
}

// streamAllowed re-checks a stream's access: the person is still active (not
// suspended, not denied by the group re-check; gone counts as not) and, for a
// stream of one agent, may still use it (team membership and role). A check
// that fails for any other reason (the database) keeps the stream: the next
// tick asks again. Service accounts are checked for the agent only (their
// role and team are those of their key at connect time).
func (s *Service) streamAllowed(ctx context.Context, a authz.Actor, only *uuid.UUID) bool {
	if a.Service == nil && a.UserID != uuid.Nil {
		u, err := s.q.GetUser(ctx, a.UserID)
		if errors.Is(store.NotFound(err), store.ErrNotFound) {
			return false
		} else if err == nil && !store.IsUserActive(u) {
			return false
		}
	}
	if only != nil {
		if _, err := s.usable(ctx, a, *only); err != nil {
			if ae, ok := apperr.As(err); ok && ae.Status < 500 {
				return false
			}
		}
	}
	return true
}

// WatchStreamAccess runs until ctx ends, calling stop (once) when the actor
// may no longer follow a thread's event stream: they are no longer active
// (suspended, or denied by the group re-check), may no longer use the agent
// (removed from its team, a lower role, the agent gone), or the thread is
// another's private one. It checks every StreamRecheck; use it for streams the
// platform only relays (a thread's events come from the agent's runtime).
func (s *Service) WatchStreamAccess(ctx context.Context, a authz.Actor, agentID uuid.UUID, threadID string, stop func()) {
	tick := time.NewTicker(s.streamRecheck())
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		ok := s.streamAllowed(ctx, a, &agentID)
		if ok {
			if err := s.ThreadAccess(ctx, a, agentID, threadID); err != nil {
				if ae, isApp := apperr.As(err); isApp && ae.Status < 500 {
					ok = false
				}
			}
		}
		if !ok {
			stop()
			return
		}
	}
}

// SubscribeThreads returns changes to threads of agents the actor may use (or
// one of them, with only), until ctx ends: shared threads and the actor's
// own private ones. Access is decided when the stream opens and checked
// again every StreamRecheck (SetStreamRecheck): the channel is closed when the
// person is no longer active or, for a stream of one agent, may no longer use
// it; a stream of all their agents stops carrying an agent's threads when they
// can no longer use it.
func (s *Service) SubscribeThreads(ctx context.Context, a authz.Actor, only *uuid.UUID) <-chan ThreadEvent {
	raw := make(chan ThreadEvent, 64)
	s.hub.mu.Lock()
	s.hub.subs[raw] = struct{}{}
	s.hub.mu.Unlock()
	s.Metrics.ThreadSubscribers(1)
	out := make(chan ThreadEvent, 64)
	go func() {
		defer close(out)
		defer func() {
			s.hub.mu.Lock()
			delete(s.hub.subs, raw)
			s.hub.mu.Unlock()
			s.Metrics.ThreadSubscribers(-1)
		}()
		allowed := map[uuid.UUID]bool{} // per agent, decided again after every tick
		tick := time.NewTicker(s.streamRecheck())
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				if !s.streamAllowed(ctx, a, only) {
					return
				}
				clear(allowed)
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
				if !ok || !listable(a, ev.Thread.Thread) {
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
		done := s.Metrics.Loop("thread_events")
		if err := s.listenThreads(ctx); err != nil && ctx.Err() == nil {
			done(err) // a listener's run ends only by failing
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
			gone := Thread{AgentID: agentID, ThreadID: note.Thread, Visibility: VisibilityShared}
			if note.Private {
				gone.Visibility = VisibilityPrivate
				if id, err := uuid.Parse(note.User); err == nil {
					gone.CreatedByUser = uuid.NullUUID{UUID: id, Valid: true}
				}
				if id, err := uuid.Parse(note.Service); err == nil {
					gone.CreatedByService = uuid.NullUUID{UUID: id, Valid: true}
				}
			}
			s.publish(ThreadEvent{AgentID: agentID, Thread: ThreadView{Thread: gone}, Deleted: true})
			continue
		} else if err != nil {
			return err
		}
		v, err := s.view1(ctx, t)
		if err != nil {
			return err
		}
		s.publish(ThreadEvent{AgentID: agentID, Thread: v})
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
