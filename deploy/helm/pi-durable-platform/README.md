# pi-durable-platform (Helm chart)

Deploys [Pi Durable Platform](https://github.com/ncecere/pi-durable-platform): the platform Deployment and Services, the
agents namespace with restricted Pod Security, the launcher's RBAC and the NetworkPolicies that isolate agents. Read
[`deploy/README.md`](../../README.md) first for what is deployed and what is not (PostgreSQL, the ingress controller,
Agent Sandbox's controller).

Requires Kubernetes 1.27 or newer, Helm 3 or 4, and a CNI that enforces NetworkPolicy.

## Install

1. **PostgreSQL 17** with a platform database and an agents database (whose role has `CREATEROLE` and `CREATE` on it).
   [`deploy/examples/cloudnative-pg`](../../examples/cloudnative-pg) is a CloudNativePG `Cluster` named `pdp-db` and its
   policies; any PostgreSQL 17 works.

2. **The platform's namespace and Secret.** The chart creates the agents namespace, not this one:

   ```sh
   kubectl create namespace pi-durable-platform
   kubectl label namespace pi-durable-platform pod-security.kubernetes.io/enforce=restricted
   kubectl -n pi-durable-platform create secret generic pi-durable-platform \
     --from-literal=DATABASE_URL='postgres://pdp:...@pdp-db-rw.pi-durable-platform.svc:5432/pdp?sslmode=require' \
     --from-literal=AGENTS_DATABASE_URL='postgres://pdp_agents_admin:...@pdp-db-rw.pi-durable-platform.svc:5432/pdp_agents?sslmode=require' \
     --from-literal=ENCRYPTION_KEY="$(openssl rand -base64 32)" \
     --from-literal=API_KEY_PEPPER="$(openssl rand -base64 32)" \
     --from-literal=OIDC_CLIENT_SECRET='...'
   ```

   ([`deploy/secrets.example.yaml`](../../secrets.example.yaml) shows the same with fake values. Keep the two keys with
   your backups: losing `ENCRYPTION_KEY` loses every sealed provider key and header.) A setting can come from another
   Secret instead: `secrets.databaseUrl: {name: pdp-db-app, key: uri}`.

3. **Values.** The API server's address is what the standard NetworkPolicy needs for the platform to reach it
   (Cilium: `flavor: cilium` instead, no address):

   ```sh
   kubectl get endpointslices -l kubernetes.io/service-name=kubernetes \
     -o jsonpath='{.items[0].endpoints[*].addresses[*]}'
   ```

   ```yaml
   # values.yaml
   appUrl: https://agents.example.com
   oidc:
     issuer: https://idp.example.com/application/o/pi-durable-platform/
     clientId: pi-durable-platform
     allowedGroups: [pi-durable-users]          # optional: limit sign-in to IdP groups
     bootstrapAdminSubject: "your-oidc-subject" # becomes platform admin on first sign-in
   trustedProxies: [10.244.0.0/16]              # your ingress controller's pod CIDR
   ingress:
     enabled: true
     className: nginx
     tls:
       - secretName: pdp-tls
   networkPolicy:
     kubeApiServer:
       cidrs: [192.0.2.10/32]                   # the address(es) from above
     database:
       podLabels:
         cnpg.io/cluster: pdp-db                # your PostgreSQL pods (or cidrs: [...])
     ingress:
       from:                                    # who may reach the web port
         - namespaceSelector:
             matchLabels:
               kubernetes.io/metadata.name: ingress-nginx
   ```

4. **Install**, pinning the images by digest from the release notes (the default tag is `v<appVersion>`):

   ```sh
   helm install pi-durable-platform oci://ghcr.io/ncecere/charts/pi-durable-platform --version 0.2.0 \
     -n pi-durable-platform -f values.yaml \
     --set images.platform.digest=sha256:... --set images.runtime.digest=sha256:...
   ```

   From a checkout: `helm install pi-durable-platform deploy/helm/pi-durable-platform ...` (the images are the
   release in `Chart.yaml`'s `appVersion`). Verify a published chart with cosign:

   ```sh
   cosign verify ghcr.io/ncecere/charts/pi-durable-platform:0.2.0 \
     --certificate-identity https://github.com/ncecere/pi-durable-platform/.github/workflows/chart.yml@refs/tags/v0.2.0 \
     --certificate-oidc-issuer https://token.actions.githubusercontent.com
   ```

5. **Check.** `kubectl -n pi-durable-platform exec deploy/pi-durable-platform -- /pi-durable-platform doctor`, then sign
   in at `appUrl`. The first sign-in of `oidc.bootstrapAdminSubject` makes that person platform admin.

`helm uninstall` keeps the agents namespace (`agents.keepNamespace`): deleting it would delete every agent's Secret.
The database is yours.

## Launchers

- `agents.launcher: kubernetes` (default): one pod per agent.
- `agents.launcher: agent-sandbox`: install Agent Sandbox first. With the warm pool (`agentSandbox.warmPool.enabled`,
  `size`), the chart creates the SandboxTemplate and SandboxWarmPool and the platform creates the activation Secret
  (`agentSandbox.activationSecret`) itself. Without it, each agent is a Sandbox suspended when idle.

## Values

`values.yaml` documents every value, and `values.schema.json` rejects unknown keys and wrong types. The ones you set:

| Value | |
|---|---|
| `appUrl` | required: the public origin |
| `images.{platform,runtime}.{tag,digest}` | default tag `v<appVersion>`; pin by digest |
| `secrets.name` and per-setting `{name, key}` | where the database URLs, `ENCRYPTION_KEY`, `API_KEY_PEPPER` and the OIDC secret are read |
| `oidc.*` | issuer, client ID, claims, `allowedGroups`, `allowedEmailDomains`, `bootstrapAdminSubject` |
| `agents.*` | namespace, launcher, idle timeout, runtime class, pull Secret, default model, MCP hosts, quota |
| `agentSandbox.*` | warm pool size, names, runtime resources |
| `ingress.*` | class, host (default: `appUrl`'s), path, TLS, annotations |
| `networkPolicy.*` | flavor (`standard` or `cilium`), `kubeApiServer.cidrs`, `database` (pod labels or CIDRs), `ingress.from`, public HTTPS egress, extra egress rules for the platform and for agents |
| `metrics.*` | `enabled` (default on), `port` (9090), `serviceMonitor.*` (Prometheus Operator: namespace, labels, interval, relabelings), `dashboard.*` (the Grafana dashboard as a ConfigMap for the sidecar, off by default) |
| `tracing.*` | `otlpEndpoint` (an OTLP/HTTP collector's base URL; empty: tracing off), `serviceName`, `sampleRatio` |
| `networkPolicy.metrics.from` | who may reach the metrics port (peers as in NetworkPolicy `from`); nobody by default |
| `networkPolicy.egress.tracing` | the in-cluster collector (`to`, `ports`) |
| `resources`, `podSecurityContext`, `securityContext` | defaults are restricted: non-root, read-only root, no capabilities, seccomp |
| `replicaCount` | 1 by default; 2 is the supported setup for staying up through drains and rollouts: it rolls one pod at a time with none missing and spreads pods over nodes and zones ([Replicas](../../README.md#replicas), `ci/replicas.yaml`) |
| `podDisruptionBudget.*` | off by default (one replica: a budget of `minAvailable: 1` blocks node drains); turn it on with `replicaCount: 2`, `minAvailable: 1` |
| `affinity`, `topologySpreadConstraints` | when set, replace the spreading defaults of `replicaCount` > 1 |
| `config`, `extraEnv`, `extraEnvFrom`, `extraVolumes` | any other setting of the platform |

The ConfigMap holds every non-secret setting; the pod restarts when it changes (`rolloutOnConfigChange`).

## NetworkPolicies

- With `flavor: standard`, `networkPolicy.kubeApiServer.cidrs` is required: the platform starts agents through the API
  server, which a standard policy can only name by address. The chart fails to render without it rather than leave the
  launcher unable to start agents.
- With `flavor: cilium`, a `CiliumNetworkPolicy` allows the `kube-apiserver` entity; everything else stays standard.
- `networkPolicy.database` is required: pods with `podLabels` (in `namespace`, default the release's) and/or `cidrs`. The
  platform and agent pods may reach them on `port`. PostgreSQL's own pods need ingress from both: if your namespace
  denies by default, see [`deploy/examples/cloudnative-pg/network-policies.yaml`](../../examples/cloudnative-pg/network-policies.yaml).
- The chart's policies select the platform's own pods in its namespace, and every pod in the agents namespace.
- `networkPolicy.enabled: false` removes them all (agents are then not isolated).

## Metrics and traces

`metrics.enabled` (default) makes the platform serve `/metrics` on `metrics.port` and nothing else: a container port
named `metrics`, a Service `<release>-metrics` (not the web Service) and no Ingress. The port has no authentication, so
the chart creates no ingress rule for it until you name the scraper in `networkPolicy.metrics.from`:

```yaml
networkPolicy:
  metrics:
    from:
      - namespaceSelector: { matchLabels: { kubernetes.io/metadata.name: monitoring } }
        podSelector: { matchLabels: { app.kubernetes.io/name: prometheus } }
  egress:
    tracing:   # lets the platform pods reach the collector
      to:
        - namespaceSelector: { matchLabels: { kubernetes.io/metadata.name: observability } }
          podSelector: { matchLabels: { app.kubernetes.io/name: tempo } }
metrics:
  serviceMonitor: { enabled: true, labels: { release: kube-prometheus-stack } }   # Prometheus Operator only
  dashboard: { enabled: true }                                                      # ConfigMap grafana_dashboard: "1"
tracing:
  otlpEndpoint: http://tempo.observability.svc:4318
```

`files/pi-durable-platform-dashboard.json` is a link to `deploy/grafana/pi-durable-platform.json` (Helm includes the
file it points to when it lints, templates and packages). See [deploy/README.md](../../README.md#monitoring) and
DESIGN.md, Observability.

## Development

```sh
helm lint . -f ci/default.yaml
deploy/scripts/validate.sh        # every ci/*.yaml, kubeconform, Kustomize parity
deploy/scripts/smoke-kind.sh      # install into kind and check isolation
```

After changing a template, run `deploy/scripts/generate-kustomize.sh` and commit the regenerated Kustomize files.
`ci/*.yaml` are the values examples CI renders.
