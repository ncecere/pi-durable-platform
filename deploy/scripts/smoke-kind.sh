#!/usr/bin/env bash
# Smoke test: installs the chart into a kind cluster and checks that it works
# and that agents are isolated.
#
#   - a test PostgreSQL 17 (smoke/postgres.yaml), the chart with the kubernetes
#     launcher, development sign-in on a loopback APP_URL and the demo model;
#   - `doctor` passes; the RBAC allows what the launcher needs and no more;
#   - through the API (port-forward): a team, an agent and a message get a reply
#     from the demo model; hibernating and waking works;
#   - isolation (kind's CNI, kindnet, enforces NetworkPolicy): from an agent
#     pod only the database and the gateway are reachable (not the platform's
#     web port, the API server or the internet), and a pod outside the platform
#     cannot reach the agent.
#
# Needs kind, kubectl, helm, docker, curl and jq. Environment:
#   SMOKE_PORT      local port for the platform (default 8098; APP_URL follows)
#   SMOKE_CLUSTER   kind cluster name (default pdp-smoke)
#   KEEP_CLUSTER=1  leave the cluster in place afterwards
#   PLATFORM_TAG, RUNTIME_TAG  image tags to test (default: the chart's v<appVersion>)
#   SMOKE_HELM_ARGS  more `helm install` arguments (to try that a change is caught)
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
chart=$here/../helm/pi-durable-platform
port=${SMOKE_PORT:-8098}
cluster=${SMOKE_CLUSTER:-pdp-smoke}
ns=pi-durable-platform
agents_ns=pi-durable-agents
release=pi-durable-platform
base=http://localhost:$port
export KUBECONFIG=${SMOKE_KUBECONFIG:-$(mktemp -t pdp-smoke-kubeconfig.XXXXXX)}
jar=$(mktemp -t pdp-smoke-cookies.XXXXXX)
pf_pid=""

step() { printf '\n==> %s\n' "$*"; }
fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }

diagnostics() {
  echo "---- diagnostics" >&2
  kubectl get pods,svc,networkpolicy -A -o wide 2>&1 | grep -v -e kube-system -e local-path -e kindnet >&2 || true
  kubectl -n "$ns" logs deploy/$release --tail=80 >&2 2>&1 || true
  kubectl -n "$ns" describe pods -l app.kubernetes.io/name=pi-durable-platform 2>&1 | tail -40 >&2 || true
  kubectl -n "$agents_ns" describe pods 2>&1 | tail -40 >&2 || true
}

cleanup() {
  status=$?
  [ "$status" -eq 0 ] || diagnostics
  if [ -n "$pf_pid" ]; then kill "$pf_pid" 2>/dev/null || true; wait "$pf_pid" 2>/dev/null || true; fi
  if [ "${KEEP_CLUSTER:-}" != 1 ]; then kind delete cluster --name "$cluster" >/dev/null 2>&1 || true; fi
  rm -f "$jar"
  exit "$status"
}
trap cleanup EXIT

# expect <description> <command...>: the command must succeed.
expect() { local d=$1; shift; if "$@" >/dev/null 2>&1; then echo "ok:   $d"; else fail "$d"; fi; }
# refuse <description> <command...>: the command must fail.
refuse() { local d=$1; shift; if "$@" >/dev/null 2>&1; then fail "$d (it should be refused)"; else echo "ok:   $d"; fi; }

step "kind cluster $cluster"
kind delete cluster --name "$cluster" >/dev/null 2>&1 || true
kind create cluster --name "$cluster" --kubeconfig "$KUBECONFIG" --wait 120s

step "namespaces, Secrets, PostgreSQL"
kubectl create namespace $ns
kubectl label namespace $ns \
  pod-security.kubernetes.io/enforce=restricted pod-security.kubernetes.io/warn=restricted
dbpass=$(openssl rand -hex 16)
kubectl -n $ns create secret generic smoke-postgres --from-literal=password="$dbpass"
kubectl -n $ns create secret generic pi-durable-platform \
  --from-literal=DATABASE_URL="postgres://postgres:$dbpass@postgres.$ns.svc:5432/pdp?sslmode=disable" \
  --from-literal=AGENTS_DATABASE_URL="postgres://postgres:$dbpass@postgres.$ns.svc:5432/pdp_agents?sslmode=disable" \
  --from-literal=ENCRYPTION_KEY="$(openssl rand -base64 32)" \
  --from-literal=API_KEY_PEPPER="$(openssl rand -base64 32)"
kubectl apply -f "$here/smoke/postgres.yaml"
kubectl -n $ns rollout status deploy/postgres --timeout=180s

step "install the chart"
apiserver=$(kubectl get endpointslices -l kubernetes.io/service-name=kubernetes -o jsonpath='{.items[0].endpoints[0].addresses[0]}')
[ -n "$apiserver" ] || fail "no API server address"
tags=()
[ -z "${PLATFORM_TAG:-}" ] || tags+=(--set-string "images.platform.tag=$PLATFORM_TAG")
[ -z "${RUNTIME_TAG:-}" ] || tags+=(--set-string "images.runtime.tag=$RUNTIME_TAG")
helm install $release "$chart" -n $ns -f "$chart/ci/kind-smoke.yaml" \
  --set-string "appUrl=$base" \
  --set "networkPolicy.kubeApiServer.cidrs={$apiserver/32}" \
  ${tags[@]+"${tags[@]}"} ${SMOKE_HELM_ARGS:-} --wait --timeout 5m
kubectl -n $ns get pods -o wide

step "doctor"
kubectl -n $ns exec deploy/$release -- /pi-durable-platform doctor

step "RBAC: the launcher's rights in the agents namespace, nothing else"
sa=system:serviceaccount:$ns:$release
can() { kubectl auth can-i "$@" --as="$sa"; }
expect "may create pods in $agents_ns" can create pods -n $agents_ns
expect "may create secrets in $agents_ns" can create secrets -n $agents_ns
expect "may delete pods in $agents_ns" can delete pods -n $agents_ns
refuse "may not create deployments in $agents_ns" can create deployments -n $agents_ns
refuse "may not read secrets in $ns" can get secrets -n $ns
refuse "may not read secrets in kube-system" can get secrets -n kube-system
refuse "may not list nodes" can list nodes
refuse "may not create pods in $ns" can create pods -n $ns

step "reach the platform"
kubectl -n $ns port-forward svc/$release "$port":80 >/dev/null 2>&1 &
pf_pid=$!
for _ in $(seq 1 30); do curl -fsS "$base/readyz" >/dev/null 2>&1 && break; sleep 1; done
curl -fsS "$base/readyz" >/dev/null || fail "the platform is not ready on $base"

api() { # api <method> <path> [json body]
  local method=$1 path=$2 body=${3:-}
  local args=(-sS -X "$method" -b "$jar" -c "$jar" -H "X-CSRF-Token: ${csrf:-}" -w '\n%{http_code}')
  [ -z "$body" ] || args+=(-H 'Content-Type: application/json' -d "$body")
  local out code
  out=$(curl "${args[@]}" "$base$path")
  code=${out##*$'\n'}
  printf '%s' "${out%$'\n'*}"
  [[ "$code" =~ ^2 ]] || { echo "HTTP $code from $method $path: ${out%$'\n'*}" >&2; return 1; }
}

step "sign in as the development admin; create a team and an agent"
api POST /auth/dev '{"account":"admin"}' >/dev/null
csrf=$(api GET /v1/me | jq -r .data.csrfToken)
[ -n "$csrf" ] && [ "$csrf" != null ] || fail "no CSRF token"
api POST /v1/admin/teams '{"slug":"smoke","name":"Smoke","ownerEmail":"admin@localhost"}' >/dev/null
agent=$(api POST /v1/teams/smoke/agents '{"name":"smoke-agent"}' | jq -r .data.id)
[ -n "$agent" ] && [ "$agent" != null ] || fail "no agent id"
echo "agent $agent"

# send_and_wait <thread>: send a message (the first one wakes the agent: the
# runtime image may still be pulling) and wait for the demo model's reply.
send_and_wait() {
  local thread=$1 sent=0
  for i in $(seq 1 12); do
    if api POST "/v1/agents/$agent/threads/$thread/messages" "{\"text\":\"hello\",\"requestId\":\"$thread-$i\"}" >/dev/null 2>&1; then sent=1; break; fi
    sleep 10
  done
  [ "$sent" = 1 ] || fail "could not send a message (the agent did not start)"
  for _ in $(seq 1 60); do
    if api GET "/v1/agents/$agent/threads/$thread" 2>/dev/null | grep -q 'scripted demo model'; then return 0; fi
    sleep 2
  done
  fail "no reply from the demo model in thread $thread"
}

step "a message gets a reply from the demo model"
send_and_wait first
echo "ok:   the agent answered"
pod=$(kubectl -n $agents_ns get pods -l "pi-durable-platform/agent-id=$agent" -o jsonpath='{.items[0].metadata.name}')
[ -n "$pod" ] || fail "no agent pod"
kubectl -n $agents_ns wait --for=condition=Ready "pod/$pod" --timeout=60s
echo "agent pod $pod"
kubectl -n $agents_ns get pod "$pod" -o jsonpath='{.spec.automountServiceAccountToken}' | grep -qx false || fail "the agent pod mounts a service account token"

step "isolation: what an agent pod can reach"
# tcp <host> <port>: a TCP connection from the agent pod, 5 second limit.
tcp() {
  kubectl -n $agents_ns exec "$pod" -- node -e '
    const s = require("net").connect({ host: process.argv[1], port: +process.argv[2] });
    s.setTimeout(5000, () => process.exit(1));
    s.on("connect", () => process.exit(0));
    s.on("error", () => process.exit(1));' "$1" "$2"
}
expect "agent -> its database (postgres:5432)" tcp "postgres.$ns.svc" 5432
expect "agent -> the gateway ($release-gateway:8099)" tcp "$release-gateway.$ns.svc" 8099
refuse "agent -/-> the platform's web port ($release:80)" tcp "$release.$ns.svc" 80
refuse "agent -/-> the platform's web port on the pod (8098)" tcp "$(kubectl -n $ns get pod -l app.kubernetes.io/name=pi-durable-platform -o jsonpath='{.items[0].status.podIP}')" 8098
refuse "agent -/-> the Kubernetes API (kubernetes.default:443)" tcp kubernetes.default.svc 443
refuse "agent -/-> the Kubernetes API's address ($apiserver:6443)" tcp "$apiserver" 6443
refuse "agent -/-> the internet (1.1.1.1:443)" tcp 1.1.1.1 443
refuse "agent -/-> another namespace's service (kube-dns on 9153)" tcp kube-dns.kube-system.svc 9153

step "isolation: who can reach an agent pod"
agent_ip=$(kubectl -n $agents_ns get pod "$pod" -o jsonpath='{.status.podIP}')
kubectl create namespace smoke-intruder
kubectl -n smoke-intruder run intruder --image=registry.k8s.io/e2e-test-images/agnhost:2.53 -- pause
kubectl -n smoke-intruder wait --for=condition=Ready pod/intruder --timeout=180s
# Controls: a pod outside the platform's and the agents' policies reaches what
# the agent pod could not, so the refusals above and below are the policies'.
expect "control: a pod without policies reaches 1.1.1.1:443" kubectl -n smoke-intruder exec intruder -- /agnhost connect 1.1.1.1:443 --timeout=5s
expect "control: a pod without policies reaches the API server ($apiserver:6443)" kubectl -n smoke-intruder exec intruder -- /agnhost connect "$apiserver:6443" --timeout=5s
refuse "another namespace -/-> agent:4100" kubectl -n smoke-intruder exec intruder -- /agnhost connect "$agent_ip:4100" --timeout=5s
# A second agent pod is refused too: agents may not talk to each other.
runtime_image=$(kubectl -n $agents_ns get pod "$pod" -o jsonpath='{.spec.containers[0].image}')
kubectl apply -f - <<PEER
apiVersion: v1
kind: Pod
metadata:
  name: peer
  namespace: $agents_ns
  labels: { app.kubernetes.io/name: pi-durable-agent }
spec:
  automountServiceAccountToken: false
  securityContext: { runAsNonRoot: true, runAsUser: 10001, seccompProfile: { type: RuntimeDefault } }
  containers:
    - name: peer
      image: $runtime_image
      command: [node, -e, "setInterval(() => {}, 1000)"]
      securityContext: { allowPrivilegeEscalation: false, capabilities: { drop: [ALL] } }
PEER
kubectl -n $agents_ns wait --for=condition=Ready pod/peer --timeout=180s
refuse "agent -/-> another agent pod ($agent_ip:4100)" kubectl -n $agents_ns exec peer -- node -e '
  const s = require("net").connect({ host: process.argv[1], port: 4100 });
  s.setTimeout(5000, () => process.exit(1)); s.on("connect", () => process.exit(0)); s.on("error", () => process.exit(1));' "$agent_ip"
kubectl -n $agents_ns delete pod peer --now >/dev/null

step "hibernate and wake"
api POST "/v1/agents/$agent/hibernate" >/dev/null
for _ in $(seq 1 60); do
  kubectl -n $agents_ns get pod "$pod" >/dev/null 2>&1 || break
  sleep 2
done
refuse "the hibernated agent's pod is gone" kubectl -n $agents_ns get pod "$pod"
send_and_wait second
echo "ok:   the agent woke and answered"

step "smoke test passed"
