package launcher

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// AgentSandbox runs agents with Kubernetes Agent Sandbox
// (https://agent-sandbox.sigs.k8s.io), in one of two modes:
//
//   - A Sandbox per agent (WarmPool empty): the same hardened pod as the
//     Kubernetes launcher, wrapped in a Sandbox named after the agent.
//     Hibernating suspends it (operatingMode Suspended: the pod goes, the
//     Sandbox and the agent's Secret stay); waking resumes it.
//   - Claims on a warm pool (WarmPool set): a SandboxWarmPool keeps standby
//     runtimes started. Waking an agent claims one (a SandboxClaim named after
//     the agent) and activates it as the agent over HTTP, passing its
//     configuration with the pool's activation token; nothing about the agent
//     is stored in Kubernetes. Hibernating releases the claim, which deletes
//     that runtime; the pool starts a replacement.
//
// The agent's state is in PostgreSQL either way, so a hibernated agent can
// wake in any runtime. It needs, in Namespace: sandboxes (cold mode: plus
// secrets, and get on pods to see a runtime that exited because the agent is
// owned elsewhere) or sandboxclaims (warm mode).
type AgentSandbox struct {
	*Kubernetes
	// WarmPool names the SandboxWarmPool to claim runtimes from.
	WarmPool string
	// ActivationToken is the pool's RUNTIME_ACTIVATION_TOKEN.
	ActivationToken string

	http *http.Client
}

const (
	sandboxGroup   = "agents.x-k8s.io"
	extensionGroup = "extensions.agents.x-k8s.io"
	sandboxVersion = "v1beta1"
)

// NewAgentSandbox builds the backend from the pod's service-account
// credentials. warmPool and activationToken select warm mode.
func NewAgentSandbox(k *Kubernetes, warmPool, activationToken string) *AgentSandbox {
	return &AgentSandbox{Kubernetes: k, WarmPool: warmPool, ActivationToken: activationToken, http: &http.Client{Timeout: 30 * time.Second}}
}

func (s *AgentSandbox) Kind() string {
	if s.WarmPool != "" {
		return "agent-sandbox (warm pool " + s.WarmPool + ")"
	}
	return "agent-sandbox"
}

func (s *AgentSandbox) Ensure(ctx context.Context, spec Spec) (Endpoint, error) {
	l := s.lock(spec.AgentID)
	l.Lock()
	defer l.Unlock()
	ctx, cancel := context.WithTimeout(ctx, s.StartTimeout)
	defer cancel()
	if s.WarmPool != "" {
		return s.ensureClaimed(ctx, spec)
	}
	return s.ensureSandbox(ctx, spec)
}

func (s *AgentSandbox) Stop(ctx context.Context, agentID string) error {
	name := podName(agentID)
	if s.WarmPool != "" {
		return ignoreNotFound(s.do(ctx, http.MethodDelete, s.claimPath(name), nil, nil))
	}
	return ignoreNotFound(s.do(ctx, http.MethodPatch, s.sandboxPath(name), map[string]any{"spec": map[string]any{"operatingMode": "Suspended"}}, nil))
}

// Remove deletes the agent's claim or Sandbox, and its Secret (in warm mode,
// one left by another launcher).
func (s *AgentSandbox) Remove(ctx context.Context, agentID string) error {
	name := podName(agentID)
	resource := s.sandboxPath(name)
	if s.WarmPool != "" {
		resource = s.claimPath(name)
	}
	if err := ignoreNotFound(s.do(ctx, http.MethodDelete, resource, nil, nil)); err != nil {
		return err
	}
	return ignoreNotFound(s.do(ctx, http.MethodDelete, s.path("secrets", name), nil, nil))
}

// Running lists agents whose runtime is ready. In warm mode a ready claim
// counts even if its runtime restarted and awaits re-activation: the reaper
// then skips it (its status call fails) until the next request activates it.
func (s *AgentSandbox) Running() map[string]Endpoint {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out := map[string]Endpoint{}
	selector := "?labelSelector=" + url.QueryEscape(managedLabel+"="+managedBy)
	if s.WarmPool != "" {
		var list struct{ Items []sandboxClaim }
		if err := s.do(ctx, http.MethodGet, s.claimPath("")+selector, nil, &list); err != nil {
			s.Log.Warn("list agent sandbox claims", "err", err)
			return out
		}
		for _, c := range list.Items {
			if ip, ok := c.ready(); ok {
				out[c.Metadata.Labels[agentLabel]] = Endpoint{BaseURL: s.runtimeURL(ip)}
			}
		}
		return out
	}
	var list struct{ Items []sandbox }
	if err := s.do(ctx, http.MethodGet, s.sandboxPath("")+selector, nil, &list); err != nil {
		s.Log.Warn("list agent sandboxes", "err", err)
		return out
	}
	for _, sb := range list.Items {
		if ip, ok := sb.ready(); ok {
			out[sb.Metadata.Labels[agentLabel]] = Endpoint{BaseURL: s.runtimeURL(ip)}
		}
	}
	return out
}

// --- A Sandbox per agent ---

func (s *AgentSandbox) ensureSandbox(ctx context.Context, spec Spec) (Endpoint, error) {
	name := podName(spec.AgentID)
	sb, err := s.getSandbox(ctx, name)
	if err != nil {
		return Endpoint{}, err
	}
	switch {
	case sb == nil:
		if err := s.putSecret(ctx, name, spec); err != nil {
			return Endpoint{}, err
		}
		if err := s.createSandbox(ctx, name, spec); err != nil {
			return Endpoint{}, err
		}
	case sb.Metadata.DeletionTimestamp != nil:
		return Endpoint{}, errors.New("agent sandbox is being deleted")
	case sb.Spec.OperatingMode == "Suspended":
		// The Secret is refreshed in case the agent's settings changed while it slept.
		if err := s.putSecret(ctx, name, spec); err != nil {
			return Endpoint{}, err
		}
		if err := s.do(ctx, http.MethodPatch, s.sandboxPath(name), map[string]any{"spec": map[string]any{"operatingMode": "Running"}}, nil); err != nil {
			return Endpoint{}, err
		}
	default:
		if ip, ok := sb.ready(); ok {
			return Endpoint{BaseURL: s.runtimeURL(ip), Token: spec.Token}, nil
		}
	}
	for {
		sb, err := s.getSandbox(ctx, name)
		if err != nil {
			return Endpoint{}, err
		}
		if sb == nil {
			return Endpoint{}, errors.New("agent sandbox disappeared during start-up")
		}
		if ip, ok := sb.ready(); ok {
			return Endpoint{BaseURL: s.runtimeURL(ip), Token: spec.Token}, nil
		}
		// The runtime exits 3 when another process still owns the agent.
		if p, _ := s.getPod(ctx, name); p != nil && p.ownedElsewhere() {
			return Endpoint{}, ErrOwnedElsewhere
		}
		if err := sleep(ctx, 300*time.Millisecond); err != nil {
			return Endpoint{}, fmt.Errorf("agent sandbox not ready: %w", err)
		}
	}
}

func (s *AgentSandbox) createSandbox(ctx context.Context, name string, spec Spec) error {
	labels, podSpec := s.agentPod(name, spec)
	sb := map[string]any{
		"apiVersion": sandboxGroup + "/" + sandboxVersion, "kind": "Sandbox",
		"metadata": map[string]any{"name": name, "labels": map[string]string{managedLabel: managedBy, agentLabel: spec.AgentID}},
		"spec": map[string]any{
			"podTemplate":   map[string]any{"metadata": map[string]any{"labels": labels}, "spec": podSpec},
			"service":       false,
			"operatingMode": "Running",
		},
	}
	return s.do(ctx, http.MethodPost, s.sandboxPath(""), sb, nil)
}

func (s *AgentSandbox) getSandbox(ctx context.Context, name string) (*sandbox, error) {
	var sb sandbox
	if err := s.do(ctx, http.MethodGet, s.sandboxPath(name), nil, &sb); err != nil {
		if errors.Is(err, errNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &sb, nil
}

// --- Claims on a warm pool ---

func (s *AgentSandbox) ensureClaimed(ctx context.Context, spec Spec) (Endpoint, error) {
	name := podName(spec.AgentID)
	for {
		c, err := s.getClaim(ctx, name)
		if err != nil {
			return Endpoint{}, err
		}
		if c == nil {
			if err := s.createClaim(ctx, name, spec); err != nil && !errors.Is(err, errConflict) {
				return Endpoint{}, err
			}
		} else if c.Metadata.DeletionTimestamp == nil {
			if ip, ok := c.ready(); ok {
				ep := Endpoint{BaseURL: s.runtimeURL(ip), Token: spec.Token}
				err := s.activate(ctx, ep.BaseURL, spec)
				if err == nil {
					return ep, nil
				}
				if !errors.Is(err, ErrOwnedElsewhere) && !errors.Is(err, errNotYet) {
					return Endpoint{}, err
				}
				// The previous runtime is still letting go of the agent, or
				// this one is restarting: try again shortly.
			}
		}
		// Otherwise: a released claim still being deleted, or a claim waiting
		// for its runtime.
		if err := sleep(ctx, 250*time.Millisecond); err != nil {
			return Endpoint{}, fmt.Errorf("agent runtime not ready: %w", err)
		}
	}
}

func (s *AgentSandbox) createClaim(ctx context.Context, name string, spec Spec) error {
	c := map[string]any{
		"apiVersion": extensionGroup + "/" + sandboxVersion, "kind": "SandboxClaim",
		"metadata": map[string]any{"name": name, "labels": map[string]string{managedLabel: managedBy, agentLabel: spec.AgentID}},
		"spec":     map[string]any{"warmPoolRef": map[string]string{"name": s.WarmPool}},
	}
	return s.do(ctx, http.MethodPost, s.claimPath(""), c, nil)
}

func (s *AgentSandbox) getClaim(ctx context.Context, name string) (*sandboxClaim, error) {
	var c sandboxClaim
	if err := s.do(ctx, http.MethodGet, s.claimPath(name), nil, &c); err != nil {
		if errors.Is(err, errNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &c, nil
}

var errNotYet = errors.New("runtime not ready for activation")

// activate makes a claimed standby runtime the agent. It is idempotent: a
// runtime already activated as this agent answers 200.
func (s *AgentSandbox) activate(ctx context.Context, base string, spec Spec) error {
	env := map[string]string{}
	for k, v := range spec.Env {
		env[k] = v
	}
	body, err := json.Marshal(map[string]any{
		"agentId": spec.AgentID, "name": spec.Name, "instructions": spec.Instructions,
		"schema": spec.Schema, "databaseUrl": spec.DatabaseURL, "token": spec.Token, "env": env,
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/activate", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+s.ActivationToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.http.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", errNotYet, err)
	}
	defer resp.Body.Close()
	var out struct {
		Error struct{ Code string } `json:"error"`
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	_ = json.Unmarshal(b, &out)
	switch {
	case resp.StatusCode == http.StatusOK:
		return nil
	case out.Error.Code == "owned_elsewhere":
		return ErrOwnedElsewhere
	case resp.StatusCode == http.StatusConflict:
		// Never expected (claims are per agent); refuse rather than mix agents.
		return errors.New("claimed runtime is already activated as another agent")
	case resp.StatusCode == http.StatusNotFound, resp.StatusCode == http.StatusServiceUnavailable:
		return fmt.Errorf("%w: status %d", errNotYet, resp.StatusCode)
	default:
		return fmt.Errorf("activate agent runtime: status %d %s", resp.StatusCode, out.Error.Code)
	}
}

// --- Resources ---

func (s *AgentSandbox) sandboxPath(name string) string {
	return s.groupPath(sandboxGroup, sandboxVersion, "sandboxes", name)
}

func (s *AgentSandbox) claimPath(name string) string {
	return s.groupPath(extensionGroup, sandboxVersion, "sandboxclaims", name)
}

type condition struct {
	Type   string `json:"type"`
	Status string `json:"status"`
}

func readyIP(conds []condition, ips []string) (string, bool) {
	if len(ips) == 0 || ips[0] == "" {
		return "", false
	}
	for _, c := range conds {
		if c.Type == "Ready" && c.Status == "True" {
			return ips[0], true
		}
	}
	return "", false
}

type objectMeta struct {
	Labels            map[string]string `json:"labels"`
	DeletionTimestamp *string           `json:"deletionTimestamp"`
}

type sandbox struct {
	Metadata objectMeta `json:"metadata"`
	Spec     struct {
		OperatingMode string `json:"operatingMode"`
	} `json:"spec"`
	Status struct {
		Conditions []condition `json:"conditions"`
		PodIPs     []string    `json:"podIPs"`
	} `json:"status"`
}

func (sb *sandbox) ready() (string, bool) {
	if sb.Metadata.DeletionTimestamp != nil || sb.Spec.OperatingMode == "Suspended" {
		return "", false
	}
	return readyIP(sb.Status.Conditions, sb.Status.PodIPs)
}

type sandboxClaim struct {
	Metadata objectMeta `json:"metadata"`
	Status   struct {
		Conditions []condition `json:"conditions"`
		Sandbox    struct {
			Name   string   `json:"name"`
			PodIPs []string `json:"podIPs"`
		} `json:"sandbox"`
	} `json:"status"`
}

func (c *sandboxClaim) ready() (string, bool) {
	if c.Metadata.DeletionTimestamp != nil {
		return "", false
	}
	return readyIP(c.Status.Conditions, c.Status.Sandbox.PodIPs)
}

func ignoreNotFound(err error) error {
	if errors.Is(err, errNotFound) {
		return nil
	}
	return err
}

func sleep(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}
