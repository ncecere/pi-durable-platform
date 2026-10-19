# Kustomize

The same objects as the [Helm chart](../helm/pi-durable-platform/README.md), for clusters managed with Kustomize. Read
[`deploy/README.md`](../README.md) first (what is deployed, the Secrets, what is not included).

| Directory | |
|---|---|
| `base/` | the platform for the `kubernetes` launcher: both namespaces (restricted Pod Security), ServiceAccount, Role and RoleBinding, ConfigMap, Deployment, the two Services and the NetworkPolicies. **Generated from the chart**, except `namespace-platform.yaml` |
| `overlays/kubernetes/` | one pod per agent: the base as it is |
| `overlays/agent-sandbox/` | Agent Sandbox with a warm pool: SandboxTemplate and SandboxWarmPool (generated), launcher settings, and the Role for sandbox claims |
| `overlays/agent-sandbox-cold/` | Agent Sandbox without a warm pool: a Sandbox per agent |
| `components/cilium/` | the Kubernetes API egress as a `CiliumNetworkPolicy` (the `kube-apiserver` entity) instead of the base's NetworkPolicy |
| `components/ingress/` | an Ingress (patch the class and host) |
| `components/pod-disruption-budget/`, `components/resourcequota/` | a PodDisruptionBudget; a quota for the agents namespace |
| `examples/production/` | Agent Sandbox + ingress with TLS + PodDisruptionBudget + quota, images by digest, single sign-on settings |
| `examples/cilium/` | the `kubernetes` launcher on Cilium |
| `generate/` | the Helm values the generated files come from (do not edit the generated files: edit the chart and run `deploy/scripts/generate-kustomize.sh`) |

## Use

1. Create the Secrets in `pi-durable-platform` ([`../secrets.example.yaml`](../secrets.example.yaml)), and the
   namespace first if you create them before applying: `kubectl apply -f base/namespace-platform.yaml`.
2. Copy [`examples/production`](examples/production) (or `examples/cilium`) and replace the example values:
   - the settings in the ConfigMap patch (`APP_URL`, `OIDC_*`, `BOOTSTRAP_ADMIN_SUBJECT`, `TRUSTED_PROXIES`, ...). The
     ConfigMap holds every non-secret setting: after changing it, `kubectl -n pi-durable-platform rollout restart
     deploy/pi-durable-platform`;
   - **the API server's address** in the `pi-durable-platform-egress-kube-api` NetworkPolicy: the base ships the
     placeholder `192.0.2.10/32` (`kubectl get endpointslices -l kubernetes.io/service-name=kubernetes`), or use
     `components/cilium`;
   - the images by digest (`images:`, with `newTag: v0.1.2@sha256:...`; `images-config.yaml` makes the transformer
     reach the SandboxTemplate; with the `kubernetes` launcher the runtime image is `AGENTS_IMAGE` in the ConfigMap);
   - PostgreSQL's pod labels in `pi-durable-platform-egress` / `-agents-egress-database` (the base assumes the
     CloudNativePG cluster `pdp-db` in the platform namespace, as in
     [`../examples/cloudnative-pg`](../examples/cloudnative-pg)): patch the `to` peers for another location;
   - who may reach the web port (`pi-durable-platform-ingress-web`; the base allows every namespace).
3. `kubectl kustomize <your directory>`, then `kubectl apply -k <your directory>`.

The namespace names (`pi-durable-platform`, `pi-durable-agents`) are fixed in the base: they appear in the NetworkPolicy
selectors, so a `namespace:` transformer is not enough. Use the Helm chart (`agents.namespace`, `-n`) for other names.

## Keeping in step with the chart

`deploy/scripts/generate-kustomize.sh --check` fails when a generated file differs from `helm template`, and
`deploy/scripts/check-parity.sh` builds each overlay and example and compares every object with the chart rendered with
the matching values (`generate/*.values.yaml`, `examples/*/parity.values.yaml`). A change to an overlay's patch (the
Role's rules, the launcher settings) that the chart does not share fails there.
