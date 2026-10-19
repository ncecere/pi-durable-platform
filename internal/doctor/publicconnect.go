package doctor

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ncecere/pi-durable-platform/internal/store"
)

// Where an agent's role can connect.
//
// Agent roles are server-wide, and PostgreSQL gives PUBLIC the CONNECT
// privilege on every database it creates, so unless it is revoked an agent's
// role (whose runtime runs code the platform doesn't trust) can open the
// platform's own database and any other database of the server. Its schema
// privileges stop it reading anything there, but it can take advisory locks,
// which are per database: the wake locks (`pdp.agent-wake:<agent>`), the
// reaper lock and the migration lock all live in the platform database, so
// holding one would stop agents waking, the reaper or a pod's start. A role
// that cannot connect to the platform database cannot take them. These checks
// warn (they change nothing, and other roles may rely on PUBLIC's CONNECT).

// publicConnectSQL is a condition on pg_database d: PUBLIC has CONNECT on it,
// from its ACL (NULL means the default, which grants it).
const publicConnectSQL = `EXISTS (SELECT 1 FROM aclexplode(COALESCE(d.datacl, acldefault('d', d.datdba))) a
	WHERE a.grantee = 0 AND a.privilege_type = 'CONNECT')`

// publicConnect reports whether PUBLIC may connect to the platform database
// and to the other databases of the agents database's server. platform is the
// platform database's pool (nil when unavailable).
func (r *runner) publicConnect(ctx context.Context, platform *pgxpool.Pool) {
	if platform == nil {
		return // the postgres checks say why
	}
	if r.cfg.Agents.DatabaseURL == "" {
		r.add(Check{Group: "postgres", Name: "public-connect", Status: Info, Detail: "skipped: AGENTS_DATABASE_URL is not set, so there are no agent roles"})
		return
	}
	ctx, cancel := context.WithTimeout(ctx, r.opts.Timeout)
	defer cancel()
	start := time.Now()
	agents, err := store.Open(ctx, r.cfg.Agents.DatabaseURL)
	if err != nil {
		r.add(Check{Group: "postgres", Name: "public-connect", Status: Info, Detail: "skipped: the agents database is unavailable, so its server is unknown"})
		return
	}
	defer agents.Close()

	var platformDB, platformRole string
	var platformOpen bool
	var platformStarted time.Time
	err = platform.QueryRow(ctx, `SELECT current_database(), current_user, pg_postmaster_start_time(), `+publicConnectSQL+`
		FROM pg_database d WHERE d.datname = current_database()`).Scan(&platformDB, &platformRole, &platformStarted, &platformOpen)
	if err != nil {
		r.add(Check{Group: "postgres", Name: "public-connect", Status: Info, Detail: "skipped: the platform database's privileges can't be read"})
		return
	}
	var agentsStarted time.Time
	if err := agents.QueryRow(ctx, "SELECT pg_postmaster_start_time()").Scan(&agentsStarted); err != nil {
		r.add(Check{Group: "postgres", Name: "public-connect", Status: Info, Detail: "skipped: the agents database's server can't be queried"})
		return
	}
	sameServer := platformStarted.Equal(agentsStarted)

	c := Check{Group: "postgres", Name: "public-connect", Status: OK, DurationMs: millis(time.Since(start))}
	switch {
	case !sameServer:
		c.Detail = fmt.Sprintf("the agents database is on another server than %s: its roles can't reach it", platformDB)
	case platformOpen:
		c.Status = Warn
		c.Detail = fmt.Sprintf("PUBLIC may connect to %s: an agent's role can open the platform database and take its advisory locks (agent wake, reaper, migration)", platformDB)
		c.Fix = fmt.Sprintf("REVOKE CONNECT ON DATABASE %s FROM PUBLIC; GRANT CONNECT ON DATABASE %s TO %s; (and to any other role that uses it). "+
			"Other databases on the server need the same REVOKE, or pg_hba.conf rules that limit agent roles to the agents database", ident(platformDB), ident(platformDB), ident(platformRole))
	default:
		c.Detail = fmt.Sprintf("PUBLIC may not connect to %s: agents' roles can't reach the platform's advisory locks", platformDB)
	}
	r.add(c)
	if sameServer {
		r.siblings(ctx, agents, platformDB)
	}
}

// siblings lists the other databases of the agents database's server that
// PUBLIC may connect to (so an agent's role may too), besides the platform
// database (reported on its own) and the agents database itself.
func (r *runner) siblings(ctx context.Context, agents *pgxpool.Pool, platformDB string) {
	c := Check{Group: "agents", Name: "public-connect", Status: OK}
	open, err := openDatabases(ctx, agents, platformDB)
	switch {
	case err != nil:
		c.Status, c.Detail = Info, "skipped: the server's databases can't be listed"
	case len(open) > 0:
		c.Status = Warn
		c.Detail = fmt.Sprintf("PUBLIC may connect to %d other database(s) on the agents database's server: %s: an agent's role can open them", len(open), listNames(open))
		var fix []string
		for _, name := range open[:min(len(open), 3)] {
			fix = append(fix, fmt.Sprintf("REVOKE CONNECT ON DATABASE %s FROM PUBLIC", ident(name)))
		}
		c.Fix = strings.Join(fix, "; ") + "; (grant CONNECT back to the roles that use them), or limit agent roles to the agents database in pg_hba.conf"
	default:
		c.Detail = "PUBLIC may connect to no other database on the agents database's server (pg_hba.conf may still limit roles further)"
	}
	r.add(c)
}

// openDatabases lists the databases PUBLIC may connect to, except templates,
// databases that refuse connections, the connection's own database (the
// agents database) and the ones named in except.
func openDatabases(ctx context.Context, pool *pgxpool.Pool, except ...string) ([]string, error) {
	except = append([]string{}, except...) // a NULL array would match nothing
	rows, err := pool.Query(ctx, `SELECT d.datname FROM pg_database d
		WHERE NOT d.datistemplate AND d.datallowconn AND d.datname <> current_database() AND NOT d.datname = ANY($1) AND `+publicConnectSQL+`
		ORDER BY 1`, except)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// ident quotes a database or role name for the SQL shown in a fix.
func ident(name string) string { return pgx.Identifier{name}.Sanitize() }
