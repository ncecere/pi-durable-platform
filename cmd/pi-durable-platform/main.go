// Command pi-durable-platform runs Pi Durable Platform, where teams and
// services deploy and manage Pi Durable agents.
//
//	pi-durable-platform serve     HTTP API, embedded UI and background loops
//	pi-durable-platform migrate   apply database migrations and exit
//	pi-durable-platform doctor    check the configuration, Postgres, migrations and the agents database
//	pi-durable-platform prune-roles           drop agent database roles no agent owns
//	pi-durable-platform rekey     re-seal every stored secret under the current ENCRYPTION_KEY
//	pi-durable-platform version   print the version
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ncecere/pi-durable-platform/internal/app"
	"github.com/ncecere/pi-durable-platform/internal/buildinfo"
	"github.com/ncecere/pi-durable-platform/internal/config"
	"github.com/ncecere/pi-durable-platform/internal/doctor"
	"github.com/ncecere/pi-durable-platform/internal/rekey"
)

const usage = `usage: pi-durable-platform <command>

commands:
  serve     HTTP API, embedded UI and background loops
  migrate   apply database migrations and exit
  doctor    check the configuration, Postgres, migrations and the agents
            database (--json, --help)
  prune-agent-storage
            remove agent storage no agent owns (--dry-run, --help)
  prune-roles
            drop agent database roles no agent owns, left by a dropped
            agents database (--dry-run, --help)
  rekey     re-seal every stored secret under the current ENCRYPTION_KEY
            (--dry-run, --batch, --help)
  version   print the version

Configuration comes from environment variables and the optional YAML file
named by PDP_CONFIG_FILE. See .env.example.
`

// errChecksFailed makes `doctor` exit non-zero after printing its report.
var errChecksFailed = errors.New("some checks failed")

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "pi-durable-platform:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	cmd := app.ModeServe
	if len(args) > 0 {
		cmd = args[0]
	}
	switch cmd {
	case "version", "--version":
		fmt.Println(buildinfo.String())
		return nil
	case "help", "-h", "--help":
		fmt.Print(usage)
		return nil
	case "doctor":
		return runDoctor(args[1:], os.Stdout)
	case "prune-agent-storage":
		return runPrune(args[1:], os.Stdout)
	case "prune-roles":
		return runPruneRoles(args[1:], os.Stdout)
	case "rekey":
		return runRekey(args[1:], os.Stdout)
	case app.ModeServe, app.ModeMigrate:
	default:
		fmt.Fprint(os.Stderr, usage)
		return fmt.Errorf("unknown command %q", cmd)
	}

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("configuration:\n%w", err)
	}
	log := newLogger(os.Stdout, cfg.LogLevel, cfg.LogFormat)
	// Packages that log through slog's package functions (httpx.Internal)
	// write in the same structured format as everything else.
	slog.SetDefault(log)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return app.Run(ctx, cmd, cfg, log)
}

func newLogger(w io.Writer, level, format string) *slog.Logger {
	var lvl slog.Level
	_ = lvl.UnmarshalText([]byte(level))
	opts := &slog.HandlerOptions{Level: lvl}
	if format == "text" {
		return slog.New(slog.NewTextHandler(w, opts))
	}
	return slog.New(slog.NewJSONHandler(w, opts))
}

// runDoctor implements `pi-durable-platform doctor [flags]`. It uses the same
// configuration as the other commands, so in a pod (the image has no shell):
// kubectl exec deploy/pi-durable-platform -- /pi-durable-platform doctor
func runDoctor(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print the report as JSON")
	mode := fs.String("mode", "serve", "process mode whose configuration is validated: serve or migrate")
	timeout := fs.Duration("timeout", 10*time.Second, "time limit of each check")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), "usage: pi-durable-platform doctor [--json] [--mode serve|migrate] [--timeout 10s]\n\n"+
			"Checks the configuration (including the production safety checks), then\n"+
			"PostgreSQL and the migration state, then the agents database\n"+
			"(AGENTS_DATABASE_URL: reachable, and its role may provision agents).\n"+
			"Exits 1 if any check fails.\n\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); errors.Is(err, flag.ErrHelp) {
		return nil
	} else if err != nil {
		return err
	}
	if *mode != app.ModeServe && *mode != app.ModeMigrate {
		return fmt.Errorf("--mode must be serve or migrate")
	}
	var rep doctor.Report
	if cfg, err := config.Load(); err != nil {
		rep = doctor.ConfigLoadFailed(*mode, err)
	} else {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		rep = doctor.Run(ctx, cfg, doctor.Options{Mode: *mode, Timeout: *timeout})
	}
	write := rep.WriteText
	if *asJSON {
		write = rep.WriteJSON
	}
	if err := write(stdout); err != nil {
		return err
	}
	if !rep.OK {
		return errChecksFailed
	}
	return nil
}

// runPrune implements `pi-durable-platform prune-agent-storage [--dry-run]`.
func runPrune(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("prune-agent-storage", flag.ContinueOnError)
	dryRun := fs.Bool("dry-run", false, "list what would be removed without removing it")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), "usage: pi-durable-platform prune-agent-storage [--dry-run]\n\n"+
			"Removes agent storage (schema and role in the agents database) that no agent owns,\n"+
			"left by a failed deletion or a migration. Run it while no agents are being created.\n\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); errors.Is(err, flag.ErrHelp) {
		return nil
	} else if err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("configuration:\n%w", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log := newLogger(io.Discard, cfg.LogLevel, cfg.LogFormat)
	names, err := app.PruneAgentStorage(ctx, cfg, log, *dryRun)
	for _, n := range names {
		fmt.Fprintln(stdout, n)
	}
	if err != nil {
		return err
	}
	verb := "removed"
	if *dryRun {
		verb = "would remove"
	}
	fmt.Fprintf(stdout, "%s %d unowned agent storage name(s)\n", verb, len(names))
	return nil
}

// runPruneRoles implements `pi-durable-platform prune-roles [--dry-run]`.
func runPruneRoles(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("prune-roles", flag.ContinueOnError)
	dryRun := fs.Bool("dry-run", false, "list what would be dropped without dropping it")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), "usage: pi-durable-platform prune-roles [--dry-run]\n\n"+
			"Drops agent database roles (agent_...) that belong to no agent and have no schema in the\n"+
			"agents database. Roles are server-wide, so a dropped or recreated agents database leaves\n"+
			"them behind. Only roles made like the platform makes them are dropped: a role that owns\n"+
			"objects or has privileges anywhere on the server, has open sessions, or has other\n"+
			"attributes is left alone and listed with the reason. Run it while no agents are being\n"+
			"created, with --dry-run first.\n\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); errors.Is(err, flag.ErrHelp) {
		return nil
	} else if err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("configuration:\n%w", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log := newLogger(io.Discard, cfg.LogLevel, cfg.LogFormat)
	dropped, skipped, err := app.PruneRoles(ctx, cfg, log, *dryRun)
	for _, n := range dropped {
		fmt.Fprintln(stdout, n)
	}
	for _, s := range skipped {
		fmt.Fprintf(stdout, "kept %s: %s\n", s.Name, s.Reason)
	}
	if err != nil {
		return err
	}
	verb := "dropped"
	if *dryRun {
		verb = "would drop"
	}
	fmt.Fprintf(stdout, "%s %d orphaned agent role(s), kept %d\n", verb, len(dropped), len(skipped))
	return nil
}

// runRekey implements `pi-durable-platform rekey [--dry-run] [--batch N]`.
func runRekey(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("rekey", flag.ContinueOnError)
	dryRun := fs.Bool("dry-run", false, "count the values under each key and check they open, without changing anything")
	batch := fs.Int("batch", rekey.DefaultBatch, "rows re-sealed per transaction")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), "usage: pi-durable-platform rekey [--dry-run] [--batch N]\n\n"+
			"Re-seals every stored secret (model provider keys and headers, MCP server and\n"+
			"thread MCP server headers, each agent's database password and runtime token)\n"+
			"under ENCRYPTION_KEY, so that ENCRYPTION_KEY_PREVIOUS can be removed. Rotation:\n"+
			"set the new key as ENCRYPTION_KEY and the old one as ENCRYPTION_KEY_PREVIOUS,\n"+
			"restart, run this, check `doctor` reports nothing under the previous key.\n\n"+
			"Safe while the platform is serving, and to repeat or resume after an interrupt.\n"+
			"If a value opens with neither key, nothing is changed. Writes one audit entry.\n\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); errors.Is(err, flag.ErrHelp) {
		return nil
	} else if err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("rekey takes no arguments (got %q)", fs.Arg(0))
	}
	if *batch < 1 || *batch > rekey.MaxBatch {
		return fmt.Errorf("--batch must be from 1 to %d", rekey.MaxBatch)
	}
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("configuration:\n%w", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	res, err := app.Rekey(ctx, cfg, rekey.Options{DryRun: *dryRun, Batch: *batch})
	writeRekey(stdout, res)
	return err
}

// writeRekey prints the per-column counts: what is under each key, and what
// a run did.
func writeRekey(w io.Writer, res rekey.Result) {
	if len(res.Columns) == 0 {
		return
	}
	fmt.Fprintf(w, "%-34s %8s %9s %11s", "column", "current", "previous", "unreadable")
	if !res.DryRun {
		fmt.Fprintf(w, " %10s %8s", "re-sealed", "changed")
	}
	fmt.Fprintln(w)
	for _, c := range res.Columns {
		fmt.Fprintf(w, "%-34s %8d %9d %11d", c.Label(), c.Current, c.Previous, c.Unreadable)
		if !res.DryRun {
			fmt.Fprintf(w, " %10d %8d", c.Resealed, c.Changed)
		}
		fmt.Fprintln(w)
	}
	current, previous, unreadable, resealed, changed := res.Totals()
	switch {
	case res.DryRun:
		fmt.Fprintf(w, "\ndry run: %d value(s) under ENCRYPTION_KEY, %d under the previous key (a run would re-seal them), %d unreadable\n", current, previous, unreadable)
	default:
		fmt.Fprintf(w, "\nre-sealed %d value(s) under ENCRYPTION_KEY\n", resealed)
		if changed > 0 {
			fmt.Fprintf(w, "%d more were saved by someone else meanwhile and left as they saved them: run again to check\n", changed)
		}
	}
}
