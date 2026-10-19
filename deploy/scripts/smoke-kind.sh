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
#   - two platform replicas (ci/kind-smoke.yaml: replicaCount 2 and a
#     PodDisruptionBudget): both become ready, and through each one's own
#     port-forward the session, the agent and its conversation are the same
#     (one replica hibernates what the other started, and either wakes it).
#
# Needs kind, kubectl, helm, docker, curl and jq. Environment:
#   SMOKE_PORT      local port for the platform (default 8098; APP_URL follows);
#                   the replicas' own forwards use the next two ports
#   SMOKE_CLUSTER   kind cluster name (default pdp-smoke)
#   KEEP_CLUSTER=1  leave the cluster in place afterwards
#   PLATFORM_IMAGE, RUNTIME_IMAGE  the images to test, as repository[:tag][@sha256:digest]
#                   (for example ghcr.io/ncecere/pi-durable-platform:sha-0123456789ab@sha256:...);
#                   they set the chart's images.<platform|runtime>.repository, tag and digest.
#                   Default: the chart's own, ghcr.io/ncecere/...:v<appVersion>, the last release.
#                   deploy.yml sets them to the images built for the commit under test.
#   PLATFORM_TAG, RUNTIME_TAG  only a tag, on the chart's repository (not with the image variable
#                   of the same image)
#   SMOKE_HELM_ARGS  more `helm install` arguments (to try that a change is caught)
#
# The script prints the images it installed. Nothing it checks depends on the
# images being released ones: it does not look at the version the platform
# reports (an image built for a commit says sha-<12>, not the chart's appVersion).
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
replica_pids=()

step() { printf '\n==> %s\n' "$*"; }
fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }

# image_args <platform|runtime> <reference> <tag>: the helm arguments for one image,
# from a reference (repository[:tag][@digest]) or else a bare tag.
image_args() {
  local key=images.$1 ref=$2 tag=$3 digest="" repo up
  up=$(printf '%s' "$1" | tr '[:lower:]' '[:upper:]')
  if [ -n "$ref" ]; then
    [ -z "$tag" ] || fail "set either ${up}_IMAGE or ${up}_TAG, not both"
    repo=$ref
    case $repo in *@*) digest=${repo#*@}; repo=${repo%%@*} ;; esac
    case ${repo##*/} in *:*) tag=${repo##*:}; repo=${repo%:*} ;; esac
    [ -n "$repo" ] || fail "${up}_IMAGE '$ref' has no repository"
    printf '%s\n' --set-string "$key.repository=$repo"
    [ -z "$digest" ] || printf '%s\n' --set-string "$key.digest=$digest"
  fi
  [ -z "$tag" ] || printf '%s\n' --set-string "$key.tag=$tag"
}

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
  for pid in ${pf_pid:+"$pf_pid"} ${replica_pids[@]+"${replica_pids[@]}"}; do kill "$pid" 2>/dev/null || true; wait "$pid" 2>/dev/null || true; done
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
# (not in a process substitution: a failure there would not stop the script)
platform_args=$(image_args platform "${PLATFORM_IMAGE:-}" "${PLATFORM_TAG:-}") || exit 1
runtime_args=$(image_args runtime "${RUNTIME_IMAGE:-}" "${RUNTIME_TAG:-}") || exit 1
images=()
while IFS= read -r line; do [ -z "$line" ] || images+=("$line"); done <<ARGS
$platform_args
$runtime_args
ARGS
helm install $release "$chart" -n $ns -f "$chart/ci/kind-smoke.yaml" \
  --set-string "appUrl=$base" \
  --set "networkPolicy.kubeApiServer.cidrs={$apiserver/32}" \
  ${images[@]+"${images[@]}"} ${SMOKE_HELM_ARGS:-} --wait --timeout 5m
kubectl -n $ns get pods -o wide
# What was installed (the chart's v<appVersion> when nothing was set above).
tested_platform=$(kubectl -n $ns get deploy/$release -o jsonpath='{.spec.template.spec.containers[0].image}')
tested_runtime=$(kubectl -n $ns get configmap/$release -o jsonpath='{.data.AGENTS_IMAGE}')
echo "images under test:"
echo "  platform: $tested_platform"
echo "  runtime:  $tested_runtime"

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
echo "agent pod $pod runs $(kubectl -n $agents_ns get pod "$pod" -o jsonpath='{.status.containerStatuses[0].imageID}')"
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

step "two platform replicas"
# Each replica through its own port-forward (the Service's one goes to a single
# pod). The session cookie is the same: a session made on one is valid on both.
read -ra platform_pods <<<"$(kubectl -n $ns get pods -l app.kubernetes.io/name=pi-durable-platform -o jsonpath='{.items[*].metadata.name}')"
[ "${#platform_pods[@]}" -eq 2 ] || fail "expected 2 platform pods, got: ${platform_pods[*]}"
kubectl -n $ns wait --for=condition=Ready "pod/${platform_pods[0]}" "pod/${platform_pods[1]}" --timeout=120s
bases=()
for i in 0 1; do
  rport=$((port + 1 + i))
  kubectl -n $ns port-forward "pod/${platform_pods[$i]}" "$rport":8098 >/dev/null 2>&1 &
  replica_pids+=("$!")
  bases+=("http://localhost:$rport")
done
for b in "${bases[@]}"; do
  for _ in $(seq 1 30); do curl -fsS "$b/readyz" >/dev/null 2>&1 && break; sleep 1; done
  curl -fsS "$b/readyz" >/dev/null || fail "a platform pod is not ready on $b"
done
on() { local base=$1; shift; api "$@"; } # on <base> <api args>: through one replica
for b in "${bases[@]}"; do
  expect "the session is valid on $b" on "$b" GET /v1/me
  expect "the agent's conversation reads on $b" on "$b" GET "/v1/agents/$agent/threads/first"
done
# A message through each replica, the conversation read through the other.
on "${bases[0]}" POST "/v1/agents/$agent/threads/shared/messages" '{"text":"from the first","requestId":"shared-1"}' >/dev/null || fail "send through the first replica"
for _ in $(seq 1 60); do
  on "${bases[1]}" GET "/v1/agents/$agent/threads/shared" 2>/dev/null | grep -q 'scripted demo model' && break
  sleep 2
done
on "${bases[1]}" GET "/v1/agents/$agent/threads/shared" | grep -q 'scripted demo model' || fail "a reply to a message sent through one replica is not visible on the other"
echo "ok:   a conversation started on one replica is read on the other"
expect "the PodDisruptionBudget allows one disruption" \
  test "$(kubectl -n $ns get pdb $release -o jsonpath='{.status.disruptionsAllowed}')" = 1

step "hibernate and wake"
# One replica hibernates the agent, the other wakes it.
on "${bases[1]}" POST "/v1/agents/$agent/hibernate" >/dev/null
for _ in $(seq 1 60); do
  kubectl -n $agents_ns get pod "$pod" >/dev/null 2>&1 || break
  sleep 2
done
refuse "the hibernated agent's pod is gone" kubectl -n $agents_ns get pod "$pod"
send_and_wait second
echo "ok:   the agent woke and answered"

step "smoke test passed"
