// Package provision creates and removes the PostgreSQL storage of one agent: a
// login role and a schema it owns, in the agents database (separate from the
// platform's own metadata database).
//
// Isolation comes from ownership: an agent role owns only its schema and has
// no privileges on any other, so it cannot read another agent's transcript,
// write or lock another agent's ownership record (the runtime keeps it in the
// agent's own schema, runtime/src/storage/postgres.ts), or end another agent's
// sessions. The agents database revokes PUBLIC's default privileges on schema
// public so nothing else is reachable. The provisioning connection needs
// CREATEROLE and CREATE on the agents database; it is never given to agents.
//
// Roles are server-wide, so they outlive a dropped agents database. Create
// marks each role with the agents database it was made for, and OrphanRoles
// and PruneRoles find and drop the ones that belong to no agent.
package provision

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"regexp"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ncecere/pi-durable-platform/internal/store"
)

// Credentials are what an agent runtime needs to open its storage.
type Credentials struct {
	Schema   string
	Role     string
	Password string
}

// DSN returns a connection URL for the agent role, based on the agents
// database URL: database and TLS settings are kept, the user and password
// replaced, and host:port replaced by hostOverride when it is set (runtimes
// that reach PostgreSQL by another address).
func (c Credentials) DSN(agentsDatabaseURL, hostOverride string) (string, error) {
	u, err := url.Parse(agentsDatabaseURL)
	if err != nil {
		return "", fmt.Errorf("agents database URL: %w", err)
	}
	u.User = url.UserPassword(c.Role, c.Password)
	if hostOverride != "" {
		u.Host = hostOverride
	}
	return u.String(), nil
}

var namePattern = regexp.MustCompile(`^agent_[a-z0-9_]{1,56}$`)

// NameFor returns the schema and role name for an agent ID (a UUID or other
// [a-z0-9-] identifier).
func NameFor(agentID string) (string, error) {
	n := "agent_" + regexp.MustCompile(`[^a-z0-9]`).ReplaceAllString(agentID, "")
	if !namePattern.MatchString(n) {
		return "", fmt.Errorf("agent ID %q does not make a valid schema name", agentID)
	}
	return n, nil
}

// Provisioner manages agent schemas and roles.
type Provisioner struct {
	admin *pgxpool.Pool
}

func New(admin *pgxpool.Pool) *Provisioner { return &Provisioner{admin: admin} }

// Prepare hardens the agents database once: PUBLIC loses CREATE and USAGE on
// schema public and TEMP on the database. Idempotent.
//
// Platform replicas prepare the database as they start, and concurrent
// REVOKEs on the same object fail ("tuple concurrently updated"): one at a
// time, through an advisory lock in the agents database.
func (p *Provisioner) Prepare(ctx context.Context) error {
	unlock, err := store.AdvisoryLockText(ctx, p.admin, "pdp.prepare-agents-database")
	if err != nil {
		return fmt.Errorf("prepare agents database: %w", err)
	}
	defer unlock()
	var db string
	if err := p.admin.QueryRow(ctx, "SELECT current_database()").Scan(&db); err != nil {
		return err
	}
	for _, stmt := range []string{
		"REVOKE ALL ON SCHEMA public FROM PUBLIC",
		fmt.Sprintf("REVOKE TEMPORARY ON DATABASE %s FROM PUBLIC", quoteIdent(db)),
	} {
		if _, err := p.admin.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("prepare agents database: %w", err)
		}
	}
	return nil
}

// Create makes the role and schema for an agent, or rotates the password if
// they already exist (so a lost credential can be recovered by re-running).
func (p *Provisioner) Create(ctx context.Context, agentID string) (Credentials, error) {
	name, err := NameFor(agentID)
	if err != nil {
		return Credentials{}, err
	}
	password, err := randomSecret()
	if err != nil {
		return Credentials{}, err
	}
	tx, err := p.admin.Begin(ctx)
	if err != nil {
		return Credentials{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	var exists bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)", name).Scan(&exists); err != nil {
		return Credentials{}, err
	}
	// Identifiers and the password literal are quoted by the server (format %I/%L),
	// since DDL cannot take bind parameters. Two connections: the runtime holds
	// one; a replacement may briefly connect while the old one is closing. An
	// existing role only gets a new password (a CREATEROLE role may not
	// restate attributes it doesn't hold itself, such as NOCREATEDB).
	statement := "CREATE ROLE %I LOGIN NOINHERIT NOCREATEDB NOCREATEROLE CONNECTION LIMIT 2 PASSWORD %L"
	if exists {
		statement = "ALTER ROLE %I PASSWORD %L"
	}
	var ddl string
	if err := tx.QueryRow(ctx, "SELECT format($3::text, $1::text, $2::text)", name, password, statement).Scan(&ddl); err != nil {
		return Credentials{}, err
	}
	if _, err := tx.Exec(ctx, ddl); err != nil {
		return Credentials{}, fmt.Errorf("agent role: %w", err)
	}
	// A CREATEROLE role (not a superuser) gets only the ADMIN option on roles
	// it creates (PostgreSQL 16+). Membership with SET lets it create the
	// agent's schema owned by the role; INHERIT lets it end the agent's
	// sessions (Evict, Delete).
	if _, err := tx.Exec(ctx, fmt.Sprintf("GRANT %s TO CURRENT_USER WITH INHERIT TRUE, SET TRUE", quoteIdent(name))); err != nil {
		return Credentials{}, fmt.Errorf("agent role membership: %w", err)
	}
	if _, err := tx.Exec(ctx, fmt.Sprintf("CREATE SCHEMA IF NOT EXISTS %s AUTHORIZATION %s", quoteIdent(name), quoteIdent(name))); err != nil {
		return Credentials{}, fmt.Errorf("agent schema: %w", err)
	}
	// Roles are server-wide: the comment says which agents database this one
	// serves, so another installation on the same server leaves it alone.
	var mark string
	if err := tx.QueryRow(ctx, "SELECT format('COMMENT ON ROLE %I IS %L', $1::text, $2::text || current_database())", name, roleMarkerPrefix).Scan(&mark); err != nil {
		return Credentials{}, err
	}
	if _, err := tx.Exec(ctx, mark); err != nil {
		return Credentials{}, fmt.Errorf("agent role comment: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Credentials{}, err
	}
	return Credentials{Schema: name, Role: name, Password: password}, nil
}

// Delete disconnects the agent and removes its schema (all its data) and role.
func (p *Provisioner) Delete(ctx context.Context, agentID string) error {
	name, err := NameFor(agentID)
	if err != nil {
		return err
	}
	return p.deleteName(ctx, name)
}

// Names lists the agent schemas in the agents database. Roles are
// server-wide (shared by every database on the server), so a role is removed
// with its schema here; roles left without one are OrphanRoles.
func (p *Provisioner) Names(ctx context.Context) ([]string, error) {
	rows, err := p.admin.Query(ctx, `SELECT nspname FROM pg_namespace WHERE nspname LIKE 'agent\_%' ORDER BY 1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		if namePattern.MatchString(n) {
			out = append(out, n)
		}
	}
	return out, rows.Err()
}

// DeleteAll removes every agent's schema and role in the agents database.
// For tests that drop the database afterwards: roles outlive databases.
func (p *Provisioner) DeleteAll(ctx context.Context) error {
	names, err := p.Names(ctx)
	if err != nil {
		return err
	}
	for _, n := range names {
		if err := p.deleteName(ctx, n); err != nil {
			return err
		}
	}
	return nil
}

// DeleteName removes an agent schema and role by name (see Names).
func (p *Provisioner) DeleteName(ctx context.Context, name string) error {
	if !namePattern.MatchString(name) {
		return fmt.Errorf("%q is not an agent storage name", name)
	}
	return p.deleteName(ctx, name)
}

func (p *Provisioner) deleteName(ctx context.Context, name string) error {
	if _, err := p.admin.Exec(ctx, "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE usename = $1", name); err != nil {
		return err
	}
	if _, err := p.admin.Exec(ctx, fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", quoteIdent(name))); err != nil {
		return fmt.Errorf("drop agent schema: %w", err)
	}
	if _, err := p.admin.Exec(ctx, fmt.Sprintf("DROP ROLE IF EXISTS %s", quoteIdent(name))); err != nil {
		return fmt.Errorf("drop agent role: %w", err)
	}
	return nil
}

// Evict terminates the agent's database sessions, releasing its ownership
// lock. Used when a runtime is stuck and must be replaced.
func (p *Provisioner) Evict(ctx context.Context, agentID string) error {
	name, err := NameFor(agentID)
	if err != nil {
		return err
	}
	_, err = p.admin.Exec(ctx, "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE usename = $1", name)
	return err
}

// roleMarkerPrefix starts the comment Create puts on an agent role; the rest is
// the agents database's name.
const roleMarkerPrefix = "pi-durable-platform agent role for agents database "

// SkippedRole is a role named like an agent's that pruning leaves alone, and why.
type SkippedRole struct {
	Name   string
	Reason string
}

// OrphanRoles lists the roles that look like agent roles (named like
// NameFor's) but belong to no agent and to no schema of this agents
// database: left behind when an agents database was dropped or recreated,
// since roles are server-wide. agentIDs are the platform's agents; a role of
// one of them is never listed, nor is any role with a schema here.
//
// Only roles that are what Create makes are listed: a plain login role with
// no privileged attributes, no memberships of its own, managed by this
// connection's role (it is a member, as Create grants), not marked for
// another agents database, and without open sessions. Anything else named like
// an agent role is returned as skipped with the reason. The server decides the
// rest when the role is dropped: DROP ROLE refuses a role that still owns
// objects or holds privileges in any database of the server, which is how
// another installation's live agents (their roles own their schemas there) stay
// safe.
func (p *Provisioner) OrphanRoles(ctx context.Context, agentIDs []string) (orphans []string, skipped []SkippedRole, err error) {
	known := map[string]bool{}
	for _, id := range agentIDs {
		n, err := NameFor(id)
		if err != nil {
			return nil, nil, err
		}
		known[n] = true
	}
	roles, err := p.roleRows(ctx, "")
	if err != nil {
		return nil, nil, err
	}
	for _, r := range roles {
		if known[r.name] || r.hasSchema {
			continue
		}
		if reason := r.refusal(); reason != "" {
			skipped = append(skipped, SkippedRole{Name: r.name, Reason: reason})
			continue
		}
		orphans = append(orphans, r.name)
	}
	return orphans, skipped, nil
}

// PruneRoles drops the orphaned roles (see OrphanRoles); with dryRun it only
// lists them. A role the server refuses to drop (it still owns objects or has
// privileges somewhere on the server) is skipped, never forced: there is no
// DROP OWNED or CASCADE here. Returns the roles dropped (or that would be).
func (p *Provisioner) PruneRoles(ctx context.Context, agentIDs []string, dryRun bool) (dropped []string, skipped []SkippedRole, err error) {
	orphans, skipped, err := p.OrphanRoles(ctx, agentIDs)
	if err != nil || dryRun {
		return orphans, skipped, err
	}
	for _, name := range orphans {
		reason, err := p.dropOrphanRole(ctx, name)
		if err != nil {
			return dropped, skipped, err
		}
		if reason != "" {
			skipped = append(skipped, SkippedRole{Name: name, Reason: reason})
			continue
		}
		dropped = append(dropped, name)
	}
	return dropped, skipped, nil
}

// dropOrphanRole checks the role again right before dropping it. It returns
// the reason the role was kept, or "" when it is gone.
func (p *Provisioner) dropOrphanRole(ctx context.Context, name string) (string, error) {
	if !namePattern.MatchString(name) {
		return "", fmt.Errorf("%q is not an agent role name", name)
	}
	rows, err := p.roleRows(ctx, name)
	if err != nil {
		return "", err
	}
	switch {
	case len(rows) == 0:
		return "", nil // already gone
	case rows[0].hasSchema:
		return "has a schema in the agents database", nil
	case rows[0].refusal() != "":
		return rows[0].refusal(), nil
	}
	if _, err := p.admin.Exec(ctx, "DROP ROLE "+quoteIdent(name)); err != nil {
		var pgErr *pgconn.PgError
		switch {
		case errors.As(err, &pgErr) && pgErr.Code == "2BP01": // dependent_objects_still_exist
			return "still owns objects or holds privileges in a database of this server", nil
		case errors.As(err, &pgErr) && pgErr.Code == "42501": // insufficient_privilege
			return "this connection's role may not drop it", nil
		}
		return "", fmt.Errorf("drop role %s: %w", name, err)
	}
	return "", nil
}

type roleRow struct {
	name          string
	plain         bool // a login role without privileged attributes
	memberOfOther bool
	managed       bool // this connection's role is a member
	foreignMark   bool // its comment says it isn't an agent role of this agents database
	hasSchema     bool
	connected     bool
}

// refusal says why a role isn't an orphan agent role this connection may
// drop, or "" when it is.
func (r roleRow) refusal() string {
	switch {
	case !r.plain:
		return "has attributes an agent role doesn't have (superuser, CREATEDB, CREATEROLE, REPLICATION, BYPASSRLS) or can't log in"
	case r.memberOfOther:
		return "is a member of other roles"
	case !r.managed:
		return "wasn't made by this connection's role"
	case r.foreignMark:
		return "its comment marks it for another agents database, or it isn't an agent role of this platform"
	case r.connected:
		return "has open sessions"
	}
	return ""
}

func (p *Provisioner) roleRows(ctx context.Context, only string) ([]roleRow, error) {
	rows, err := p.admin.Query(ctx, `SELECT r.rolname,
		r.rolcanlogin AND NOT (r.rolsuper OR r.rolcreaterole OR r.rolcreatedb OR r.rolreplication OR r.rolbypassrls),
		EXISTS (SELECT 1 FROM pg_auth_members m WHERE m.member = r.oid),
		pg_has_role(current_user, r.oid, 'MEMBER'),
		coalesce(shobj_description(r.oid, 'pg_authid'), ''),
		EXISTS (SELECT 1 FROM pg_namespace n WHERE n.nspname = r.rolname),
		EXISTS (SELECT 1 FROM pg_stat_activity a WHERE a.usename = r.rolname),
		current_database()
		FROM pg_roles r WHERE r.rolname LIKE 'agent\_%' AND ($1 = '' OR r.rolname = $1) ORDER BY 1`, only)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []roleRow
	for rows.Next() {
		var r roleRow
		var marker, database string
		if err := rows.Scan(&r.name, &r.plain, &r.memberOfOther, &r.managed, &marker, &r.hasSchema, &r.connected, &database); err != nil {
			return nil, err
		}
		if !namePattern.MatchString(r.name) {
			continue
		}
		r.foreignMark = marker != "" && marker != roleMarkerPrefix+database
		out = append(out, r)
	}
	return out, rows.Err()
}

// quoteIdent quotes an identifier already validated against a strict pattern.
func quoteIdent(s string) string {
	for _, r := range s {
		if r == '"' || r == 0 {
			panic(errors.New("invalid identifier"))
		}
	}
	return `"` + s + `"`
}

func randomSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
