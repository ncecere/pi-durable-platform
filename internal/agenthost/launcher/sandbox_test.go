package launcher

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeAPI is just enough of the Kubernetes API for the Agent Sandbox launcher:
// secrets, sandboxes and sandbox claims in one namespace. Created sandboxes and
// claims become Ready at once with a loopback pod IP; suspending a sandbox
// clears its status.
type fakeAPI struct {
	t  *testing.T
	mu sync.Mutex
	// objects by "resource/name"
	objects map[string]map[string]any
	calls   []string
	patches []string // Content-Type of PATCH requests
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer test-sa-token" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var resource, name string
	for _, prefix := range []string{
		"/api/v1/namespaces/agents/",
		"/apis/agents.x-k8s.io/v1beta1/namespaces/agents/",
		"/apis/extensions.agents.x-k8s.io/v1beta1/namespaces/agents/",
	} {
		if rest, ok := strings.CutPrefix(r.URL.Path, prefix); ok {
			resource, name, _ = strings.Cut(rest, "/")
		}
	}
	if resource == "" {
		http.Error(w, "unexpected path "+r.URL.Path, http.StatusBadRequest)
		return
	}
	f.calls = append(f.calls, r.Method+" "+resource+"/"+name)
	key := resource + "/" + name
	var body map[string]any
	if r.Body != nil {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
	}
	switch {
	case r.Method == http.MethodGet && name == "":
		var items []map[string]any
		for k, o := range f.objects {
			if strings.HasPrefix(k, resource+"/") {
				items = append(items, o)
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"items": items})
	case r.Method == http.MethodGet:
		o, ok := f.objects[key]
		if !ok {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(o)
	case r.Method == http.MethodPost:
		name := body["metadata"].(map[string]any)["name"].(string)
		if _, ok := f.objects[resource+"/"+name]; ok {
			http.Error(w, "exists", http.StatusConflict)
			return
		}
		switch resource {
		case "sandboxes":
			body["status"] = readyStatus()
		case "sandboxclaims":
			body["status"] = map[string]any{
				"conditions": []map[string]string{{"type": "Ready", "status": "True"}},
				"sandbox":    map[string]any{"name": "pool-abc12", "podIPs": []string{"127.0.0.1"}},
			}
		}
		storeSecretData(resource, body)
		f.objects[resource+"/"+name] = body
		w.WriteHeader(http.StatusCreated)
	case r.Method == http.MethodPut:
		if _, ok := f.objects[key]; !ok {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		storeSecretData(resource, body)
		f.objects[key] = body
	case r.Method == http.MethodPatch:
		f.patches = append(f.patches, r.Header.Get("Content-Type"))
		o, ok := f.objects[key]
		if !ok {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		mode := body["spec"].(map[string]any)["operatingMode"].(string)
		o["spec"].(map[string]any)["operatingMode"] = mode
		o["status"] = map[string]any{}
		if mode == "Running" {
			o["status"] = readyStatus()
		}
	case r.Method == http.MethodDelete:
		if _, ok := f.objects[key]; !ok {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		delete(f.objects, key)
	}
}

// storeSecretData turns a Secret's stringData into data, as the API server does.
func storeSecretData(resource string, body map[string]any) {
	if resource != "secrets" || body["stringData"] == nil {
		return
	}
	data := map[string]any{}
	for k, v := range body["stringData"].(map[string]any) {
		data[k] = base64.StdEncoding.EncodeToString([]byte(v.(string)))
	}
	body["data"] = data
}

func readyStatus() map[string]any {
	return map[string]any{"conditions": []map[string]string{{"type": "Ready", "status": "True"}}, "podIPs": []string{"127.0.0.1"}}
}

func (f *fakeAPI) has(key string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.objects[key]
	return ok
}

func (f *fakeAPI) get(key string) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.objects[key]
}

// fakeRuntime is a standby runtime: it answers owned_elsewhere `busy` times,
// then accepts the activation.
type fakeRuntime struct {
	mu          sync.Mutex
	busy        int
	activations []map[string]any
	auth        []string
}

func (f *fakeRuntime) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.URL.Path != "/activate" {
		http.NotFound(w, r)
		return
	}
	f.auth = append(f.auth, r.Header.Get("Authorization"))
	if f.busy > 0 {
		f.busy--
		w.WriteHeader(http.StatusConflict)
		_, _ = io.WriteString(w, `{"error":{"code":"owned_elsewhere"}}`)
		return
	}
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	f.activations = append(f.activations, body)
	_, _ = io.WriteString(w, `{"data":{"activated":true}}`)
}

func newTestSandbox(t *testing.T, warmPool string, runtime http.Handler) (*AgentSandbox, *fakeAPI) {
	t.Helper()
	api := &fakeAPI{t: t, objects: map[string]map[string]any{}}
	srv := httptest.NewServer(api)
	t.Cleanup(srv.Close)
	port := 0
	if runtime != nil {
		rt := httptest.NewServer(runtime)
		t.Cleanup(rt.Close)
		_, p, _ := net.SplitHostPort(strings.TrimPrefix(rt.URL, "http://"))
		port, _ = strconv.Atoi(p)
	}
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("test-sa-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	k := &Kubernetes{
		Namespace: "agents", Image: "registry.example.com/runtime@sha256:abc", StartTimeout: 5 * time.Second,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		api: srv.URL, tokenFile: tokenFile, client: srv.Client(), port: port, locks: map[string]*sync.Mutex{},
	}
	return NewAgentSandbox(k, warmPool, "activation-token-0123456789-0123456789", ""), api
}

func testSpec() Spec {
	return Spec{
		AgentID: "A1b2", Name: "Helper", Instructions: "Be brief.", Schema: "agent_a1b2",
		DatabaseURL: "postgres://agent_a1b2:pw@db:5432/pdp_agents", Token: "runtime-token-0123456789",
		Env: map[string]string{"MODEL_PROVIDER": "demo"},
	}
}

func TestAgentSandboxWarmPool(t *testing.T) {
	rt := &fakeRuntime{busy: 2} // the previous runtime still holds the agent twice
	s, api := newTestSandbox(t, "pdp-runtimes", rt)
	ctx := context.Background()
	spec := testSpec()

	ep, err := s.Ensure(ctx, spec)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if ep.Token != spec.Token || !strings.HasPrefix(ep.BaseURL, "http://127.0.0.1:") {
		t.Fatalf("endpoint = %+v", ep)
	}
	claim := api.get("sandboxclaims/agent-a1b2")
	if claim == nil {
		t.Fatal("no claim created")
	}
	if ref := claim["spec"].(map[string]any)["warmPoolRef"].(map[string]any)["name"]; ref != "pdp-runtimes" {
		t.Fatalf("warmPoolRef = %v", ref)
	}
	labels := claim["metadata"].(map[string]any)["labels"].(map[string]any)
	if labels[agentLabel] != "A1b2" || labels[managedLabel] != managedBy {
		t.Fatalf("claim labels = %v", labels)
	}
	// Activated once (after two owned_elsewhere answers), with the pool token and the agent's configuration.
	if len(rt.activations) != 1 || len(rt.auth) != 3 {
		t.Fatalf("activations = %d, attempts = %d", len(rt.activations), len(rt.auth))
	}
	for _, a := range rt.auth {
		if a != "Bearer activation-token-0123456789-0123456789" {
			t.Fatalf("activation auth = %q", a)
		}
	}
	got := rt.activations[0]
	if got["agentId"] != "A1b2" || got["schema"] != "agent_a1b2" || got["databaseUrl"] != spec.DatabaseURL || got["token"] != spec.Token {
		t.Fatalf("activation body = %v", got)
	}
	// Nothing about the agent is stored in Kubernetes.
	if api.has("secrets/agent-a1b2") {
		t.Fatal("warm mode created a Secret")
	}

	if r := s.Running(); r["A1b2"].BaseURL != ep.BaseURL {
		t.Fatalf("Running = %v", r)
	}
	// Waking again re-activates idempotently (the runtime may have restarted).
	if _, err := s.Ensure(ctx, spec); err != nil {
		t.Fatalf("second Ensure: %v", err)
	}

	if err := s.Stop(ctx, "A1b2"); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if api.has("sandboxclaims/agent-a1b2") {
		t.Fatal("Stop did not release the claim")
	}
	if len(s.Running()) != 0 {
		t.Fatal("still running after Stop")
	}
	if err := s.Stop(ctx, "A1b2"); err != nil {
		t.Fatalf("Stop twice: %v", err)
	}
	if err := s.Remove(ctx, "A1b2"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
}

func TestAgentSandboxWarmPoolRefusesAnotherAgentsRuntime(t *testing.T) {
	rt := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = io.WriteString(w, `{"error":{"code":"already_activated"}}`)
	})
	s, _ := newTestSandbox(t, "pdp-runtimes", rt)
	if _, err := s.Ensure(context.Background(), testSpec()); err == nil || !strings.Contains(err.Error(), "another agent") {
		t.Fatalf("Ensure = %v, want a refusal", err)
	}
}

func TestAgentSandboxActivationSecret(t *testing.T) {
	rt := &fakeRuntime{}
	s, api := newTestSandbox(t, "pdp-runtimes", rt)
	s.ActivationToken, s.ActivationSecret = "", "pdp-runtime-activation"
	ctx := context.Background()

	if err := s.Prepare(ctx); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	secret := api.get("secrets/pdp-runtime-activation")
	if secret == nil {
		t.Fatal("Prepare did not create the activation Secret")
	}
	raw, _ := base64.StdEncoding.DecodeString(secret["data"].(map[string]any)[activationKey].(string))
	if len(raw) != 64 {
		t.Fatalf("generated token length = %d", len(raw))
	}
	if err := s.Prepare(ctx); err != nil { // idempotent: reads the existing Secret
		t.Fatalf("second Prepare: %v", err)
	}
	if got := api.get("secrets/pdp-runtime-activation")["data"].(map[string]any)[activationKey]; got != secret["data"].(map[string]any)[activationKey] {
		t.Fatal("Prepare replaced an existing token")
	}
	if _, err := s.Ensure(ctx, testSpec()); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if rt.auth[0] != "Bearer "+string(raw) {
		t.Fatal("activation did not use the Secret's token")
	}
}

func TestAgentSandboxPerAgent(t *testing.T) {
	s, api := newTestSandbox(t, "", nil)
	ctx := context.Background()
	spec := testSpec()

	ep, err := s.Ensure(ctx, spec)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if ep.BaseURL != "http://127.0.0.1:4100" || ep.Token != spec.Token {
		t.Fatalf("endpoint = %+v", ep)
	}
	secret := api.get("secrets/agent-a1b2")
	if secret == nil || secret["stringData"].(map[string]any)["AGENT_DATABASE_URL"] != spec.DatabaseURL {
		t.Fatalf("secret = %v", secret)
	}
	sb := api.get("sandboxes/agent-a1b2")
	sbSpec := sb["spec"].(map[string]any)
	if sbSpec["service"] != false || sbSpec["operatingMode"] != "Running" {
		t.Fatalf("sandbox spec = %v", sbSpec)
	}
	tpl := sbSpec["podTemplate"].(map[string]any)
	if tpl["metadata"].(map[string]any)["labels"].(map[string]any)["app.kubernetes.io/name"] != "pi-durable-agent" {
		t.Fatal("pod template lacks the agent label the network policies select")
	}
	podSpec := tpl["spec"].(map[string]any)
	if podSpec["automountServiceAccountToken"] != false {
		t.Fatal("pod template mounts a service-account token")
	}
	c := podSpec["containers"].([]any)[0].(map[string]any)
	if c["securityContext"].(map[string]any)["readOnlyRootFilesystem"] != true {
		t.Fatal("pod template is not hardened")
	}
	if strings.Contains(mustJSON(t, sb), spec.DatabaseURL) || strings.Contains(mustJSON(t, sb), spec.Token) {
		t.Fatal("secrets in the Sandbox spec")
	}

	if err := s.Stop(ctx, "A1b2"); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if mode := api.get("sandboxes/agent-a1b2")["spec"].(map[string]any)["operatingMode"]; mode != "Suspended" {
		t.Fatalf("operatingMode after Stop = %v", mode)
	}
	if len(s.Running()) != 0 {
		t.Fatal("suspended sandbox listed as running")
	}

	// Waking resumes the same Sandbox.
	if _, err := s.Ensure(ctx, spec); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if mode := api.get("sandboxes/agent-a1b2")["spec"].(map[string]any)["operatingMode"]; mode != "Running" {
		t.Fatalf("operatingMode after wake = %v", mode)
	}
	for _, ct := range api.patches {
		if ct != "application/merge-patch+json" {
			t.Fatalf("PATCH Content-Type = %q", ct)
		}
	}
	if _, ok := s.Running()["A1b2"]; !ok {
		t.Fatal("resumed sandbox not running")
	}

	if err := s.Remove(ctx, "A1b2"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if api.has("sandboxes/agent-a1b2") || api.has("secrets/agent-a1b2") {
		t.Fatal("Remove left the Sandbox or the Secret")
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
