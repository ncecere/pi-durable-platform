// Package provision creates and removes the PostgreSQL storage of one agent: a
// login role and a schema it owns, in the agents database (separate from the
// platform's own metadata database).
//
// Isolation comes from ownership: an agent role owns only its schema and has
// no privileges on any other, so it cannot read another agent's transcript.
// The agents database revokes PUBLIC's default privileges on schema public so
// nothing else is reachable. The provisioning connection needs CREATEROLE and
// CREATE on the agents database; it is never given to agents.
package provision

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"regexp"

	"github.com/jackc/pgx/v5/pgxpool"
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
func (p *Provisioner) Prepare(ctx context.Context) error {
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
	verb := "CREATE"
	if exists {
		verb = "ALTER"
	}
	// Identifiers and the password literal are quoted by the server (format %I/%L),
	// since DDL cannot take bind parameters. Two connections: the runtime holds
	// one; a replacement may briefly connect while the old one is closing.
	var ddl string
	if err := tx.QueryRow(ctx,
		"SELECT format('"+verb+" ROLE %I LOGIN NOINHERIT NOCREATEDB NOCREATEROLE CONNECTION LIMIT 2 PASSWORD %L', $1::text, $2::text)",
		name, password).Scan(&ddl); err != nil {
		return Credentials{}, err
	}
	if _, err := tx.Exec(ctx, ddl); err != nil {
		return Credentials{}, fmt.Errorf("agent role: %w", err)
	}
	if _, err := tx.Exec(ctx, fmt.Sprintf("CREATE SCHEMA IF NOT EXISTS %s AUTHORIZATION %s", quoteIdent(name), quoteIdent(name))); err != nil {
		return Credentials{}, fmt.Errorf("agent schema: %w", err)
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
