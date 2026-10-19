#!/usr/bin/env bash
# Static checks for deploy/: the same ones deploy.yml runs.
#
#   - helm lint and helm template of every chart values example (ci/*.yaml),
#     and the cases the chart must refuse;
#   - kustomize build of every overlay and example;
#   - kubeconform (strict) on all rendered objects: Kubernetes schemas for the
#     built-in kinds, the datree CRD catalog for Cilium and CloudNativePG, and
#     Agent Sandbox's kinds skipped (the catalog has no schema for them);
#   - the Kustomize files being what the chart generates (generate-kustomize.sh
#     --check) and the overlays rendering the same objects as the chart
#     (check-parity.sh);
#   - the chart's appVersion being the release in internal/buildinfo.
#
# Needs helm, kubectl (kustomize), kubeconform, yq and jq. HELM=helm3 checks the
# chart with another Helm.
set -euo pipefail

root=$(cd "$(dirname "$0")/../.." && pwd)
chart=$root/deploy/helm/pi-durable-platform
kz=$root/deploy/kustomize
export HELM=${HELM:-helm}
KUBERNETES_VERSION=${KUBERNETES_VERSION:-1.33.0}
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

step() { printf '\n==> %s\n' "$*"; }
fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }

kubeconform_check() {
  kubeconform -strict -summary -kubernetes-version "$KUBERNETES_VERSION" \
    -schema-location default \
    -schema-location 'https://raw.githubusercontent.com/datreeio/CRDs-catalog/main/{{.Group}}/{{.ResourceKind}}_{{.ResourceAPIVersion}}.json' \
    -skip SandboxTemplate,SandboxWarmPool "$@"
}

# The values the smoke test adds on the command line.
extra_for() {
  case $1 in
    kind-smoke.yaml) echo "--set-string appUrl=http://localhost:8098 --set networkPolicy.kubeApiServer.cidrs={192.0.2.10/32}" ;;
  esac
}

step "chart: $("$HELM" version --short)"
for f in "$chart"/ci/*.yaml; do
  name=$(basename "$f")
  # shellcheck disable=SC2046
  "$HELM" lint "$chart" -f "$f" $(extra_for "$name") >"$tmp/lint.txt" 2>&1 || { cat "$tmp/lint.txt"; fail "helm lint $name"; }
  # shellcheck disable=SC2046
  "$HELM" template pi-durable-platform "$chart" -n pi-durable-platform -f "$f" $(extra_for "$name") >"$tmp/helm-$name" || fail "helm template $name"
  echo "ok: $name"
done

step "chart: what it must refuse"
refuses() { # refuses <message fragment> <helm args...>
  local want=$1
  shift
  if "$HELM" template x "$chart" -n n "$@" >"$tmp/refuse.txt" 2>&1; then fail "the chart accepted: $*"; fi
  grep -q -- "$want" "$tmp/refuse.txt" || { cat "$tmp/refuse.txt"; fail "no '$want' for: $*"; }
  echo "ok: refuses $*"
}
ok_values=(-f "$chart/ci/default.yaml")
refuses "appUrl is required" --set networkPolicy.kubeApiServer.cidrs='{192.0.2.1/32}' --set networkPolicy.database.cidrs='{192.0.2.2/32}'
refuses "networkPolicy.kubeApiServer.cidrs is required" --set appUrl=https://a.example.com --set networkPolicy.database.cidrs='{192.0.2.2/32}'
refuses "networkPolicy.database" --set appUrl=https://a.example.com --set networkPolicy.kubeApiServer.cidrs='{192.0.2.1/32}'
refuses "replicaCount" "${ok_values[@]}" --set replicaCount=0
refuses "launcher" "${ok_values[@]}" --set agents.launcher=local
refuses "flavor" "${ok_values[@]}" --set networkPolicy.flavor=calico
refuses "digest" "${ok_values[@]}" --set images.platform.digest=latest

step "chart: replicas"
# One replica is replaced (Recreate) with nothing added; several roll one pod at a
# time with none missing, spread over nodes and zones unless the values say otherwise.
render_deploy() { "$HELM" template x "$chart" -n n "${ok_values[@]}" -s templates/deployment.yaml "$@"; }
one=$(render_deploy | yq '.spec.strategy.type + " " + (.spec.template.spec | has("affinity") | tostring) + " " + (.spec.template.spec | has("topologySpreadConstraints") | tostring)')
[ "$one" = "Recreate false false" ] || fail "one replica renders: $one"
render_deploy --set replicaCount=2 | yq -e '.spec.replicas == 2 and .spec.strategy.type == "RollingUpdate" and .spec.strategy.rollingUpdate.maxUnavailable == 0' >/dev/null ||
  fail "two replicas do not roll one at a time with none missing"
render_deploy --set replicaCount=2 | yq -e '.spec.template.spec.affinity.podAntiAffinity.preferredDuringSchedulingIgnoredDuringExecution[0].podAffinityTerm.topologyKey == "kubernetes.io/hostname"
  and .spec.template.spec.topologySpreadConstraints[0].whenUnsatisfiable == "ScheduleAnyway"' >/dev/null || fail "two replicas are not spread by default"
# What the values say wins over the defaults.
render_deploy --set replicaCount=3 --set 'topologySpreadConstraints[0].maxSkew=2' --set 'topologySpreadConstraints[0].topologyKey=x' --set 'topologySpreadConstraints[0].whenUnsatisfiable=DoNotSchedule' |
  yq -e '(.spec.template.spec.topologySpreadConstraints | length) == 1 and .spec.template.spec.topologySpreadConstraints[0].topologyKey == "x"' >/dev/null || fail "topologySpreadConstraints are not taken from the values"
"$HELM" template x "$chart" -n n -f "$chart/ci/replicas.yaml" -s templates/pdb.yaml | yq -e '.spec.minAvailable == 1' >/dev/null || fail "the replicas example has no PodDisruptionBudget"
echo "ok: replicas"

step "kustomize"
for d in overlays/kubernetes overlays/agent-sandbox overlays/agent-sandbox-cold examples/production examples/cilium; do
  kubectl kustomize "$kz/$d" >"$tmp/kustomize-$(echo "$d" | tr / -).yaml" || fail "kustomize build $d"
  echo "ok: $d"
done

step "kubeconform (Kubernetes $KUBERNETES_VERSION)"
kubeconform_check "$tmp"/helm-*.yaml "$tmp"/kustomize-*.yaml \
  "$root"/deploy/examples/cloudnative-pg/*.yaml "$root"/deploy/secrets.example.yaml

step "Kustomize files are generated from the chart"
"$root/deploy/scripts/generate-kustomize.sh" --check

step "Kustomize and Helm render the same objects"
"$root/deploy/scripts/check-parity.sh"

step "chart version"
app=$(sed -n 's/^appVersion: "\(.*\)"$/\1/p' "$chart/Chart.yaml")
rel=$(sed -n 's/^[[:space:]]*Version = "\([^"]*\)"$/\1/p' "$root/internal/buildinfo/buildinfo.go" | head -n1)
[ "$app" = "$rel" ] || fail "Chart.yaml appVersion is $app but internal/buildinfo's version is $rel: update deploy/helm/pi-durable-platform/Chart.yaml (version and appVersion) and deploy/kustomize (the generated files carry the version label)"
echo "ok: appVersion $app"

step "deploy checks passed"
