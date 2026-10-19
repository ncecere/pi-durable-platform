package launcher

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"
)

// Local runs each agent as a child process of the platform, on a loopback
// port. For development and single-host installs; the Kubernetes backend is
// the isolated one.
type Local struct {
	// RuntimeDir is the runtime package (contains package.json and node_modules).
	RuntimeDir string
	// StartTimeout bounds how long a runtime may take to answer /healthz.
	StartTimeout time.Duration
	// StopTimeout bounds graceful shutdown before the process is killed.
	StopTimeout time.Duration
	Log         *slog.Logger

	mu     sync.Mutex
	agents map[string]*localAgent
}

type localAgent struct {
	starting chan struct{} // closed when the start attempt finished
	startErr error
	endpoint Endpoint
	cmd      *exec.Cmd
	exited   chan struct{}
	exitCode int
}

func NewLocal(runtimeDir string, log *slog.Logger) *Local {
	return &Local{RuntimeDir: runtimeDir, StartTimeout: 60 * time.Second, StopTimeout: 20 * time.Second, Log: log, agents: map[string]*localAgent{}}
}

func (l *Local) Kind() string { return "local" }

func (l *Local) Ensure(ctx context.Context, spec Spec) (Endpoint, error) {
	for {
		l.mu.Lock()
		a := l.agents[spec.AgentID]
		if a != nil {
			l.mu.Unlock()
			select {
			case <-a.starting:
				report(ctx, StartRunning)
			default:
				// Another call is starting it: this one waits for that.
				report(ctx, StartCold)
				select {
				case <-a.starting:
				case <-ctx.Done():
					return Endpoint{}, ctx.Err()
				}
			}
			select {
			case <-a.exited:
				// Exited (crash or stop): forget it and start again.
				l.forget(spec.AgentID, a)
				continue
			default:
			}
			if a.startErr != nil {
				l.forget(spec.AgentID, a)
				return Endpoint{}, a.startErr
			}
			return a.endpoint, nil
		}
		a = &localAgent{starting: make(chan struct{}), exited: make(chan struct{})}
		l.agents[spec.AgentID] = a
		l.mu.Unlock()
		report(ctx, StartCold)
		a.startErr = l.start(spec, a)
		close(a.starting)
		if a.startErr != nil {
			l.forget(spec.AgentID, a)
			return Endpoint{}, a.startErr
		}
		return a.endpoint, nil
	}
}

// Lookup returns the endpoint of a runtime this process started and that
// has not exited. A runtime another platform process started is not known:
// the local backend is for one process.
func (l *Local) Lookup(_ context.Context, spec Spec) (Endpoint, bool, error) {
	ep, ok := l.Running()[spec.AgentID]
	return ep, ok, nil
}

func (l *Local) forget(id string, a *localAgent) {
	l.mu.Lock()
	if l.agents[id] == a {
		delete(l.agents, id)
	}
	l.mu.Unlock()
}

func (l *Local) start(spec Spec, a *localAgent) error {
	port, err := freePort()
	if err != nil {
		return err
	}
	tsx := filepath.Join(l.RuntimeDir, "node_modules", ".bin", "tsx")
	cmd := exec.Command(tsx, "src/main.ts")
	cmd.Dir = l.RuntimeDir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// Only what the runtime needs: no platform secrets leak into the agent.
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
		"NODE_ENV=production",
		"AGENT_ID=" + spec.AgentID,
		"AGENT_SCHEMA=" + spec.Schema,
		"AGENT_DATABASE_URL=" + spec.DatabaseURL,
		"RUNTIME_TOKEN=" + spec.Token,
		"HOST=127.0.0.1",
		"PORT=" + strconv.Itoa(port),
	}
	if spec.Name != "" {
		cmd.Env = append(cmd.Env, "AGENT_NAME="+spec.Name)
	}
	if spec.Instructions != "" {
		cmd.Env = append(cmd.Env, "AGENT_INSTRUCTIONS="+spec.Instructions)
	}
	for k, v := range spec.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start agent runtime: %w", err)
	}
	log := l.Log.With("agent", spec.AgentID, "pid", cmd.Process.Pid)
	go pipeLog(stdout, log, slog.LevelInfo)
	go pipeLog(stderr, log, slog.LevelWarn)
	a.cmd = cmd
	go func() {
		err := cmd.Wait()
		a.exitCode = cmd.ProcessState.ExitCode()
		if err != nil {
			log.Info("agent runtime exited", "code", a.exitCode)
		}
		close(a.exited)
	}()
	a.endpoint = Endpoint{BaseURL: "http://127.0.0.1:" + strconv.Itoa(port), Token: spec.Token}
	ctx, cancel := context.WithTimeout(context.Background(), l.StartTimeout)
	defer cancel()
	if err := waitHealthy(ctx, a.endpoint.BaseURL, a.exited); err != nil {
		select {
		case <-a.exited:
			if a.exitCode == 3 {
				return ErrOwnedElsewhere
			}
		default:
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		return err
	}
	log.Info("agent runtime started", "endpoint", a.endpoint.BaseURL)
	return nil
}

func (l *Local) Stop(ctx context.Context, agentID string) error {
	l.mu.Lock()
	a := l.agents[agentID]
	l.mu.Unlock()
	if a == nil {
		return nil
	}
	select {
	case <-a.starting:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer l.forget(agentID, a)
	if a.cmd == nil || a.cmd.Process == nil {
		return nil
	}
	_ = syscall.Kill(-a.cmd.Process.Pid, syscall.SIGTERM)
	select {
	case <-a.exited:
		return nil
	case <-time.After(l.StopTimeout):
		_ = syscall.Kill(-a.cmd.Process.Pid, syscall.SIGKILL)
		<-a.exited
		return errors.New("agent runtime did not stop in time; killed")
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Remove stops the agent; a local runtime keeps nothing else.
func (l *Local) Remove(ctx context.Context, agentID string) error { return l.Stop(ctx, agentID) }

// StopAll stops every runtime (platform shutdown).
func (l *Local) StopAll(ctx context.Context) {
	var wg sync.WaitGroup
	for id := range l.Running() {
		wg.Add(1)
		go func() { defer wg.Done(); _ = l.Stop(ctx, id) }()
	}
	wg.Wait()
}

func (l *Local) Running() map[string]Endpoint {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := map[string]Endpoint{}
	for id, a := range l.agents {
		select {
		case <-a.starting:
		default:
			continue
		}
		select {
		case <-a.exited:
			continue
		default:
		}
		if a.startErr == nil {
			out[id] = a.endpoint
		}
	}
	return out
}

func freePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port, nil
}

func pipeLog(r io.Reader, log *slog.Logger, level slog.Level) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		log.Log(context.Background(), level, "runtime: "+scanner.Text())
	}
}
