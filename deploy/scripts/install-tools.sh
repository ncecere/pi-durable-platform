#!/usr/bin/env bash
# Installs pinned, checksum-verified tools (linux/amd64) for the deploy
# workflows into ${BIN_DIR:-$HOME/.local/bin}:
#
#   install-tools.sh helm helm3 kubeconform yq kind
#
# helm is Helm 4 (the chart is also checked with Helm 3, installed as helm3).
# Helm and kubeconform are verified against the checksums their projects
# publish; yq and kind against the digests recorded here when they were pinned.
# To upgrade a tool, change its version and digest together.
set -euo pipefail

bin=${BIN_DIR:-$HOME/.local/bin}
mkdir -p "$bin"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

HELM_VERSION=v4.3.0
HELM_SHA256=86584a54def73570558f66f5111cc53dfed56689637ae32c1201205d494f54fb
HELM3_VERSION=v3.19.0
HELM3_SHA256=a7f81ce08007091b86d8bd696eb4d86b8d0f2e1b9f6c714be62f82f96a594496
KUBECONFORM_VERSION=v0.7.0
KUBECONFORM_SHA256=c31518ddd122663b3f3aa874cfe8178cb0988de944f29c74a0b9260920d115d3
YQ_VERSION=v4.54.1
YQ_SHA256=8e34fc298390875de416e6a4afcb8cabeceb25d9aa8506c1a2f9353cf702ea5f
KIND_VERSION=v0.33.0
KIND_SHA256=aee6151561422756b764a4ae28e7f44cda5af5a9eead3cc9985112b1de8d8e0d

fetch() { # fetch <url> <sha256> <file>
  curl -fsSL --retry 3 -o "$tmp/$3" "$1"
  echo "$2  $tmp/$3" | sha256sum -c - >/dev/null || { echo "checksum mismatch for $1" >&2; exit 1; }
}

for tool in "$@"; do
  case "$tool" in
    helm)
      fetch "https://get.helm.sh/helm-$HELM_VERSION-linux-amd64.tar.gz" "$HELM_SHA256" helm.tgz
      tar -xzf "$tmp/helm.tgz" -C "$tmp" linux-amd64/helm
      install -m 0755 "$tmp/linux-amd64/helm" "$bin/helm"
      ;;
    helm3)
      fetch "https://get.helm.sh/helm-$HELM3_VERSION-linux-amd64.tar.gz" "$HELM3_SHA256" helm3.tgz
      mkdir -p "$tmp/h3"
      tar -xzf "$tmp/helm3.tgz" -C "$tmp/h3" linux-amd64/helm
      install -m 0755 "$tmp/h3/linux-amd64/helm" "$bin/helm3"
      ;;
    kubeconform)
      fetch "https://github.com/yannh/kubeconform/releases/download/$KUBECONFORM_VERSION/kubeconform-linux-amd64.tar.gz" "$KUBECONFORM_SHA256" kc.tgz
      tar -xzf "$tmp/kc.tgz" -C "$tmp" kubeconform
      install -m 0755 "$tmp/kubeconform" "$bin/kubeconform"
      ;;
    yq)
      fetch "https://github.com/mikefarah/yq/releases/download/$YQ_VERSION/yq_linux_amd64" "$YQ_SHA256" yq
      install -m 0755 "$tmp/yq" "$bin/yq"
      ;;
    kind)
      fetch "https://kind.sigs.k8s.io/dl/$KIND_VERSION/kind-linux-amd64" "$KIND_SHA256" kind
      install -m 0755 "$tmp/kind" "$bin/kind"
      ;;
    *)
      echo "unknown tool: $tool" >&2
      exit 2
      ;;
  esac
  echo "installed $tool"
done
