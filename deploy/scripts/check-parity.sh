#!/usr/bin/env bash
# Renders the Kustomize overlays and the Helm chart with the matching values
# and fails if their objects differ: same RBAC rules, same NetworkPolicies,
# same Deployment, and so on. A hand-written overlay patch that drifts from
# the chart shows up here; generate-kustomize.sh --check covers the generated
# files.
#
# Needs helm, kubectl (kustomize), yq (mikefarah, v4) and jq.
set -euo pipefail

root=$(cd "$(dirname "$0")/../.." && pwd)
chart=$root/deploy/helm/pi-durable-platform
kz=$root/deploy/kustomize
gen=$kz/generate
HELM=${HELM:-helm}
KUSTOMIZE=${KUSTOMIZE:-kubectl kustomize}
YQ=${YQ:-yq}

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

# normalise: a multi-document YAML stream to one sorted JSON array. What
# legitimately differs is removed: Helm's own labels, the config checksum, and
# the platform namespace (the chart does not create the release namespace).
normalise() {
  "$YQ" eval-all -o=json '[.] | .[] | select(. != null)' - | jq -s -S '
    map(del(.metadata.labels["helm.sh/chart"], .metadata.labels["app.kubernetes.io/managed-by"]))
    | map(del(.spec.template.metadata.annotations["checksum/config"]))
    | map(select((.kind == "Namespace" and .metadata.name == "pi-durable-platform") | not))
    | sort_by(.kind, (.metadata.namespace // ""), .metadata.name)'
}

status=0
# compare <overlay directory> <values file>...
compare() {
  local dir=$1
  shift
  local args=()
  for f in "$@"; do args+=(-f "$f"); done
  # shellcheck disable=SC2086
  $KUSTOMIZE "$kz/$dir" | normalise >"$tmp/kustomize.json"
  "$HELM" template pi-durable-platform "$chart" -n pi-durable-platform "${args[@]}" | normalise >"$tmp/helm.json"
  if diff -u --label "kustomize: $dir" --label "helm: ${*}" "$tmp/kustomize.json" "$tmp/helm.json" >"$tmp/diff.txt"; then
    echo "ok: $dir ($(jq length "$tmp/helm.json") objects)"
  else
    echo "DIFFERENT: $dir" >&2
    cat "$tmp/diff.txt" >&2
    status=1
  fi
}

compare overlays/kubernetes "$gen/base.values.yaml"
compare overlays/agent-sandbox "$gen/base.values.yaml" "$gen/agent-sandbox.values.yaml"
compare overlays/agent-sandbox-cold "$gen/base.values.yaml" "$gen/agent-sandbox-cold.values.yaml"
compare examples/production "$kz/examples/production/parity.values.yaml"
compare examples/cilium "$kz/examples/cilium/parity.values.yaml"
compare examples/monitoring "$kz/examples/monitoring/parity.values.yaml"

exit "$status"
