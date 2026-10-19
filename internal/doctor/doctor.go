// Package doctor implements `pi-durable-platform doctor`: the configuration
// and production safety checks, then PostgreSQL connectivity and the
// migration state, with timings, then key rotation (API keys still on another
// pepper, stored secrets still sealed with the previous encryption key), then
// the agents database (AGENTS_DATABASE_URL): that it answers, that its role may
// provision agents, and which agent roles belong to no agent. It changes nothing.
package doctor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ncecere/pi-durable-platform/internal/agenthost/provision"
	"github.com/ncecere/pi-durable-platform/internal/buildinfo"
	"github.com/ncecere/pi-durable-platform/internal/config"
	"github.com/ncecere/pi-durable-platform/internal/rekey"
	"github.com/ncecere/pi-durable-platform/internal/secrets"
	"github.com/ncecere/pi-durable-platform/internal/store"
	"github.com/ncecere/pi-durable-platform/internal/store/dbgen"
)

// Status of one check.
type Status string

const (
	OK   Status = "ok"
	Info Status = "info" // worth knowing
	Warn Status = "warn" // something to do, but the platform works: doctor still exits zero
	Fail Status = "fail" // broken: doctor exits non-zero
)

// Check is one line of the report.
type Check struct {
	Group      string  `json:"group"` // configuration, safety, postgres, keys, agents
	Name       string  `json:"name"`
	Status     Status  `json:"status"`
	Detail     string  `json:"detail"`
	Fix        string  `json:"fix,omitempty"`
	DurationMs float64 `json:"durationMs,omitempty"`
}

// Report is the outcome of a run. OK is false when any check failed.
type Report struct {
	Version string  `json:"version"`
	Mode    string  `json:"mode"`
	OK      bool    `json:"ok"`
	Checks  []Check `json:"checks"`
}

// Options select what a run checks.
type Options struct {
	// Mode is the process mode whose configuration is validated: serve
	// (default) or migrate.
	Mode string
	// Timeout bounds each check (default 10s).
	Timeout time.Duration
}

type runner struct {
	cfg    config.Config
	opts   Options
	checks []Check
}

func (r *runner) add(c Check) { r.checks = append(r.checks, c) }

// Run performs every check.
func Run(ctx context.Context, cfg config.Config, opts Options) Report {
	if opts.Mode == "" {
		opts.Mode = "serve"
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 10 * time.Second
	}
	r := &runner{cfg: cfg, opts: opts}
	r.configuration()
	pool := r.postgres(ctx)
	if pool != nil {
		defer pool.Close()
	}
	r.agentsDatabase(ctx, pool)
	return r.report()
}

// ConfigLoadFailed is the report when the configuration cannot even be
// loaded (an invalid value or an unreadable *_FILE).
func ConfigLoadFailed(mode string, err error) Report {
	r := &runner{opts: Options{Mode: mode}}
	for _, e := range splitErrors(err) {
		r.add(Check{Group: "configuration", Name: "load", Status: Fail, Detail: e.Error()})
	}
	return r.report()
}

func (r *runner) report() Report {
	rep := Report{Version: buildinfo.String(), Mode: r.opts.Mode, OK: true, Checks: r.checks}
	for _, c := range r.checks {
		if c.Status == Fail {
			rep.OK = false
		}
	}
	return rep
}

// configuration reports the production safety checks, then the rest of
// the validation of the mode's configuration.
func (r *runner) configuration() {
	safety := r.cfg.SafetyErrors(r.opts.Mode)
	seen := map[string]bool{}
	for _, e := range safety {
		seen[e.Error()] = true
		r.add(Check{Group: "safety", Name: "production", Status: Fail, Detail: e.Error()})
	}
	var other []error
	for _, e := range splitErrors(r.cfg.Validate(r.opts.Mode)) {
		if !seen[e.Error()] {
			other = append(other, e)
		}
	}
	for _, e := range other {
		r.add(Check{Group: "configuration", Name: r.opts.Mode, Status: Fail, Detail: e.Error()})
	}
	switch {
	case len(safety) == 0 && r.cfg.LoopbackAppURL():
		r.add(Check{Group: "safety", Name: "production", Status: Info,
			Detail: "APP_URL is loopback (" + r.cfg.AppURL + "): development install, production checks not applied"})
	case len(safety) == 0:
		r.add(Check{Group: "safety", Name: "production", Status: OK, Detail: "DEV_AUTH off, APP_URL is https"})
	}
	if len(other) == 0 {
		r.add(Check{Group: "configuration", Name: r.opts.Mode, Status: OK, Detail: "valid for pi-durable-platform " + r.opts.Mode})
	}
}

// postgres connects, reports the server version and the migration state.
// It returns the pool (nil when unavailable).
func (r *runner) postgres(ctx context.Context) *pgxpool.Pool {
	if r.cfg.DatabaseURL == "" {
		r.add(Check{Group: "postgres", Name: "connect", Status: Fail, Detail: "DATABASE_URL is not set"})
		return nil
	}
	cctx, cancel := context.WithTimeout(ctx, r.opts.Timeout)
	defer cancel()
	start := time.Now()
	pool, err := store.Open(cctx, r.cfg.DatabaseURL)
	if err != nil {
		r.add(Check{Group: "postgres", Name: "connect", Status: Fail, Detail: err.Error(), DurationMs: millis(time.Since(start)),
			Fix: "Start PostgreSQL (make deps-up) and check DATABASE_URL"})
		return nil
	}
	var version string
	err = pool.QueryRow(cctx, "SHOW server_version").Scan(&version)
	c := Check{Group: "postgres", Name: "connect", Status: OK, Detail: "PostgreSQL " + version, DurationMs: millis(time.Since(start))}
	if err != nil {
		c.Status, c.Detail = Fail, "server version: "+err.Error()
	}
	r.add(c)
	if r.migrations(cctx, pool) {
		r.keys(cctx, pool)
		r.encryption(cctx, pool)
	}
	return pool
}

// provisionMinVersion is the oldest PostgreSQL that can provision agents:
// provisioning grants the platform's role membership of each agent role
// WITH INHERIT TRUE, SET TRUE (PostgreSQL 16).
const provisionMinVersion = 160000

// agentsDatabase connects to the agents database and checks what
// provisioning an agent needs of its role (internal/agenthost/provision):
// CREATEROLE, for the agent's role, and CREATE on the database, for its
// schema (or a superuser), on PostgreSQL 16 or later. It reads the role's
// attributes and privileges; nothing is created. Without
// AGENTS_DATABASE_URL the checks are skipped (the configuration checks say
// whether the mode needs it). platform is the platform database's pool (nil
// when unavailable), for the agents whose roles are expected.
func (r *runner) agentsDatabase(ctx context.Context, platform *pgxpool.Pool) {
	if r.cfg.Agents.DatabaseURL == "" {
		r.add(Check{Group: "agents", Name: "database", Status: Info, Detail: "skipped: AGENTS_DATABASE_URL is not set, so agents can't be deployed"})
		return
	}
	// Not the URL itself or the parser's error: either may hold the password.
	pcfg, err := pgxpool.ParseConfig(r.cfg.Agents.DatabaseURL)
	if err != nil {
		r.add(Check{Group: "agents", Name: "database", Status: Fail, Detail: "AGENTS_DATABASE_URL is not a valid PostgreSQL connection URL"})
		return
	}
	name := pcfg.ConnConfig.Database
	cctx, cancel := context.WithTimeout(ctx, r.opts.Timeout)
	defer cancel()
	start := time.Now()
	pool, err := store.Open(cctx, r.cfg.Agents.DatabaseURL)
	if err != nil {
		c := Check{Group: "agents", Name: "database", Status: Fail, Detail: err.Error(), DurationMs: millis(time.Since(start)),
			Fix: "Check AGENTS_DATABASE_URL: the agents database must exist and accept the platform's provisioning role"}
		if missingDatabase(err) && r.cfg.Agents.CreateDatabase {
			c.Status, c.Fix = Info, ""
			c.Detail = fmt.Sprintf("database %s doesn't exist yet: serve creates it (AGENTS_CREATE_DATABASE)", name)
		}
		r.add(c)
		return
	}
	defer pool.Close()
	var (
		role, db, version           string
		versionNum                  int
		super, createRole, createDB bool
	)
	err = pool.QueryRow(cctx, `SELECT current_user, current_database(), current_setting('server_version'),
		current_setting('server_version_num')::int, r.rolsuper, r.rolcreaterole,
		has_database_privilege(current_database(), 'CREATE')
		FROM pg_roles r WHERE r.rolname = current_user`).Scan(&role, &db, &version, &versionNum, &super, &createRole, &createDB)
	c := Check{Group: "agents", Name: "database", Status: OK, DurationMs: millis(time.Since(start)),
		Detail: fmt.Sprintf("PostgreSQL %s, database %s as %s", version, db, role)}
	if err != nil {
		c.Status, c.Detail = Fail, "role and privileges: "+err.Error()
		r.add(c)
		return
	}
	r.add(c)
	p := Check{Group: "agents", Name: "provisioning", Status: OK}
	var missing []string
	if !createRole {
		missing = append(missing, "CREATEROLE")
	}
	if !createDB {
		missing = append(missing, "CREATE on database "+db)
	}
	switch {
	case versionNum < provisionMinVersion:
		p.Status = Fail
		p.Detail = fmt.Sprintf("PostgreSQL %s can't provision agents: it needs PostgreSQL 16 or later", version)
		p.Fix = "Put the agents database on PostgreSQL 16 or later"
	case super:
		p.Detail = fmt.Sprintf("%s is a superuser (CREATEROLE and CREATE on %s are enough)", role, db)
	case len(missing) > 0:
		p.Status = Fail
		p.Detail = fmt.Sprintf("%s can't provision agents: it lacks %s", role, strings.Join(missing, " and "))
		p.Fix = fmt.Sprintf("ALTER ROLE %s CREATEROLE; GRANT CREATE ON DATABASE %s TO %s (or make it the database's owner)", role, db, role)
	default:
		p.Detail = fmt.Sprintf("%s has CREATEROLE and CREATE on %s", role, db)
	}
	r.add(p)
	if p.Status != Fail {
		r.orphanRoles(ctx, platform, pool)
	}
}

// orphanRoles lists agent roles that belong to no agent of the platform and
// have no schema in the agents database. Roles are server-wide, so a dropped
// or recreated agents database leaves them behind; prune-roles drops them.
func (r *runner) orphanRoles(ctx context.Context, platform, agents *pgxpool.Pool) {
	ctx, cancel := context.WithTimeout(ctx, r.opts.Timeout)
	defer cancel()
	c := Check{Group: "agents", Name: "roles", Status: OK}
	if platform == nil {
		c.Status, c.Detail = Info, "skipped: the platform database is unavailable, so the agents' roles are unknown"
		r.add(c)
		return
	}
	start := time.Now()
	ids, err := dbgen.New(platform).AgentIDs(ctx)
	if err != nil {
		c.Status, c.Detail = Info, "skipped: the platform's agents can't be listed (migrations pending?)"
		r.add(c)
		return
	}
	known := make([]string, len(ids))
	for i, id := range ids {
		known[i] = id.String()
	}
	orphans, skipped, err := provision.New(agents).OrphanRoles(ctx, known)
	c.DurationMs = millis(time.Since(start))
	switch {
	case err != nil:
		c.Status, c.Detail = Fail, "agent roles: "+err.Error()
	case len(orphans) > 0:
		c.Status = Info
		c.Detail = fmt.Sprintf("%d agent role(s) belong to no agent and have no schema here: %s", len(orphans), listNames(orphans))
		if len(skipped) > 0 {
			c.Detail += fmt.Sprintf("; %d more look like agent roles but prune-roles would leave them alone", len(skipped))
		}
		c.Fix = "Run pi-durable-platform prune-roles --dry-run, then prune-roles"
	case len(skipped) > 0:
		c.Status = Info
		c.Detail = fmt.Sprintf("no droppable orphaned agent roles; %d role(s) named like agent roles are left alone: %s", len(skipped), listNames(skippedNames(skipped)))
	default:
		c.Detail = "every agent role belongs to an agent or to a schema in the agents database"
	}
	r.add(c)
}

func skippedNames(s []provision.SkippedRole) []string {
	out := make([]string, len(s))
	for i, x := range s {
		out[i] = x.Name
	}
	return out
}

// listNames joins up to five names, then counts the rest.
func listNames(names []string) string {
	if len(names) <= 5 {
		return strings.Join(names, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(names[:5], ", "), len(names)-5)
}

// missingDatabase reports whether a connection failed because the database
// doesn't exist.
func missingDatabase(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "3D000"
}

// keys reports usable API keys and service account keys by pepper.
func (r *runner) keys(ctx context.Context, pool *pgxpool.Pool) {
	pepper, err := secrets.ParseKey(r.cfg.APIKeyPepper)
	if err != nil {
		return // reported by the configuration checks
	}
	p := dbgen.CountKeysByPepperParams{CurrentID: secrets.KeyID(pepper)}
	if r.cfg.APIKeyPepperPrevious != "" {
		if pp, err := secrets.ParseKey(r.cfg.APIKeyPepperPrevious); err == nil {
			p.PreviousID = secrets.KeyID(pp)
		}
	}
	start := time.Now()
	n, err := dbgen.New(pool).CountKeysByPepper(ctx, p)
	c := Check{Group: "keys", Name: "pepper", Status: OK, DurationMs: millis(time.Since(start))}
	switch {
	case err != nil:
		c.Status, c.Detail = Fail, err.Error()
	case n.OnPrevious+n.Others > 0:
		c.Status = Info
		c.Detail = fmt.Sprintf("%d usable key(s) on API_KEY_PEPPER_PREVIOUS, %d on a pepper no longer configured", n.OnPrevious, n.Others)
		c.Fix = "Keys move to API_KEY_PEPPER when used. Keys on a pepper no longer configured no longer work: revoke them"
	case p.PreviousID != nil:
		c.Status = Info
		c.Detail = "every usable key is on API_KEY_PEPPER: API_KEY_PEPPER_PREVIOUS can be removed"
	default:
		c.Detail = "every usable key is on API_KEY_PEPPER"
	}
	r.add(c)
}

// encryption reports the stored secrets by the key they are sealed with
// (from their key ids; nothing is opened): values still under
// ENCRYPTION_KEY_PREVIOUS need `rekey`, values under no configured key
// cannot be read at all.
func (r *runner) encryption(ctx context.Context, pool *pgxpool.Pool) {
	cur, err := secrets.ParseKey(r.cfg.EncryptionKey)
	if err != nil {
		return // reported by the configuration checks
	}
	var prev [][]byte
	if r.cfg.EncryptionKeyPrevious != "" {
		p, err := secrets.ParseKey(r.cfg.EncryptionKeyPrevious)
		if err != nil {
			return // likewise
		}
		prev = append(prev, p)
	}
	box, err := secrets.New(cur, prev...)
	if err != nil {
		return
	}
	start := time.Now()
	counts, err := rekey.Count(ctx, pool, box)
	c := Check{Group: "keys", Name: "encryption", Status: OK, DurationMs: millis(time.Since(start))}
	var current, previous, unreadable int
	var onPrevious, onNone []string
	for _, n := range counts {
		current += n.Current
		previous += n.Previous
		unreadable += n.Unreadable
		if n.Previous > 0 {
			onPrevious = append(onPrevious, fmt.Sprintf("%s %d", n.Label(), n.Previous))
		}
		if n.Unreadable > 0 {
			onNone = append(onNone, fmt.Sprintf("%s %d", n.Label(), n.Unreadable))
		}
	}
	switch {
	case err != nil:
		c.Status, c.Detail = Fail, err.Error()
	case unreadable > 0:
		c.Status = Fail
		c.Detail = fmt.Sprintf("%d stored secret(s) are sealed with neither ENCRYPTION_KEY nor ENCRYPTION_KEY_PREVIOUS and can't be read (%s)", unreadable, strings.Join(onNone, ", "))
		c.Fix = "Set ENCRYPTION_KEY_PREVIOUS to the key they were sealed with and run pi-durable-platform rekey, or enter those secrets again"
	case previous > 0:
		c.Status = Warn
		c.Detail = fmt.Sprintf("%d stored secret(s) are still sealed with ENCRYPTION_KEY_PREVIOUS (%s)", previous, strings.Join(onPrevious, ", "))
		c.Fix = "Run pi-durable-platform rekey, then remove ENCRYPTION_KEY_PREVIOUS"
	case box.HasPrevious():
		c.Status = Info
		c.Detail = fmt.Sprintf("no stored secret is sealed with ENCRYPTION_KEY_PREVIOUS (%d under ENCRYPTION_KEY): it can be removed", current)
	default:
		c.Detail = fmt.Sprintf("every stored secret (%d) is sealed with ENCRYPTION_KEY", current)
	}
	r.add(c)
}

// migrations reports the migration state; ok when the schema is current.
func (r *runner) migrations(ctx context.Context, pool *pgxpool.Pool) bool {
	st, err := store.Migrations(ctx, pool)
	c := Check{Group: "postgres", Name: "migrations", Status: OK}
	switch {
	case err != nil:
		c.Status, c.Detail = Fail, err.Error()
	case st.Pending:
		c.Status = Fail
		c.Detail = fmt.Sprintf("pending: the database is at version %d, this binary has %d", st.Current, st.Latest)
		c.Fix = "Run pi-durable-platform migrate"
	case st.Current > st.Latest:
		c.Status = Info
		c.Detail = fmt.Sprintf("the database (version %d) is newer than this binary (%d): an older binary during a rollout?", st.Current, st.Latest)
	default:
		c.Detail = fmt.Sprintf("current (version %d)", st.Current)
	}
	r.add(c)
	return err == nil && !st.Pending && st.Current == st.Latest
}

// splitErrors lists the errors joined in err (errors.Join), one per check.
func splitErrors(err error) []error {
	if j, ok := err.(interface{ Unwrap() []error }); ok {
		var out []error
		for _, e := range j.Unwrap() {
			out = append(out, splitErrors(e)...)
		}
		return out
	}
	if err == nil {
		return nil
	}
	return []error{err}
}

func millis(d time.Duration) float64 {
	return float64(d.Round(10*time.Microsecond)) / float64(time.Millisecond)
}

var symbols = map[Status]string{OK: "ok  ", Info: "info", Warn: "warn", Fail: "FAIL"}

// WriteJSON writes the report as indented JSON.
func (rep Report) WriteJSON(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(rep)
}

// WriteText writes the report as a table: status, check, duration, detail,
// with fixes on indented lines below.
func (rep Report) WriteText(w io.Writer) error {
	var b strings.Builder
	fmt.Fprintf(&b, "pi-durable-platform doctor (%s; configuration checked for %s)\n\n", rep.Version, rep.Mode)
	counts := map[Status]int{}
	for _, c := range rep.Checks {
		counts[c.Status]++
		dur := ""
		if c.DurationMs > 0 {
			dur = fmt.Sprintf("%.1fms", c.DurationMs)
		}
		fmt.Fprintf(&b, "%s  %-24s %9s  %s\n", symbols[c.Status], c.Group+" "+c.Name, dur, c.Detail)
		if c.Fix != "" && c.Status != OK {
			fmt.Fprintf(&b, "%39sfix: %s\n", "", c.Fix)
		}
	}
	verdict := "all checks passed"
	if !rep.OK {
		verdict = "FAILED"
	}
	fmt.Fprintf(&b, "\n%d checks: %d ok, %d info, %d warning, %d failed. %s\n", len(rep.Checks), counts[OK], counts[Info], counts[Warn], counts[Fail], verdict)
	_, err := io.WriteString(w, b.String())
	return err
}
