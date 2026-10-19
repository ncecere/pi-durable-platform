// Package app wires dependencies together and runs one process mode.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ncecere/pi-durable-platform/internal/agenthost"
	"github.com/ncecere/pi-durable-platform/internal/agenthost/launcher"
	"github.com/ncecere/pi-durable-platform/internal/agents"
	"github.com/ncecere/pi-durable-platform/internal/apikeys"
	"github.com/ncecere/pi-durable-platform/internal/auth"
	"github.com/ncecere/pi-durable-platform/internal/buildinfo"
	"github.com/ncecere/pi-durable-platform/internal/config"
	"github.com/ncecere/pi-durable-platform/internal/httpapi"
	"github.com/ncecere/pi-durable-platform/internal/llm"
	"github.com/ncecere/pi-durable-platform/internal/mcp"
	"github.com/ncecere/pi-durable-platform/internal/oauth"
	"github.com/ncecere/pi-durable-platform/internal/platform"
	"github.com/ncecere/pi-durable-platform/internal/ratelimit"
	"github.com/ncecere/pi-durable-platform/internal/secrets"
	"github.com/ncecere/pi-durable-platform/internal/serviceaccounts"
	"github.com/ncecere/pi-durable-platform/internal/store"
	"github.com/ncecere/pi-durable-platform/internal/teamnet"
	"github.com/ncecere/pi-durable-platform/internal/teams"
	"github.com/ncecere/pi-durable-platform/web"
)

// Modes.
const (
	ModeServe   = "serve"   // HTTP API, embedded UI and background loops
	ModeMigrate = "migrate" // apply migrations and exit
)

// dbConnectWait is how long a process waits for Postgres at start-up.
const dbConnectWait = 3 * time.Minute

// cleanupInterval is how often expired sessions, abandoned sign-ins and
// expired OAuth codes and tokens are deleted.
const cleanupInterval = 15 * time.Minute

// Run starts the given mode and blocks until ctx is cancelled (or the mode
// completes, for migrate). Shutdown is bounded by SHUTDOWN_TIMEOUT.
func Run(ctx context.Context, mode string, cfg config.Config, log *slog.Logger) error {
	if err := cfg.Validate(mode); err != nil {
		return fmt.Errorf("invalid configuration:\n%w", err)
	}
	log = log.With("mode", mode)
	log.Info("starting", "version", buildinfo.Version, "commit", buildinfo.Commit)

	pool, err := store.OpenWait(ctx, cfg.DatabaseURL, dbConnectWait, log)
	if err != nil {
		return err
	}
	defer pool.Close()

	if mode == ModeMigrate || cfg.MigrateOnStart {
		if err := store.Migrate(ctx, pool, log); err != nil {
			return err
		}
		if mode == ModeMigrate {
			log.Info("migrations complete")
			return nil
		}
	}
	if st, err := store.Migrations(ctx, pool); err != nil {
		return fmt.Errorf("migration state: %w", err)
	} else if st.Pending {
		return fmt.Errorf("the database is at migration %d but this binary needs %d: run `pi-durable-platform migrate` (or set MIGRATE_ON_START=true)", st.Current, st.Latest)
	}

	if cfg.Agents.CreateDatabase {
		if err := ensureAgentsDatabase(ctx, pool, cfg.Agents.DatabaseURL, log); err != nil {
			return err
		}
	}
	deps, err := NewDeps(cfg, pool, log)
	if err != nil {
		return err
	}
	defer deps.Agents.Close()
	if err := prepareAgents(ctx, deps.Agents, log); err != nil {
		return fmt.Errorf("agents database: %w", err)
	}
	health := httpapi.NewHealth(map[string]func(context.Context) error{"postgres": pool.Ping, "agents-postgres": deps.Agents.Ping})
	deps.Health = health
	deps.Web, deps.WebBuilt = web.Assets()
	if !deps.WebBuilt {
		log.Warn("web UI not built; run `make web` before building")
	}
	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           httpapi.NewAPIHandler(deps),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		// No WriteTimeout: responses may stream (agent output). Handlers
		// bound their own work with contexts.
		BaseContext: func(net.Listener) context.Context { return context.WithoutCancel(ctx) },
	}
	srv.RegisterOnShutdown(deps.Agents.Host().CloseStreams)
	// The gateway agents call (the model proxy), on its own listener so the
	// agents' network reaches nothing else of the platform.
	gw := &http.Server{
		Addr:              cfg.Agents.GatewayAddr,
		Handler:           httpapi.NewGatewayHandler(deps),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return context.WithoutCancel(ctx) },
	}
	loopCtx, stopLoops := context.WithCancel(ctx)
	defer stopLoops()
	go cleanupLoop(loopCtx, deps.Auth, deps.OAuth, deps.Agents, log)
	go rateCounterLoop(loopCtx, deps.RateStore, log)
	go deps.Agents.Host().ReapLoop(loopCtx, 30*time.Second)
	go deps.Agents.RunThreadEvents(loopCtx, log.With("component", "thread-events"))
	if err := serve(ctx, log, srv, gw); err != nil {
		return err
	}
	stopLoops()
	err = shutdown(cfg, log, health, srv, gw)
	// Local runtimes are child processes: stop them gracefully with the
	// platform. Kubernetes agents keep running and hibernate on their own.
	if l, ok := deps.Agents.Host().Launcher().(*launcher.Local); ok {
		stopCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		l.StopAll(stopCtx)
		cancel()
	}
	return err
}

// keyPeppers are API_KEY_PEPPER and API_KEY_PEPPER_PREVIOUS, decoded.
type keyPeppers struct{ Current, Previous []byte }

func peppers(cfg config.Config) (keyPeppers, error) {
	var p keyPeppers
	var err error
	if p.Current, err = secrets.ParseKey(cfg.APIKeyPepper); err != nil {
		return p, fmt.Errorf("API_KEY_PEPPER: %w", err)
	}
	if cfg.APIKeyPepperPrevious != "" {
		if p.Previous, err = secrets.ParseKey(cfg.APIKeyPepperPrevious); err != nil {
			return p, fmt.Errorf("API_KEY_PEPPER_PREVIOUS: %w", err)
		}
	}
	return p, nil
}

// rateCounterLoop deletes old rate limit windows every minute (any replica;
// deletes are idempotent).
func rateCounterLoop(ctx context.Context, st ratelimit.Store, log *slog.Logger) {
	pg, ok := st.(*ratelimit.PostgresStore)
	if !ok {
		return
	}
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if _, err := pg.Cleanup(ctx); err != nil && ctx.Err() == nil {
			log.Warn("rate limit counter clean-up failed", "err", err)
		}
	}
}

// NewDeps builds the services the HTTP API needs (Health and Web are left
// to the caller). The configuration must be valid for serve.
func NewDeps(cfg config.Config, pool *pgxpool.Pool, log *slog.Logger) (httpapi.Deps, error) {
	box, err := secretBox(cfg)
	if err != nil {
		return httpapi.Deps{}, err
	}
	pep, err := peppers(cfg)
	if err != nil {
		return httpapi.Deps{}, err
	}
	// Rate limits are counted in PostgreSQL, shared by every replica.
	rates := ratelimit.NewPostgres(pool)
	keys := apikeys.New(pool, pep.Current)
	sas := serviceaccounts.New(pool, pep.Current)
	if pep.Previous != nil {
		keys.SetPreviousPepper(pep.Previous)
		sas.SetPreviousPepper(pep.Previous)
	}
	authSvc := auth.NewService(cfg, pool, log)
	authSvc.SetRateStore(rates)
	// OAuth Connect: apps act as the people who connect them. Its tokens are
	// digested under the API key pepper, like keys. The development allowance
	// lets a metadata document live on this machine.
	connect := oauth.New(pool, oauth.Config{Issuer: cfg.AppURL, Pepper: pep.Current, PepperPrevious: pep.Previous,
		DevAllowance: cfg.DevAuth && cfg.LoopbackAppURL()})
	connect.Log = log
	models := llm.New(pool, box)
	// Team providers are reached through the address guard; the development
	// allowance lets them live on this machine.
	models.SetTeamNetwork(llm.TeamNetwork{DevAllowance: cfg.DevAuth && cfg.LoopbackAppURL()})
	hosts, err := mcp.ParseHostPatterns(cfg.Agents.ClientMCPHosts)
	if err != nil {
		return httpapi.Deps{}, fmt.Errorf("AGENTS_CLIENT_MCP_HOSTS: %w", err)
	}
	servers := mcp.New(pool, box, mcp.ClientPolicy{Hosts: hosts, Mode: cfg.Agents.ClientMCPMode})
	// Teams' own MCP servers are reached through the address guard too, with
	// the same development allowance; changing the private networks closes
	// the connections kept to them.
	servers.SetTeamNetwork(teamnet.Config{DevAllowance: cfg.DevAuth && cfg.LoopbackAppURL()})
	models.OnSettingsChanged(servers.CloseTeamConnections)
	agentSvc, err := newAgents(cfg, pool, box, models, servers, log)
	if err != nil {
		return httpapi.Deps{}, err
	}
	return httpapi.Deps{
		Config:          cfg,
		Pool:            pool,
		Log:             log,
		Auth:            authSvc,
		Teams:           teams.NewService(pool),
		Platform:        platform.NewService(pool),
		APIKeys:         keys,
		ServiceAccounts: sas,
		OAuth:           connect,
		Agents:          agentSvc,
		Models:          models,
		MCP:             servers,
		Secrets:         box,
		RateStore:       rates,
	}, nil
}

// secretBox opens ENCRYPTION_KEY (and ENCRYPTION_KEY_PREVIOUS).
func secretBox(cfg config.Config) (*secrets.Box, error) {
	key, err := secrets.ParseKey(cfg.EncryptionKey)
	if err != nil {
		return nil, fmt.Errorf("ENCRYPTION_KEY: %w", err)
	}
	var previous [][]byte
	if cfg.EncryptionKeyPrevious != "" {
		prev, err := secrets.ParseKey(cfg.EncryptionKeyPrevious)
		if err != nil {
			return nil, fmt.Errorf("ENCRYPTION_KEY_PREVIOUS: %w", err)
		}
		previous = append(previous, prev)
	}
	return secrets.New(key, previous...)
}

// cleanupLoop deletes expired sessions and sign-in states, and expired
// OAuth codes and tokens and unused registered OAuth clients, threads nobody
// used and expired thread MCP servers. Deletes are idempotent, so every
// replica may run it; no leader election is needed.
func cleanupLoop(ctx context.Context, a *auth.Service, o *oauth.Service, ag *agents.Service, log *slog.Logger) {
	t := time.NewTicker(cleanupInterval)
	defer t.Stop()
	for {
		sessions, logins, err := a.Cleanup(ctx)
		switch {
		case err != nil && ctx.Err() == nil:
			log.Warn("session cleanup failed", "err", err)
		case sessions+logins > 0:
			log.Debug("expired sessions deleted", "sessions", sessions, "loginStates", logins)
		}
		n, err := o.Cleanup(ctx)
		switch {
		case err != nil && ctx.Err() == nil:
			log.Warn("oauth cleanup failed", "err", err)
		case n > 0:
			log.Debug("expired oauth codes, tokens and clients deleted", "rows", n)
		}
		n, err = ag.Cleanup(ctx)
		switch {
		case err != nil && ctx.Err() == nil:
			log.Warn("agent thread cleanup failed", "err", err)
		case n > 0:
			log.Debug("unused threads and expired thread MCP servers deleted", "rows", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// serve runs srv until ctx is cancelled or the server fails.
func serve(ctx context.Context, log *slog.Logger, servers ...*http.Server) error {
	serveErr := make(chan error, len(servers))
	for i, srv := range servers {
		go func() {
			if i == 0 {
				log.Info("http listening", "addr", srv.Addr)
			} else {
				log.Info("agent gateway listening", "addr", srv.Addr)
			}
			if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				serveErr <- fmt.Errorf("http server %s: %w", srv.Addr, err)
			}
		}()
	}
	select {
	case err := <-serveErr:
		return err
	case <-ctx.Done():
	}
	return nil
}

// shutdown drains traffic before stopping: readiness fails first so the load
// balancer removes the pod, then in-flight requests get SHUTDOWN_TIMEOUT to
// finish.
func shutdown(cfg config.Config, log *slog.Logger, health *httpapi.Health, servers ...*http.Server) error {
	log.Info("shutting down", "drain_delay", cfg.ShutdownDelay, "timeout", cfg.ShutdownTimeout)
	health.SetDraining()
	time.Sleep(cfg.ShutdownDelay)

	ctx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	var errs []error
	for _, srv := range servers {
		if err := srv.Shutdown(ctx); err != nil {
			errs = append(errs, fmt.Errorf("http shutdown (%s): %w", srv.Addr, err))
		}
	}
	if len(errs) == 0 {
		log.Info("shutdown complete")
	}
	return errors.Join(errs...)
}

// newAgents builds the agents service: a lazily connected pool on the agents
// database, the provisioner, the configured launcher and the runtime manager.
func newAgents(cfg config.Config, pool *pgxpool.Pool, box *secrets.Box, models *llm.Service, servers *mcp.Service, log *slog.Logger) (*agents.Service, error) {
	pcfg, err := pgxpool.ParseConfig(cfg.Agents.DatabaseURL)
	if err != nil {
		return nil, fmt.Errorf("AGENTS_DATABASE_URL: %w", err)
	}
	pcfg.MaxConns = 4
	agentsPool, err := pgxpool.NewWithConfig(context.Background(), pcfg)
	if err != nil {
		return nil, fmt.Errorf("AGENTS_DATABASE_URL: %w", err)
	}
	var l launcher.Launcher
	switch cfg.Agents.Launcher {
	case config.LauncherKubernetes:
		k, err := launcher.NewInCluster(cfg.Agents.Namespace, cfg.Agents.Image, log.With("component", "launcher"))
		if err != nil {
			agentsPool.Close()
			return nil, err
		}
		k.RuntimeClassName, k.ImagePullSecret = cfg.Agents.RuntimeClass, cfg.Agents.ImagePullSecret
		l = k
	case config.LauncherAgentSandbox:
		k, err := launcher.NewInCluster(cfg.Agents.Namespace, cfg.Agents.Image, log.With("component", "launcher"))
		if err != nil {
			agentsPool.Close()
			return nil, err
		}
		k.RuntimeClassName, k.ImagePullSecret = cfg.Agents.RuntimeClass, cfg.Agents.ImagePullSecret
		s := launcher.NewAgentSandbox(k, cfg.Agents.SandboxWarmPool, cfg.Agents.SandboxActivationToken, cfg.Agents.SandboxActivationSecret)
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		if err := s.Prepare(ctx); err != nil {
			// Not fatal: activation retries it when an agent wakes.
			log.Warn("prepare the warm pool's activation secret", "err", err)
		}
		cancel()
		l = s
	default:
		dir, err := filepath.Abs(cfg.Agents.RuntimeDir)
		if err != nil {
			agentsPool.Close()
			return nil, err
		}
		l = launcher.NewLocal(dir, log.With("component", "launcher"))
	}
	host := agenthost.New(l, cfg.Agents.IdleTimeout, log.With("component", "agenthost"))
	return agents.New(pool, agentsPool, box, host, models, servers, cfg.Agents), nil
}

// ensureAgentsDatabase creates the agents database through the platform's
// connection if it is missing (AGENTS_CREATE_DATABASE, development only).
func ensureAgentsDatabase(ctx context.Context, pool *pgxpool.Pool, agentsURL string, log *slog.Logger) error {
	pcfg, err := pgxpool.ParseConfig(agentsURL)
	if err != nil {
		return fmt.Errorf("AGENTS_DATABASE_URL: %w", err)
	}
	name := pcfg.ConnConfig.Database
	var exists bool
	if err := pool.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)", name).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return nil
	}
	if _, err := pool.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		return fmt.Errorf("create agents database %s: %w", name, err)
	}
	log.Info("created agents database", "database", name)
	return nil
}

// PruneAgentStorage removes agent storage that no agent owns (the
// prune-agent-storage command).
func PruneAgentStorage(ctx context.Context, cfg config.Config, log *slog.Logger, dryRun bool) ([]string, error) {
	pool, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return nil, err
	}
	defer pool.Close()
	box, err := secretBox(cfg)
	if err != nil {
		return nil, err
	}
	svc, err := newAgents(cfg, pool, box, llm.New(pool, box), mcp.New(pool, box, mcp.ClientPolicy{}), log)
	if err != nil {
		return nil, err
	}
	defer svc.Close()
	return svc.PruneStorage(ctx, dryRun)
}

// prepareAgents hardens the agents database, waiting for it like the platform
// database (OpenWait): on a fresh install it may be created a little after
// the server (for example a CloudNativePG Database resource).
func prepareAgents(ctx context.Context, svc *agents.Service, log *slog.Logger) error {
	ctx, cancel := context.WithTimeout(ctx, dbConnectWait)
	defer cancel()
	wait := time.Second
	for {
		err := svc.Prepare(ctx)
		if err == nil {
			return nil
		}
		log.Warn("agents database not ready; retrying", "err", err, "in", wait)
		select {
		case <-ctx.Done():
			return err
		case <-time.After(wait):
		}
		wait = min(wait*2, 15*time.Second)
	}
}
