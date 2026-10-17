// Package doctor implements `pi-durable-platform doctor`: the configuration
// and production safety checks, then PostgreSQL connectivity and the
// migration state, with timings, then API key pepper rotation: keys still
// on another pepper. It changes nothing.
package doctor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ncecere/pi-durable-platform/internal/buildinfo"
	"github.com/ncecere/pi-durable-platform/internal/config"
	"github.com/ncecere/pi-durable-platform/internal/secrets"
	"github.com/ncecere/pi-durable-platform/internal/store"
	"github.com/ncecere/pi-durable-platform/internal/store/dbgen"
)

// Status of one check.
type Status string

const (
	OK   Status = "ok"
	Info Status = "info" // worth knowing
	Fail Status = "fail" // broken: doctor exits non-zero
)

// Check is one line of the report.
type Check struct {
	Group      string  `json:"group"` // configuration, safety, postgres, keys
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
	if pool := r.postgres(ctx); pool != nil {
		pool.Close()
	}
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
	}
	return pool
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

var symbols = map[Status]string{OK: "ok  ", Info: "info", Fail: "FAIL"}

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
	fmt.Fprintf(&b, "\n%d checks: %d ok, %d info, %d failed. %s\n", len(rep.Checks), counts[OK], counts[Info], counts[Fail], verdict)
	_, err := io.WriteString(w, b.String())
	return err
}
