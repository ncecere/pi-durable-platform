package provision_test

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ncecere/pi-durable-platform/internal/agenthost/provision"
)

// env is a throwaway agents database owned by a CREATEROLE role that is not a
// superuser, as deployed. Needs PDP_TEST_DATABASE_URL (a superuser connection
// where it may create a database and a role).
type env struct {
	t        *testing.T
	adminURL string
	su       *pgx.Conn // superuser, for setup and cleanup
	db       string
	role     string
	password string
	roles    []string // agent-named roles to drop at the end
	dbs      []string // extra databases to drop at the end
}

func newEnv(t *testing.T) *env {
	t.Helper()
	adminURL := os.Getenv("PDP_TEST_DATABASE_URL")
	if adminURL == "" {
		t.Skip("PDP_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	suffix := fmt.Sprint(time.Now().UnixNano())
	e := &env{t: t, adminURL: adminURL, db: "pdp_prov_" + suffix, role: "pdp_prov_admin_" + suffix, password: "prov-test-" + suffix}
	su, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		t.Fatal(err)
	}
	e.su = su
	t.Cleanup(func() {
		c, err := pgx.Connect(context.Background(), adminURL)
		if err != nil {
			return
		}
		defer c.Close(context.Background())
		for _, db := range append([]string{e.db}, e.dbs...) {
			_, _ = c.Exec(context.Background(), "DROP DATABASE IF EXISTS "+db+" WITH (FORCE)")
		}
		for _, name := range e.roles {
			_, _ = c.Exec(context.Background(), "DROP ROLE IF EXISTS "+name)
		}
		_, _ = c.Exec(context.Background(), "DROP ROLE IF EXISTS "+e.role)
		su.Close(context.Background())
	})
	e.exec(fmt.Sprintf("CREATE ROLE %s LOGIN CREATEROLE PASSWORD '%s'", e.role, e.password))
	e.createDatabase(e.db)
	return e
}

func (e *env) exec(stmt string) {
	e.t.Helper()
	if _, err := e.su.Exec(context.Background(), stmt); err != nil {
		e.t.Fatalf("%s: %v", stmt, err)
	}
}

// dbExec runs stmt as the superuser in database db.
func (e *env) dbExec(db, stmt string) {
	e.t.Helper()
	u, _ := url.Parse(e.adminURL)
	u.Path = "/" + db
	c, err := pgx.Connect(context.Background(), u.String())
	if err != nil {
		e.t.Fatal(err)
	}
	defer c.Close(context.Background())
	if _, err := c.Exec(context.Background(), stmt); err != nil {
		e.t.Fatalf("%s: %v", stmt, err)
	}
}

func (e *env) createDatabase(db string) {
	e.t.Helper()
	e.exec(fmt.Sprintf("CREATE DATABASE %s OWNER %s", db, e.role))
}

// agentsURL is the provisioning role's URL for a database of this test.
func (e *env) url(db string) string {
	u, _ := url.Parse(e.adminURL)
	u.User, u.Path = url.UserPassword(e.role, e.password), "/"+db
	return u.String()
}

func (e *env) provisioner(ctx context.Context) (*provision.Provisioner, *pgxpool.Pool) {
	e.t.Helper()
	pool, err := pgxpool.New(ctx, e.url(e.db))
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(pool.Close)
	return provision.New(pool), pool
}

// track registers an agent's role for cleanup.
func (e *env) track(agentID string) string {
	name, err := provision.NameFor(agentID)
	if err != nil {
		e.t.Fatal(err)
	}
	e.roles = append(e.roles, name)
	return name
}

func (e *env) roleExists(name string) bool {
	e.t.Helper()
	var n int
	if err := e.su.QueryRow(context.Background(), "SELECT count(*) FROM pg_roles WHERE rolname = $1", name).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n == 1
}

func connect(ctx context.Context, t *testing.T, agentsURL string, c provision.Credentials) *pgx.Conn {
	t.Helper()
	dsn, err := c.DSN(agentsURL, "")
	if err != nil {
		t.Fatal(err)
	}
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect as %s: %v", c.Role, err)
	}
	return conn
}

func denied(err error) bool {
	return err != nil && (strings.Contains(err.Error(), "permission denied") || strings.Contains(err.Error(), "must be owner"))
}

// The provisioner runs as a CREATEROLE role that is not a superuser, owner
// of the agents database: as deployed.
func TestProvisionAsCreateRole(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	e := newEnv(t)
	agentIDs := []string{"11111111-2222-3333-4444-555555555555", "66666666-7777-8888-9999-000000000000"}
	for _, id := range agentIDs {
		e.track(id)
	}
	agentsURL := e.url(e.db)
	p, _ := e.provisioner(ctx)
	if err := p.Prepare(ctx); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	creds := make([]provision.Credentials, len(agentIDs))
	var err error
	for i, id := range agentIDs {
		if creds[i], err = p.Create(ctx, id); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}
	// Running Create again rotates the password.
	if creds[0], err = p.Create(ctx, agentIDs[0]); err != nil {
		t.Fatalf("create again: %v", err)
	}
	// The role says which agents database it serves (roles are server-wide).
	var comment string
	if err := e.su.QueryRow(ctx, "SELECT shobj_description(oid, 'pg_authid') FROM pg_roles WHERE rolname = $1", creds[0].Role).Scan(&comment); err != nil || !strings.HasSuffix(comment, e.db) {
		t.Fatalf("role comment = %q, %v", comment, err)
	}

	// Each agent uses its own schema and cannot read the other's.
	a, b := connect(ctx, t, agentsURL, creds[0]), connect(ctx, t, agentsURL, creds[1])
	defer b.Close(ctx)
	if _, err := a.Exec(ctx, fmt.Sprintf("CREATE TABLE %s.notes (body text)", creds[0].Schema)); err != nil {
		t.Fatalf("agent creates a table in its schema: %v", err)
	}
	if _, err := b.Exec(ctx, fmt.Sprintf("SELECT * FROM %s.notes", creds[0].Schema)); err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("another agent read the table: %v", err)
	}
	if _, err := b.Exec(ctx, "CREATE TABLE public.leak (x int)"); err == nil {
		t.Fatal("an agent created a table in schema public")
	}

	// Evict ends the agent's sessions; Delete removes its schema and role.
	if err := p.Evict(ctx, agentIDs[0]); err != nil {
		t.Fatalf("evict: %v", err)
	}
	if err := a.Ping(ctx); err == nil {
		t.Fatal("the evicted session still answers")
	}
	for _, id := range agentIDs {
		if err := p.Delete(ctx, id); err != nil {
			t.Fatalf("delete %s: %v", id, err)
		}
	}
	if names, err := p.Names(ctx); err != nil || len(names) != 0 {
		t.Fatalf("left after delete: %v %v", names, err)
	}
}

// An agent's ownership record (runtime/src/lib/storage/postgres.ts: a table in the
// agent's own schema, naming the owning session) can't be written, locked or
// dropped by another agent's role, and its owner's session can't be ended or
// its connections used up by another agent. The runtime's own version of this
// is runtime/test/ownership.test.ts; this one is on roles as provisioned.
func TestAnotherAgentCannotInterfereWithOwnership(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	e := newEnv(t)
	ids := []string{"aaaaaaaa-0000-0000-0000-000000000001", "bbbbbbbb-0000-0000-0000-000000000002"}
	for _, id := range ids {
		e.track(id)
	}
	p, _ := e.provisioner(ctx)
	if err := p.Prepare(ctx); err != nil {
		t.Fatal(err)
	}
	var creds [2]provision.Credentials
	for i, id := range ids {
		var err error
		if creds[i], err = p.Create(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	agentsURL := e.url(e.db)
	a, b := connect(ctx, t, agentsURL, creds[0]), connect(ctx, t, agentsURL, creds[1])
	defer a.Close(ctx)
	defer b.Close(ctx)

	table := creds[0].Schema + ".pdp_ownership"
	if _, err := a.Exec(ctx, "CREATE TABLE "+table+" (id integer PRIMARY KEY, owner_pid integer)"); err != nil {
		t.Fatalf("the agent creates its ownership table: %v", err)
	}
	var pidA int
	if err := a.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&pidA); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Exec(ctx, "INSERT INTO "+table+" VALUES (1, $1)", pidA); err != nil {
		t.Fatal(err)
	}

	for _, stmt := range []string{
		"SELECT * FROM " + table,
		"UPDATE " + table + " SET owner_pid = NULL",
		"INSERT INTO " + table + " VALUES (2, 0)",
		"DELETE FROM " + table,
		"TRUNCATE " + table,
		"SELECT * FROM " + table + " FOR UPDATE",
		"DROP TABLE " + table,
		"CREATE TABLE " + creds[0].Schema + ".planted (x int)",
	} {
		if _, err := b.Exec(ctx, stmt); !denied(err) {
			t.Errorf("another agent ran %q: %v", stmt, err)
		}
	}
	tx, err := b.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, "LOCK TABLE "+table+" IN ACCESS EXCLUSIVE MODE"); !denied(err) {
		t.Errorf("another agent locked the ownership table: %v", err)
	}
	_ = tx.Rollback(ctx)
	// Ending the owner's session (to provoke its restart or release its ownership) is refused.
	for _, fn := range []string{"pg_terminate_backend", "pg_cancel_backend"} {
		if _, err := b.Exec(ctx, "SELECT "+fn+"($1)", pidA); !denied(err) {
			t.Errorf("another agent called %s on the owner's session: %v", fn, err)
		}
	}
	// The record and the session are as A left them.
	var owner int
	if err := a.QueryRow(ctx, "SELECT owner_pid FROM "+table+" WHERE id = 1").Scan(&owner); err != nil || owner != pidA {
		t.Fatalf("ownership record = %d, %v", owner, err)
	}

	// B can't use up A's connections: the limit is per role, so B's third connection is
	// refused and A can still connect (a replacement overlaps its predecessor briefly).
	b2 := connect(ctx, t, agentsURL, creds[1])
	defer b2.Close(ctx)
	dsn, _ := creds[1].DSN(agentsURL, "")
	if c, err := pgx.Connect(ctx, dsn); err == nil {
		_ = c.Close(ctx)
		t.Error("a third connection of the same agent role was accepted (limit 2)")
	} else if !strings.Contains(err.Error(), "too many connections for role") {
		t.Errorf("third connection: %v", err)
	}
	a2 := connect(ctx, t, agentsURL, creds[0])
	_ = a2.Close(ctx)
}

// Roles are server-wide, so a dropped or recreated agents database leaves its
// agents' roles behind. OrphanRoles lists the ones that belong to no agent and
// no schema; PruneRoles drops only those, never one that is in use, made
// differently, marked for another agents database or still owning something.
func TestOrphanRolesAndPrune(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	e := newEnv(t)
	suffix := fmt.Sprint(time.Now().UnixNano() % 1e9)
	id := func(n int) string { return fmt.Sprintf("%08d-0000-0000-0000-%012s", n, suffix) }
	var (
		known, orphan1, orphan2 = id(1), id(2), id(3)
		connected, foreign      = id(4), id(5)
		owning, live            = id(6), id(7)
	)
	for _, a := range []string{known, orphan1, orphan2, connected, foreign, owning, live} {
		e.track(a)
	}
	name := func(agentID string) string { n, _ := provision.NameFor(agentID); return n }

	p, pool := e.provisioner(ctx)
	if err := p.Prepare(ctx); err != nil {
		t.Fatal(err)
	}
	creds := map[string]provision.Credentials{}
	for _, a := range []string{known, orphan1, orphan2, connected, foreign, owning} {
		c, err := p.Create(ctx, a)
		if err != nil {
			t.Fatal(err)
		}
		creds[a] = c
	}
	// The agents database is dropped and made again (same name): the roles stay, their schemas are gone.
	pool.Close()
	e.exec("DROP DATABASE " + e.db + " WITH (FORCE)")
	e.createDatabase(e.db)
	p, _ = e.provisioner(ctx)
	if err := p.Prepare(ctx); err != nil {
		t.Fatal(err)
	}
	if c, err := p.Create(ctx, live); err != nil { // an agent with a schema here that this platform doesn't know: not an orphan
		t.Fatal(err)
	} else {
		creds[live] = c
	}
	// A role of an agent made in another agents database of the platform's server, marked for it.
	e.exec(fmt.Sprintf("COMMENT ON ROLE %s IS 'pi-durable-platform agent role for agents database some_other_database'", name(foreign)))
	// A role that still owns something in another database of the server (another installation's live agent).
	other := "pdp_prov_other_" + suffix
	e.dbs = append(e.dbs, other)
	e.exec("CREATE DATABASE " + other)
	u, _ := url.Parse(e.adminURL)
	u.Path = "/" + other
	oc, err := pgx.Connect(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := oc.Exec(ctx, fmt.Sprintf("CREATE SCHEMA %s AUTHORIZATION %s", name(owning), name(owning))); err != nil {
		t.Fatal(err)
	}
	_ = oc.Close(ctx)
	// A role with an open session, a role made another way (CREATEDB, by a superuser), a name that isn't an agent's.
	conn := connect(ctx, t, e.url(e.db), creds[connected])
	defer conn.Close(ctx)
	madeOtherwise, notAgent := "agent_other_"+suffix, "agentx_"+suffix
	e.roles = append(e.roles, madeOtherwise, notAgent)
	e.exec("CREATE ROLE " + madeOtherwise + " LOGIN CREATEDB")
	e.exec("CREATE ROLE " + notAgent + " LOGIN")

	platformAgents := []string{known}
	orphans, skipped, err := p.OrphanRoles(ctx, platformAgents)
	if err != nil {
		t.Fatal(err)
	}
	skippedFor := func(n string) string {
		for _, s := range skipped {
			if s.Name == n {
				return s.Reason
			}
		}
		return ""
	}
	// The roles the platform made without a live agent or schema: listed.
	for _, a := range []string{orphan1, orphan2, owning} {
		if !slices.Contains(orphans, name(a)) {
			t.Errorf("orphans = %v, missing %s", orphans, name(a))
		}
	}
	// Known agents, agents with a schema, and roles not made like the platform's: not listed.
	for _, n := range []string{name(known), name(live), name(connected), name(foreign), madeOtherwise, notAgent} {
		if slices.Contains(orphans, n) {
			t.Errorf("orphans = %v, wrongly lists %s", orphans, n)
		}
	}
	if r := skippedFor(name(connected)); !strings.Contains(r, "open sessions") {
		t.Errorf("connected role: %q", r)
	}
	if r := skippedFor(name(foreign)); !strings.Contains(r, "another agents database") {
		t.Errorf("role marked for another database: %q", r)
	}
	if r := skippedFor(madeOtherwise); r == "" {
		t.Errorf("role made otherwise isn't reported as left alone")
	}
	if skippedFor(notAgent) != "" {
		t.Error("a role that isn't named like an agent role was considered")
	}

	// A dry run changes nothing.
	dropped, _, err := p.PruneRoles(ctx, platformAgents, true)
	if err != nil || !slices.Contains(dropped, name(orphan1)) || !e.roleExists(name(orphan1)) {
		t.Fatalf("dry run: %v, %v", dropped, err)
	}
	// The real run drops the orphans, keeps the one that owns something elsewhere, and touches nothing else.
	dropped, skipped, err = p.PruneRoles(ctx, platformAgents, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range []string{orphan1, orphan2} {
		if !slices.Contains(dropped, name(a)) || e.roleExists(name(a)) {
			t.Errorf("%s not dropped: %v", name(a), dropped)
		}
	}
	if slices.Contains(dropped, name(owning)) || !e.roleExists(name(owning)) {
		t.Errorf("the role that owns a schema in another database was dropped: %v", dropped)
	}
	if r := skippedFor(name(owning)); !strings.Contains(r, "owns objects") {
		t.Errorf("owning role: %q", r)
	}
	for _, n := range []string{name(known), name(live), name(connected), name(foreign), madeOtherwise, notAgent} {
		if !e.roleExists(n) {
			t.Errorf("%s was dropped", n)
		}
	}
	if err := conn.Ping(ctx); err != nil {
		t.Errorf("the connected role's session ended: %v", err)
	}
	// Idempotent.
	if again, _, err := p.PruneRoles(ctx, platformAgents, false); err != nil || slices.Contains(again, name(orphan1)) {
		t.Errorf("second run: %v, %v", again, err)
	}
}

// Replicas hardening the agents database as they start (Prepare is
// idempotent, and concurrent calls neither fail nor leave it open).
func TestPrepareWithSeveralReplicas(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	e := newEnv(t)
	var provs []*provision.Provisioner
	for range 8 {
		p, _ := e.provisioner(ctx)
		provs = append(provs, p)
	}
	errs := make(chan error, len(provs))
	for _, p := range provs {
		go func() { errs <- p.Prepare(ctx) }()
	}
	for range provs {
		if err := <-errs; err != nil {
			t.Errorf("Prepare: %v", err)
		}
	}
	var open bool // PUBLIC (grantee 0) may still create temporary tables
	if err := e.su.QueryRow(ctx, `SELECT coalesce(bool_or(a.grantee = 0 AND a.privilege_type = 'TEMPORARY'), false)
		FROM pg_database d, aclexplode(coalesce(d.datacl, acldefault('d', d.datdba))) a WHERE d.datname = $1`, e.db).Scan(&open); err != nil {
		t.Fatal(err)
	}
	if open {
		t.Fatal("PUBLIC may still create temporary tables")
	}
}

// loExists reports whether the large object exists in db (superuser view).
func (e *env) loExists(db string, oid uint32) bool {
	e.t.Helper()
	u, _ := url.Parse(e.adminURL)
	u.Path = "/" + db
	c, err := pgx.Connect(context.Background(), u.String())
	if err != nil {
		e.t.Fatal(err)
	}
	defer c.Close(context.Background())
	var n int
	if err := c.QueryRow(context.Background(), "SELECT count(*) FROM pg_largeobject_metadata WHERE oid = $1", oid).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n == 1
}

func (e *env) ownedBy(db, role string) int {
	e.t.Helper()
	u, _ := url.Parse(e.adminURL)
	u.Path = "/" + db
	c, err := pgx.Connect(context.Background(), u.String())
	if err != nil {
		e.t.Fatal(err)
	}
	defer c.Close(context.Background())
	var n int
	if err := c.QueryRow(context.Background(), `SELECT
		(SELECT count(*) FROM pg_class WHERE relowner = r.oid) + (SELECT count(*) FROM pg_namespace WHERE nspowner = r.oid) +
		(SELECT count(*) FROM pg_largeobject_metadata WHERE lomowner = r.oid)
		FROM pg_roles r WHERE r.rolname = $1`, role).Scan(&n); err != nil {
		if err == pgx.ErrNoRows {
			return 0
		}
		e.t.Fatal(err)
	}
	return n
}

// An agent's role can create objects the schema drop doesn't take (a large
// object is owned by the role, and a role that was given CREATE somewhere owns
// what it makes there): deleting the agent must still work, and leave nothing
// of the role. A compromised runtime must not make an agent undeletable.
func TestDeleteRemovesWhatTheRoleCreated(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	e := newEnv(t)
	const agentID = "aaaaaaaa-0000-0000-0000-000000000001"
	name := e.track(agentID)
	p, _ := e.provisioner(ctx)
	if err := p.Prepare(ctx); err != nil {
		t.Fatal(err)
	}
	creds, err := p.Create(ctx, agentID)
	if err != nil {
		t.Fatal(err)
	}
	// A schema the role was given (by a mistake, or by what it exploited) besides its own and public.
	e.dbExec(e.db, fmt.Sprintf("CREATE SCHEMA shared_%s AUTHORIZATION %s", name, e.role))
	e.dbExec(e.db, fmt.Sprintf("GRANT CREATE, USAGE ON SCHEMA shared_%s, public TO %s", name, name))

	conn := connect(ctx, t, e.url(e.db), creds)
	defer conn.Close(ctx)
	var lo uint32
	if err := conn.QueryRow(ctx, "SELECT lo_create(0)").Scan(&lo); err != nil {
		t.Fatalf("the agent creates a large object: %v", err)
	}
	for _, stmt := range []string{
		fmt.Sprintf("CREATE TABLE %s.notes (body text)", name),
		"CREATE TABLE public.leftover (x int)",
		fmt.Sprintf("CREATE TABLE shared_%s.leftover (x int)", name),
		"CREATE SEQUENCE public.leftover_seq",
	} {
		if _, err := conn.Exec(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	if !e.loExists(e.db, lo) || e.ownedBy(e.db, name) < 6 {
		t.Fatalf("setup: the role owns %d objects", e.ownedBy(e.db, name))
	}

	if err := p.Delete(ctx, agentID); err != nil {
		t.Fatalf("delete an agent whose role owns a large object and tables: %v", err)
	}
	if e.roleExists(name) {
		t.Fatal("the role is still there")
	}
	if e.loExists(e.db, lo) {
		t.Error("the large object is still there")
	}
	for _, rel := range []string{"public.leftover", "public.leftover_seq", fmt.Sprintf("shared_%s.leftover", name)} {
		var exists bool
		u, _ := url.Parse(e.adminURL)
		u.Path = "/" + e.db
		c, err := pgx.Connect(ctx, u.String())
		if err != nil {
			t.Fatal(err)
		}
		if err := c.QueryRow(ctx, "SELECT to_regclass($1) IS NOT NULL", rel).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		_ = c.Close(ctx)
		if exists {
			t.Errorf("%s is still there", rel)
		}
	}
	if err := conn.Ping(ctx); err == nil {
		t.Error("the agent's session survived the deletion")
	}
	if names, err := p.Names(ctx); err != nil || len(names) != 0 {
		t.Errorf("schemas left: %v %v", names, err)
	}
	// Repeating it is harmless.
	if err := p.Delete(ctx, agentID); err != nil {
		t.Errorf("delete again: %v", err)
	}
}

// A deletion that can't finish (the role still owns something in another
// database of the server) leaves the role unable to log in, ends its sessions,
// reports the problem, and a retry finishes once that is removed. Creating the
// agent's storage again (a failed deletion keeps the agent) gives the login back.
func TestDeleteIsRetrySafe(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	e := newEnv(t)
	const agentID = "aaaaaaaa-0000-0000-0000-000000000002"
	name := e.track(agentID)
	p, _ := e.provisioner(ctx)
	if err := p.Prepare(ctx); err != nil {
		t.Fatal(err)
	}
	creds, err := p.Create(ctx, agentID)
	if err != nil {
		t.Fatal(err)
	}
	other := "pdp_prov_other_" + fmt.Sprint(time.Now().UnixNano())
	e.dbs = append(e.dbs, other)
	e.exec("CREATE DATABASE " + other)
	e.dbExec(other, fmt.Sprintf("CREATE SCHEMA %s AUTHORIZATION %s", name, name))
	conn := connect(ctx, t, e.url(e.db), creds)
	defer conn.Close(ctx)

	err = p.Delete(ctx, agentID)
	if err == nil || !strings.Contains(err.Error(), "another database") {
		t.Fatalf("delete with an object in another database: %v", err)
	}
	if strings.Contains(err.Error(), creds.Password) {
		t.Fatal("the error holds the password")
	}
	if err := conn.Ping(ctx); err == nil {
		t.Error("the agent's session survived the failed deletion")
	}
	dsn, _ := creds.DSN(e.url(e.db), "")
	if c, err := pgx.Connect(ctx, dsn); err == nil {
		_ = c.Close(ctx)
		t.Fatal("the role can still log in after a failed deletion")
	}
	// The agent is kept; its storage is made again and works.
	if creds, err = p.Create(ctx, agentID); err != nil {
		t.Fatalf("create again: %v", err)
	}
	connect(ctx, t, e.url(e.db), creds).Close(ctx)
	// Once the other database's object is gone, the retry deletes it all.
	e.dbExec(other, fmt.Sprintf("DROP SCHEMA %s", name))
	for range 2 {
		if err := p.Delete(ctx, agentID); err != nil {
			t.Fatalf("retry: %v", err)
		}
	}
	if e.roleExists(name) {
		t.Fatal("the role is still there")
	}
}

// prune-roles drops what a left-over role (no agent, no schema here) still owns
// in the agents database, so a large object doesn't make it permanent; it
// leaves alone one that owns something in another database.
func TestPruneRolesDropsLeftoversInTheAgentsDatabase(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	e := newEnv(t)
	const agentID, nologinID = "aaaaaaaa-0000-0000-0000-000000000003", "aaaaaaaa-0000-0000-0000-000000000004"
	name, nologin := e.track(agentID), e.track(nologinID)
	p, _ := e.provisioner(ctx)
	if err := p.Prepare(ctx); err != nil {
		t.Fatal(err)
	}
	creds, err := p.Create(ctx, agentID)
	if err != nil {
		t.Fatal(err)
	}
	conn := connect(ctx, t, e.url(e.db), creds)
	var lo uint32
	if err := conn.QueryRow(ctx, "SELECT lo_create(0)").Scan(&lo); err != nil {
		t.Fatal(err)
	}
	_ = conn.Close(ctx)
	// The schema is gone (an old failed deletion, or the agent was removed by hand); the role and its large object are not.
	e.dbExec(e.db, fmt.Sprintf("DROP SCHEMA %s CASCADE", name))
	// A role a half-finished deletion made NOLOGIN.
	if _, err := p.Create(ctx, nologinID); err != nil {
		t.Fatal(err)
	}
	e.dbExec(e.db, fmt.Sprintf("DROP SCHEMA %s CASCADE", nologin))
	e.exec(fmt.Sprintf("ALTER ROLE %s NOLOGIN", nologin))

	orphans, _, err := p.OrphanRoles(ctx, nil)
	if err != nil || !slices.Contains(orphans, name) || !slices.Contains(orphans, nologin) {
		t.Fatalf("orphans %v, %v", orphans, err)
	}
	dropped, skipped, err := p.PruneRoles(ctx, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(dropped, name) || !slices.Contains(dropped, nologin) || e.roleExists(name) || e.roleExists(nologin) {
		t.Fatalf("dropped %v, skipped %v", dropped, skipped)
	}
	if e.loExists(e.db, lo) {
		t.Error("the large object is still there")
	}
}

// An agent's role can connect to the agents database and take any advisory
// lock, including the one replicas use to prepare it one at a time: holding it
// must not stop the platform from starting.
func TestAnAgentHoldingThePrepareLockDoesNotBlockPrepare(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	e := newEnv(t)
	const agentID = "aaaaaaaa-0000-0000-0000-000000000005"
	e.track(agentID)
	p, _ := e.provisioner(ctx)
	if err := p.Prepare(ctx); err != nil {
		t.Fatal(err)
	}
	creds, err := p.Create(ctx, agentID)
	if err != nil {
		t.Fatal(err)
	}
	hostile := connect(ctx, t, e.url(e.db), creds)
	defer hostile.Close(ctx)
	if _, err := hostile.Exec(ctx, "SELECT pg_advisory_lock(hashtextextended('pdp.prepare-agents-database', 0))"); err != nil {
		t.Fatal(err)
	}
	defer provision.SetPrepareLockWait(300 * time.Millisecond)()

	start := time.Now()
	replicas := make(chan error, 4)
	for range cap(replicas) {
		go func() { q, _ := e.provisioner(ctx); replicas <- q.Prepare(ctx) }()
	}
	for range cap(replicas) {
		if err := <-replicas; err != nil {
			t.Errorf("Prepare with the lock held by an agent's role: %v", err)
		}
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Errorf("Prepare took %v", d)
	}
}
