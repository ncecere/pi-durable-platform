package launcher

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// Kubernetes runs each agent as its own pod: no service-account token, a
// restricted security context, read-only root filesystem, and (optionally) a
// sandboxing RuntimeClass such as gVisor. Secrets reach the pod through a
// per-agent Secret, never the pod spec. It talks to the API server over REST
// with the platform's in-cluster credentials and needs create/get/list/delete
// on pods and secrets in its namespace only.
type Kubernetes struct {
	Namespace string
	Image     string
	// RuntimeClassName selects a sandboxed runtime (empty: the default runtime).
	RuntimeClassName string
	// ImagePullSecret names a registry Secret in the namespace (optional).
	ImagePullSecret string
	StartTimeout    time.Duration
	Log             *slog.Logger

	api       string
	tokenFile string
	client    *http.Client

	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

const (
	runtimePort  = 4100
	managedLabel = "app.kubernetes.io/managed-by"
	managedBy    = "pi-durable-platform"
	agentLabel   = "pi-durable-platform/agent-id"
)

// NewInCluster builds the backend from the pod's service-account credentials.
func NewInCluster(namespace, image string, log *slog.Logger) (*Kubernetes, error) {
	host, port := os.Getenv("KUBERNETES_SERVICE_HOST"), os.Getenv("KUBERNETES_SERVICE_PORT")
	if host == "" || port == "" {
		return nil, errors.New("not running in a Kubernetes cluster (KUBERNETES_SERVICE_HOST unset)")
	}
	const sa = "/var/run/secrets/kubernetes.io/serviceaccount/"
	ca, err := os.ReadFile(sa + "ca.crt")
	if err != nil {
		return nil, fmt.Errorf("service account CA: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca) {
		return nil, errors.New("service account CA: no certificates")
	}
	if namespace == "" {
		b, err := os.ReadFile(sa + "namespace")
		if err != nil {
			return nil, fmt.Errorf("service account namespace: %w", err)
		}
		namespace = strings.TrimSpace(string(b))
	}
	return &Kubernetes{
		Namespace: namespace, Image: image, StartTimeout: 90 * time.Second, Log: log,
		api: "https://" + host + ":" + port, tokenFile: sa + "token",
		client: &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}},
		locks:  map[string]*sync.Mutex{},
	}, nil
}

func (k *Kubernetes) Kind() string { return "kubernetes" }

func podName(agentID string) string { return "agent-" + sanitize(agentID) }

func sanitize(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func (k *Kubernetes) lock(agentID string) *sync.Mutex {
	k.mu.Lock()
	defer k.mu.Unlock()
	l := k.locks[agentID]
	if l == nil {
		l = &sync.Mutex{}
		k.locks[agentID] = l
	}
	return l
}

func (k *Kubernetes) Ensure(ctx context.Context, spec Spec) (Endpoint, error) {
	l := k.lock(spec.AgentID)
	l.Lock()
	defer l.Unlock()
	name := podName(spec.AgentID)
	if p, err := k.getPod(ctx, name); err != nil {
		return Endpoint{}, err
	} else if p != nil {
		if p.deleting() || p.Status.Phase == "Failed" || p.Status.Phase == "Succeeded" {
			if err := k.waitGone(ctx, name); err != nil {
				return Endpoint{}, err
			}
		} else if ip, ok := p.ready(); ok {
			return Endpoint{BaseURL: fmt.Sprintf("http://%s:%d", ip, runtimePort), Token: spec.Token}, nil
		} else {
			return k.waitReady(ctx, name, spec.Token)
		}
	}
	if err := k.putSecret(ctx, name, spec); err != nil {
		return Endpoint{}, err
	}
	if err := k.createPod(ctx, name, spec); err != nil {
		return Endpoint{}, err
	}
	return k.waitReady(ctx, name, spec.Token)
}

func (k *Kubernetes) Stop(ctx context.Context, agentID string) error {
	name := podName(agentID)
	err := k.do(ctx, http.MethodDelete, k.path("pods", name), map[string]any{"gracePeriodSeconds": 30}, nil)
	if errors.Is(err, errNotFound) {
		return nil
	}
	return err
}

// Remove deletes the agent's pod and its Secret (database URL and token).
func (k *Kubernetes) Remove(ctx context.Context, agentID string) error {
	if err := k.Stop(ctx, agentID); err != nil {
		return err
	}
	err := k.do(ctx, http.MethodDelete, k.path("secrets", podName(agentID)), nil, nil)
	if errors.Is(err, errNotFound) {
		return nil
	}
	return err
}

func (k *Kubernetes) Running() map[string]Endpoint {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var list struct{ Items []pod }
	if err := k.do(ctx, http.MethodGet, k.path("pods", "")+"?labelSelector="+managedLabel+"%3D"+managedBy, nil, &list); err != nil {
		k.Log.Warn("list agent pods", "err", err)
		return map[string]Endpoint{}
	}
	out := map[string]Endpoint{}
	for _, p := range list.Items {
		if ip, ok := p.ready(); ok && !p.deleting() {
			// The token is not known here; callers needing it use Ensure.
			out[p.Metadata.Labels[agentLabel]] = Endpoint{BaseURL: fmt.Sprintf("http://%s:%d", ip, runtimePort)}
		}
	}
	return out
}

func (k *Kubernetes) waitReady(ctx context.Context, name, token string) (Endpoint, error) {
	ctx, cancel := context.WithTimeout(ctx, k.StartTimeout)
	defer cancel()
	for {
		p, err := k.getPod(ctx, name)
		if err != nil {
			return Endpoint{}, err
		}
		if p == nil {
			return Endpoint{}, errors.New("agent pod disappeared during start-up")
		}
		if ip, ok := p.ready(); ok {
			return Endpoint{BaseURL: fmt.Sprintf("http://%s:%d", ip, runtimePort), Token: token}, nil
		}
		if p.ownedElsewhere() {
			return Endpoint{}, ErrOwnedElsewhere
		}
		select {
		case <-ctx.Done():
			return Endpoint{}, fmt.Errorf("agent pod not ready: %w", ctx.Err())
		case <-time.After(300 * time.Millisecond):
		}
	}
}

func (k *Kubernetes) waitGone(ctx context.Context, name string) error {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	for {
		p, err := k.getPod(ctx, name)
		if err != nil {
			return err
		}
		if p == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("previous agent pod still terminating: %w", ctx.Err())
		case <-time.After(300 * time.Millisecond):
		}
	}
}

func (k *Kubernetes) putSecret(ctx context.Context, name string, spec Spec) error {
	data := map[string]string{
		"AGENT_DATABASE_URL": spec.DatabaseURL,
		"RUNTIME_TOKEN":      spec.Token,
	}
	for key, v := range spec.Env {
		data[key] = v
	}
	secret := map[string]any{
		"apiVersion": "v1", "kind": "Secret",
		"metadata": map[string]any{"name": name, "labels": map[string]string{managedLabel: managedBy, agentLabel: spec.AgentID}},
		"type":     "Opaque", "stringData": data,
	}
	err := k.do(ctx, http.MethodPut, k.path("secrets", name), secret, nil)
	if errors.Is(err, errNotFound) {
		err = k.do(ctx, http.MethodPost, k.path("secrets", ""), secret, nil)
	}
	return err
}

func (k *Kubernetes) createPod(ctx context.Context, name string, spec Spec) error {
	env := []map[string]any{
		{"name": "AGENT_ID", "value": spec.AgentID},
		{"name": "AGENT_SCHEMA", "value": spec.Schema},
		{"name": "HOST", "value": "0.0.0.0"},
		{"name": "PORT", "value": fmt.Sprint(runtimePort)},
	}
	if spec.Name != "" {
		env = append(env, map[string]any{"name": "AGENT_NAME", "value": spec.Name})
	}
	if spec.Instructions != "" {
		env = append(env, map[string]any{"name": "AGENT_INSTRUCTIONS", "value": spec.Instructions})
	}
	podSpec := map[string]any{
		"automountServiceAccountToken":  false,
		"enableServiceLinks":            false,
		"restartPolicy":                 "Always",
		"terminationGracePeriodSeconds": 30,
		"securityContext": map[string]any{
			"runAsNonRoot": true, "runAsUser": 10001, "runAsGroup": 10001, "fsGroup": 10001,
			"seccompProfile": map[string]string{"type": "RuntimeDefault"},
		},
		"containers": []map[string]any{{
			"name": "runtime", "image": k.Image,
			"env":     env,
			"envFrom": []map[string]any{{"secretRef": map[string]string{"name": name}}},
			"ports":   []map[string]any{{"name": "http", "containerPort": runtimePort}},
			"readinessProbe": map[string]any{
				"httpGet": map[string]any{"path": "/healthz", "port": runtimePort}, "periodSeconds": 2, "failureThreshold": 3,
			},
			"resources": map[string]any{
				"requests": map[string]string{"cpu": "50m", "memory": "160Mi"},
				"limits":   map[string]string{"cpu": "1", "memory": "512Mi"},
			},
			"securityContext": map[string]any{
				"allowPrivilegeEscalation": false, "readOnlyRootFilesystem": true,
				"capabilities": map[string]any{"drop": []string{"ALL"}},
			},
			"volumeMounts": []map[string]any{{"name": "tmp", "mountPath": "/tmp"}},
		}},
		"volumes": []map[string]any{{"name": "tmp", "emptyDir": map[string]any{"sizeLimit": "64Mi"}}},
	}
	if k.RuntimeClassName != "" {
		podSpec["runtimeClassName"] = k.RuntimeClassName
	}
	if k.ImagePullSecret != "" {
		podSpec["imagePullSecrets"] = []map[string]string{{"name": k.ImagePullSecret}}
	}
	p := map[string]any{
		"apiVersion": "v1", "kind": "Pod",
		"metadata": map[string]any{"name": name, "labels": map[string]string{
			managedLabel: managedBy, agentLabel: spec.AgentID, "app.kubernetes.io/name": "pi-durable-agent",
		}},
		"spec": podSpec,
	}
	return k.do(ctx, http.MethodPost, k.path("pods", ""), p, nil)
}

type pod struct {
	Metadata struct {
		Labels            map[string]string `json:"labels"`
		DeletionTimestamp *string           `json:"deletionTimestamp"`
	} `json:"metadata"`
	Status struct {
		Phase      string `json:"phase"`
		PodIP      string `json:"podIP"`
		Conditions []struct {
			Type, Status string
		} `json:"conditions"`
		ContainerStatuses []struct {
			LastState struct {
				Terminated *struct {
					ExitCode int `json:"exitCode"`
				} `json:"terminated"`
			} `json:"lastState"`
		} `json:"containerStatuses"`
	} `json:"status"`
}

func (p *pod) deleting() bool { return p.Metadata.DeletionTimestamp != nil }

func (p *pod) ready() (string, bool) {
	if p.Status.Phase != "Running" || p.Status.PodIP == "" {
		return "", false
	}
	for _, c := range p.Status.Conditions {
		if c.Type == "Ready" && c.Status == "True" {
			return p.Status.PodIP, true
		}
	}
	return "", false
}

// ownedElsewhere: the runtime exits 3 when another process holds the agent.
func (p *pod) ownedElsewhere() bool {
	for _, c := range p.Status.ContainerStatuses {
		if t := c.LastState.Terminated; t != nil && t.ExitCode == 3 {
			return true
		}
	}
	return false
}

func (k *Kubernetes) getPod(ctx context.Context, name string) (*pod, error) {
	var p pod
	err := k.do(ctx, http.MethodGet, k.path("pods", name), nil, &p)
	if errors.Is(err, errNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

var errNotFound = errors.New("not found")

func (k *Kubernetes) path(resource, name string) string {
	p := fmt.Sprintf("/api/v1/namespaces/%s/%s", k.Namespace, resource)
	if name != "" {
		p += "/" + name
	}
	return p
}

func (k *Kubernetes) do(ctx context.Context, method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, k.api+path, rd)
	if err != nil {
		return err
	}
	token, err := os.ReadFile(k.tokenFile) // re-read: projected tokens rotate
	if err != nil {
		return fmt.Errorf("service account token: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := k.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return errNotFound
	}
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("kubernetes %s %s: %d %s", method, path, resp.StatusCode, strings.TrimSpace(string(b)))
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}
