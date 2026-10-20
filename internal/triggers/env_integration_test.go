package triggers_test

import (
	"context"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ncecere/pi-durable-platform/internal/agents"
	"github.com/ncecere/pi-durable-platform/internal/authz"
	"github.com/ncecere/pi-durable-platform/internal/config"
	"github.com/ncecere/pi-durable-platform/internal/secrets"
	"github.com/ncecere/pi-durable-platform/internal/store"
	"github.com/ncecere/pi-durable-platform/internal/testutil"
	"github.com/ncecere/pi-durable-platform/internal/triggers"
)

// The trigger engine against a real database, with the agent replaced by a
// fake runner (the end-to-end path with real runtimes is in
// internal/httpapi/triggers_integration_test.go) and a clock of the test's.

const testKey = "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="

// clock is a settable clock.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) Set(t time.Time)     { c.mu.Lock(); c.t = t; c.mu.Unlock() }
func (c *clock) Add(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

// fakeRunner stands in for the agent: it records each message (as the
// platform would, the thread busy from the time it was sent), and fails
// when told to.
type fakeRunner struct {
	db *pgxpool.Pool // the test's own pool

	mu       sync.Mutex
	sent     []agents.TriggerMessage
	stopped  []string
	fail     error // returned by SendTriggered, once per failN
	failN    int
	excerpt  string
	accepted map[string]int // fire ID -> messages the "runtime" ran (pi-durable: once per requestId)
}

func (f *fakeRunner) SendTriggered(ctx context.Context, m agents.TriggerMessage) error {
	f.mu.Lock()
	f.sent = append(f.sent, m)
	if f.failN > 0 {
		f.failN--
		err := f.fail
		f.mu.Unlock()
		return err
	}
	if f.accepted == nil {
		f.accepted = map[string]int{}
	}
	first := f.accepted[m.RequestID] == 0
	f.accepted[m.RequestID]++
	f.mu.Unlock()
	if first {
		_, err := f.db.Exec(ctx, `INSERT INTO agent_threads (agent_id, thread_id, title, busy, status_at, created_at, updated_at)
			VALUES ($1, $2, $3, true, $4, $4, $4)
			ON CONFLICT (agent_id, thread_id) DO UPDATE SET busy = true, status_at = $4, last_error = NULL`,
			m.AgentID, m.ThreadID, m.TriggerName, m.At)
		return err
	}
	return nil
}

func (f *fakeRunner) StopThread(_ context.Context, _ uuid.UUID, threadID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopped = append(f.stopped, threadID)
	return nil
}

func (f *fakeRunner) ThreadExcerpt(context.Context, uuid.UUID, string, int) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.excerpt, nil
}

func (f *fakeRunner) sends() []agents.TriggerMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]agents.TriggerMessage(nil), f.sent...)
}

func (f *fakeRunner) stops() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.stopped...)
}

type env struct {
	t      *testing.T
	db     *pgxpool.Pool // the test's own connections
	url    string
	clock  *clock
	runner *fakeRunner
	svc    *triggers.Service
	cfg    config.Triggers

	teamID, agentID         uuid.UUID
	owner, admin, editor    authz.Actor
	member, staff, outsider authz.Actor
	service                 authz.Actor
}

func newEnv(t *testing.T) *env {
	t.Helper()
	appPool, url := testutil.NewDB(t)
	// The test's own connections, apart from the service's pool.
	pcfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	pcfg.MaxConns = 2
	db, err := pgxpool.NewWithConfig(context.Background(), pcfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	e := &env{t: t, db: db, url: url, clock: &clock{t: time.Date(2026, 10, 20, 10, 1, 0, 0, time.UTC)},
		cfg: config.Triggers{Interval: 10 * time.Second, MaxRunTime: 30 * time.Minute, MaxConcurrentRuns: 5, PauseAfterFailures: 3}}
	e.runner = &fakeRunner{db: db, excerpt: "All done."}
	e.svc = e.newService(appPool)
	e.teamID = uuid.New()
	e.exec(`INSERT INTO teams (id, slug, name) VALUES ($1, 'acme', 'Acme')`, e.teamID)
	person := func(role, platform string) authz.Actor {
		id := uuid.New()
		e.exec(`INSERT INTO users (id, oidc_issuer, oidc_subject, email, platform_role) VALUES ($1, 'test', $2, $3, $4)`,
			id, id.String(), id.String()+"@example.com", platform)
		if role != "" {
			e.exec(`INSERT INTO team_members (team_id, user_id, role) VALUES ($1, $2, $3)`, e.teamID, id, role)
		}
		return authz.Actor{UserID: id, PlatformRole: platform}
	}
	e.owner, e.admin, e.editor, e.member = person("owner", "none"), person("admin", "none"), person("editor", "none"), person("member", "none")
	e.staff, e.outsider = person("", "platform_auditor"), person("", "none")
	sa := uuid.New()
	e.exec(`INSERT INTO service_accounts (id, team_id, name, role) VALUES ($1, $2, 'bot', 'editor')`, sa, e.teamID)
	e.service = authz.Actor{Service: &authz.ServiceActor{AccountID: sa, Name: "bot", TeamID: e.teamID, Role: "editor"}}
	e.agentID = e.newAgent("helper")
	return e
}

// newService is one replica's trigger service, on its own pool.
func (e *env) newService(pool *pgxpool.Pool) *triggers.Service {
	key, _ := secrets.ParseKey(testKey)
	box, err := secrets.New(key)
	if err != nil {
		e.t.Fatal(err)
	}
	s := triggers.New(pool, box, e.runner, e.cfg, "https://pdp.example.com", testutil.Logger())
	s.Now = e.clock.Now
	s.SetLocker(store.Locks{Pool: pool})
	return s
}

// replica is another replica: its own pool on the same database.
func (e *env) replica() *triggers.Service {
	pcfg, err := pgxpool.ParseConfig(e.url)
	if err != nil {
		e.t.Fatal(err)
	}
	if n, err := strconv.Atoi(os.Getenv("PDP_TEST_POOL_MAX_CONNS")); err == nil {
		pcfg.MaxConns = int32(n) // as small as the first replica's
	}
	pool, err := pgxpool.NewWithConfig(context.Background(), pcfg)
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(pool.Close)
	return e.newService(pool)
}

func (e *env) newAgent(name string) uuid.UUID {
	id := uuid.New()
	e.exec(`INSERT INTO agents (id, team_id, template_id, template_version, name, db_password_enc, runtime_token_enc)
		SELECT $1, $2, id, 1, $3, '\x00', '\x00' FROM agent_templates WHERE slug = 'assistant'`, id, e.teamID, name)
	return id
}

func (e *env) exec(sql string, args ...any) {
	e.t.Helper()
	if _, err := e.db.Exec(context.Background(), sql, args...); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
}

func (e *env) scalar(sql string, args ...any) string {
	e.t.Helper()
	var out string
	if err := e.db.QueryRow(context.Background(), sql, args...).Scan(&out); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
	return out
}

func (e *env) tick(s *triggers.Service) {
	e.t.Helper()
	if err := s.Tick(context.Background()); err != nil {
		e.t.Fatalf("tick: %v", err)
	}
}

func ptr[T any](v T) *T { return &v }

func (e *env) schedule(name, cron, missed string) triggers.View {
	e.t.Helper()
	saved, err := e.svc.Create(context.Background(), e.owner, e.agentID, triggers.Input{Kind: triggers.KindSchedule, Name: &name,
		Cron: &cron, Timezone: ptr("UTC"), MissedPolicy: &missed, MessageTemplate: ptr("Report for {{fire.time}}")})
	if err != nil {
		e.t.Fatal(err)
	}
	return saved.View
}

func (e *env) webhook(name string, in triggers.Input) triggers.Saved {
	e.t.Helper()
	in.Kind, in.Name = triggers.KindWebhook, &name
	if in.MessageTemplate == nil {
		in.MessageTemplate = ptr("Issue: {{payload.title}}")
	}
	saved, err := e.svc.Create(context.Background(), e.owner, e.agentID, in)
	if err != nil {
		e.t.Fatal(err)
	}
	return saved
}

func hookID(v triggers.View) string { return v.WebhookURL[strings.LastIndex(v.WebhookURL, "/")+1:] }

// deliver sends a signed delivery to a webhook, now.
func (e *env) deliver(s *triggers.Service, saved triggers.Saved, body string) (triggers.WebhookResult, error) {
	sig := triggers.Sign(saved.WebhookSecret, e.clock.Now(), []byte(body))
	res, err := s.Webhook(context.Background(), hookID(saved.View), sig, []byte(body))
	s.Wait()
	return res, err
}

// runs are the agent's runs, newest first.
func (e *env) runs() []triggers.RunView {
	e.t.Helper()
	rows, err := e.svc.Runs(context.Background(), e.owner, e.agentID, triggers.RunPage{Limit: 100})
	if err != nil {
		e.t.Fatal(err)
	}
	return rows
}

func (e *env) run(id uuid.UUID) triggers.RunView {
	e.t.Helper()
	v, err := e.svc.Run(context.Background(), e.owner, e.agentID, id)
	if err != nil {
		e.t.Fatal(err)
	}
	return v
}

// replyEnds reports a run's thread's reply ending now (as the runtime does),
// with an error or not.
func (e *env) replyEnds(threadID string, failed bool) {
	var lastError *string
	if failed {
		lastError = ptr("model error")
	}
	e.exec(`UPDATE agent_threads SET busy = false, status_at = $3, last_reply_at = $3, last_error = $4 WHERE agent_id = $1 AND thread_id = $2`,
		e.agentID, threadID, e.clock.Now(), lastError)
}

// usage records model usage of the agent now: tokens of a model priced at
// $10 per million input tokens.
func (e *env) usage(inputTokens int) {
	model := e.priced()
	e.exec(`INSERT INTO model_usage (team_id, agent_id, model_id, source, input_tokens, created_at) VALUES ($1, $2, $3, 'proxy', $4, $5)`,
		e.teamID, e.agentID, model, inputTokens, e.clock.Now())
}

var pricedOnce sync.Map

func (e *env) priced() uuid.UUID {
	if v, ok := pricedOnce.Load(e); ok {
		return v.(uuid.UUID)
	}
	provider, model := uuid.New(), uuid.New()
	e.exec(`INSERT INTO model_providers (id, name, kind, base_url, api_key_enc) VALUES ($1, $2, 'openai', 'https://api.example.com', '\x00')`,
		provider, "p-"+provider.String()[:8])
	e.exec(`INSERT INTO models (id, provider_id, upstream_id, name, input_price) VALUES ($1, $2, 'm', 'Model', 10)`, model, provider)
	pricedOnce.Store(e, model)
	return model
}

func (e *env) audits(action string) []string {
	e.t.Helper()
	rows, err := e.db.Query(context.Background(), `SELECT metadata::text FROM audit_log WHERE action = $1 ORDER BY id`, action)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var m string
		_ = rows.Scan(&m)
		out = append(out, m)
	}
	return out
}

func wantStatus(t *testing.T, v triggers.RunView, status, reason string) {
	t.Helper()
	got := ""
	if v.Run.Reason != nil {
		got = *v.Run.Reason
	}
	if v.Run.Status != status || got != reason {
		t.Fatalf("run %s: status %s (%s), want %s (%s)", v.Run.FireID, v.Run.Status, got, status, reason)
	}
}

func isCode(err error, code string) bool {
	return err != nil && strings.Contains(err.Error(), code)
}

// setLimits sets the agent's trigger limits, as its owner.
func (e *env) setLimits(st triggers.Settings) {
	e.t.Helper()
	cur, err := e.svc.GetSettings(context.Background(), e.owner, e.agentID)
	if err != nil {
		e.t.Fatal(err)
	}
	if _, err := e.svc.SetSettings(context.Background(), e.owner, e.agentID, st, cur.Revision); err != nil {
		e.t.Fatal(err)
	}
}
