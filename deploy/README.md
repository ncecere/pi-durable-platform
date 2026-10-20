# Deploying Pi Durable Platform on Kubernetes

Two ways to the same objects, kept in step by CI:

| | Where | Use it when |
|---|---|---|
| **Helm chart** | [`helm/pi-durable-platform`](helm/pi-durable-platform/README.md), published as `oci://ghcr.io/ncecere/charts/pi-durable-platform` | you want values instead of patches (the source of the other) |
| **Kustomize** | [`kustomize`](kustomize/README.md): a base, overlays for each launcher, components, an example | your cluster is managed with Kustomize or Flux/Argo CD on plain manifests |

Also here: [`secrets.example.yaml`](secrets.example.yaml) (the Secrets the platform reads, with fake values) and
[`examples/cloudnative-pg`](examples/cloudnative-pg) (PostgreSQL 17 with CloudNativePG, and its NetworkPolicies).

## What gets deployed

- **The platform**: one Deployment (one replica by default, two recommended, see [Replicas](#replicas)) with two Services: the web/API port (8098, Service port 80)
  and, as a Service of its own, the gateway agents call for models (8099), so agents get a name for that port only.
  It runs non-root with a read-only root filesystem, no capabilities and the runtime's default seccomp profile.
- **The agents namespace** (`pi-durable-agents` by default), with the **restricted** Pod Security level. Agent pods
  (or Agent Sandbox runtimes) run here, and nothing else does.
- **RBAC**: a ServiceAccount for the platform and a Role and RoleBinding **in the agents namespace only**, with exactly
  the verbs the launcher uses (below). Nothing is cluster-wide.
- **NetworkPolicies**, which are what isolates agents from each other and from the cluster:
  - agents namespace: default deny; an agent pod takes traffic only from the platform (port 4100) and reaches only its
    PostgreSQL (5432), the platform's gateway (8099) and cluster DNS. Not other agents, the platform's web port, the
    Kubernetes API or the internet;
  - the platform pods: web traffic (8098) from the peers you list (your ingress controller), the gateway port from
    agent pods only; egress to DNS, PostgreSQL, agent pods (4100), the Kubernetes API and public HTTPS (the identity
    provider, model providers, MCP servers by their public names; private ranges are excluded).
- Optionally an Ingress, a PodDisruptionBudget and a ResourceQuota for the agents namespace.
- With `agent-sandbox` and a warm pool: a SandboxTemplate and a SandboxWarmPool (the template turns off the
  controller's own NetworkPolicy so ours are the only ones).

### The launcher's RBAC

| Launcher | Rules in the agents namespace |
|---|---|
| `kubernetes` | pods: get, list, watch, create, delete; secrets: get, create, update, delete |
| `agent-sandbox`, warm pool | sandboxclaims: get, list, watch, create, delete; secrets: get, create, delete (the pool's activation Secret, which the platform creates; cleanup of a Secret another launcher left) |
| `agent-sandbox`, no warm pool | sandboxes: get, list, watch, create, patch, delete; pods: get; secrets: get, create, update, delete |

Derived from `internal/agenthost/launcher`; the [v0.1.0 notes](../docs/releases/v0.1.0.md) list the union.

## Before you install

1. **A cluster whose CNI enforces NetworkPolicy** (Cilium, Calico, kindnet in recent kind, ...). A CNI that ignores
   them (flannel on its own) leaves agents un-isolated and says nothing. The smoke test
   ([`scripts/smoke-kind.sh`](scripts/smoke-kind.sh)) shows how to check: a pod that should be refused must be.
2. **PostgreSQL 17** with two databases: the platform's, and the agents' with a role that has `CREATEROLE` and
   `CREATE` on it (each agent gets its own schema and role there; agents never get this role). The chart does not
   install PostgreSQL. [`examples/cloudnative-pg`](examples/cloudnative-pg) is a CloudNativePG cluster and the
   policies for its pods; any PostgreSQL 17 reachable from the cluster works (managed services included: give the
   chart its address in `networkPolicy.database.cidrs`).

   **Keep agents' roles out of the platform database.** Agent roles are login roles of the same PostgreSQL server, and
   PostgreSQL lets PUBLIC connect to every database it creates. The platform's advisory locks (waking an agent, the
   reaper, migrations) live in the platform database: a role that can connect there could hold them and stop agents
   from waking or the platform from starting. Revoke it and grant it back to the platform's role (and any other role
   that uses that database):

   ```sql
   REVOKE CONNECT ON DATABASE pdp FROM PUBLIC;
   GRANT CONNECT ON DATABASE pdp TO pdp;
   ```

   Other databases on the server need the same `REVOKE`, or `pg_hba.conf` rules that let agent roles (`agent_<uuid>`)
   reach the agents database only: the CloudNativePG example does both. `doctor` warns until PUBLIC can no longer
   connect to the platform database and lists the other databases it can still connect to ([DESIGN.md](../DESIGN.md#agent-storage-and-ownership)).
3. **An OIDC client** at your identity provider, with redirect URI `https://<your host>/auth/callback`.
4. **An ingress controller and TLS** for the host in `appUrl` (the platform refuses a non-loopback `http://` URL).
5. **For `agent-sandbox`**: [Agent Sandbox](https://agent-sandbox.sigs.k8s.io) with its extensions (SandboxTemplate,
   SandboxWarmPool, SandboxClaim) installed: the maintainer runs v1.0.7, the release asset
   `sandbox-with-extensions.yaml` (`v1beta1`). Not part of this repository's checks beyond rendering the objects.
6. **Secrets**, created by you in the platform's namespace: see [`secrets.example.yaml`](secrets.example.yaml). The
   manifests only reference them (default name `pi-durable-platform`):

   | Key | |
   |---|---|
   | `DATABASE_URL` | required: the platform's database |
   | `AGENTS_DATABASE_URL` | required: the agents database with the provisioning role. Its host must resolve from the agents namespace (`service.namespace.svc`), or set `agents.runtimeDatabaseHost` / `AGENTS_RUNTIME_DATABASE_HOST` |
   | `ENCRYPTION_KEY`, `API_KEY_PEPPER` | required: `openssl rand -base64 32` each, different values. Keep them with your backups |
   | `OIDC_CLIENT_SECRET` | with OIDC |
   | `ENCRYPTION_KEY_PREVIOUS`, `API_KEY_PEPPER_PREVIOUS`, `AGENTS_MODEL_API_KEY` | optional |

   Secrets are never rendered into a manifest, the ConfigMap or the chart's values.

## After installing

```sh
kubectl -n pi-durable-platform get pods
kubectl -n pi-durable-platform exec deploy/pi-durable-platform -- /pi-durable-platform doctor
```

`doctor` checks the configuration, both databases, migrations and the agents provisioning role, and warns when PUBLIC may
still connect to the platform database (see step 2 above). The platform creates the key that signs the identity tokens MCP
servers receive when it first starts; servers verify them with `<appUrl>/.well-known/mcp-identity-jwks.json`, so MCP
servers that check the token must reach `appUrl`. Rotate the key with
`kubectl -n pi-durable-platform exec deploy/pi-durable-platform -- /pi-durable-platform rotate-mcp-identity-key`
(`doctor` warns after 90 days; see [docs/mcp-identity.md](../docs/mcp-identity.md)). Then sign in at your
`appUrl`: the person whose OIDC subject is `BOOTSTRAP_ADMIN_SUBJECT` becomes platform admin on their first sign-in
(once). Teams, agents, models and MCP servers are managed from the console, not from the deployment.

Agents use the scripted `demo` model until you add a provider (Admin > Models) or set `AGENTS_MODEL_*`.

## Replicas

The platform may run as several replicas behind its one Service: sessions, sign-in and OAuth state, rate limits, thread
events and what each agent is doing are shared through PostgreSQL and the runtimes, so there is no leader, no sticky
session and no shared disk ([docs/replicas.md](../docs/replicas.md) has the inventory and the design). What to do:

- **Two replicas** (`replicaCount: 2`; the Kustomize component `components/replicas`) is the supported setup for staying
  up through node drains and rollouts. Updates then roll one pod at a time with none missing, and the pods are spread
  over nodes and zones (preferred, so a small cluster still schedules them). Set `affinity` or
  `topologySpreadConstraints` to choose differently.
- **Add a PodDisruptionBudget** (the chart: `podDisruptionBudget.enabled`, `minAvailable: 1` by default; the Kustomize
  component `components/pod-disruption-budget`, `maxUnavailable: 1`, which is the same with two replicas), so a drain
  evicts one replica at a time.
- Use the **`kubernetes` or `agent-sandbox` launcher** (the chart has no other). `AGENTS_LAUNCHER=local` runs agents as
  children of one process and cannot be shared.
- **PostgreSQL connections** grow with the replicas: each holds a pool (at least four connections), one connection that
  listens for thread events, and a short-lived one for each advisory lock it takes (starting an agent, the reaper's
  turn). Behind a pooler, those listener and lock connections must be session connections (not transaction pooling).
- Event streams end with their replica (a shutdown closes them) and browsers reconnect to another one; the readiness
  probe fails first, so the Service stops sending new requests before that.
- `deploy/scripts/smoke-kind.sh` runs two replicas and checks that a session, an agent and its conversation are shared.

## What the manifests do not include

- **PostgreSQL** and its backups (see above).
- The **ingress controller, TLS certificates** and DNS.
- **Agent Sandbox's controller and CRDs** (for that launcher).
- **Prometheus, Grafana and a tracing backend** (the chart ships what they need: see Monitoring).
- **Direct-mode providers and MCP servers on private addresses**: agents may reach only the database, the gateway and
  DNS. Give them more with `networkPolicy.agents.extraEgress` (and the platform's with `networkPolicy.egress.extra`).

## Monitoring

The platform exports Prometheus metrics on a port of its own and, if you ask, OpenTelemetry traces (DESIGN.md,
[Observability](../DESIGN.md#observability)).

- **Metrics.** The chart enables them (`metrics.enabled`, port `metrics` 9090, a Service `<release>-metrics`). The port
  has no authentication and is not behind the Ingress. Nothing may reach it until you allow your scraper with
  `networkPolicy.metrics.from` (peers as in NetworkPolicy `from`).
  - With the **Prometheus Operator**: `metrics.serviceMonitor.enabled=true` (set `metrics.serviceMonitor.labels` to
    what your Prometheus selects by, and `namespace` if it only watches one).
  - Without it (Grafana Alloy, a plain Prometheus): scrape the endpoints of the Service `<release>-metrics`, port
    `metrics`, path `/metrics`; or use `kubernetes_sd` on the pods' container port named `metrics`. Annotation-based
    discovery is not set up: nothing is annotated.
  - Kustomize: `components/monitoring` (ServiceMonitor, the scraper's NetworkPolicy to patch, the dashboard).
- **Dashboard.** `deploy/grafana/pi-durable-platform.json` (import it, or `metrics.dashboard.enabled=true` for a
  ConfigMap labelled `grafana_dashboard: "1"`, which Grafana's dashboard sidecar loads; `metrics.dashboard.annotations`
  can set the folder). Its data source is a variable: pick your Prometheus or Mimir.
- **Alerts.** `deploy/grafana/alerts.yaml` is an example PrometheusRule (metrics missing, wake failures and slowness,
  warm-pool misses, 5xx ratio, model proxy errors, database pool exhaustion, stalled loops, idle agents never reaped,
  OAuth refresh-token reuse, rate limiting). For a Mimir or Prometheus rule file
  take `spec.groups`: `yq '{"groups": .spec.groups}' deploy/grafana/alerts.yaml`.
- **Traces.** Set `tracing.otlpEndpoint` to an OTLP/HTTP collector's base URL (for example
  `http://tempo.observability.svc:4318`; HTTP only, not gRPC) and allow the egress in
  `networkPolicy.egress.tracing.to`. `tracing.serviceName` and `tracing.sampleRatio` are optional; give the exporter
  authentication headers through `extraEnv` (`OTEL_EXPORTER_OTLP_HEADERS`, from a Secret).

## Upgrading

Set the new version (`--version` / the image digests of the release notes) and apply. The platform migrates its
database on start; a single replica is replaced, not rolled (a short outage), while several roll one at a time with
none missing (see [Replicas](#replicas)). The warm pool replaces its standby
runtimes at once; running agents move to the new runtime image when they next wake.

If your PostgreSQL still lets PUBLIC connect to the platform database (any install made before this check existed does),
`doctor` warns after the upgrade. The platform works either way; run the `REVOKE` and `GRANT` it prints, in
[Before you install](#before-you-install) step 2, and add the same `REVOKE` for other databases on the server that
agents must not reach. Nothing is revoked automatically: other roles may depend on PUBLIC's access.

## How the two stay in step

The Kustomize base, the SandboxTemplate and the components are **generated from the chart**
(`scripts/generate-kustomize.sh`; the inputs are `kustomize/generate/*.values.yaml`); what is hand-written is the
kustomization files, the overlays' small patches (the launcher's settings and Role) and the platform namespace. CI
fails when the generated files are stale, and `scripts/check-parity.sh` renders every overlay and the example
overlays and the chart with the matching values, then compares all objects (same Role rules, same NetworkPolicies,
same Deployment, ...). Change the chart first, run `scripts/generate-kustomize.sh`, commit both.

## Checks

```sh
deploy/scripts/validate.sh        # lint, render, kubeconform, generated files, parity, versions
deploy/scripts/smoke-kind.sh      # a kind cluster: install, an agent answers, isolation holds
```

By default the smoke test installs the images the chart names, the last release (`v<appVersion>`). To test others,
set (they become `images.platform` and `images.runtime`'s `repository`, `tag` and `digest`):

| Variable | Meaning |
|---|---|
| `PLATFORM_IMAGE`, `RUNTIME_IMAGE` | A full reference, `repository[:tag][@sha256:digest]`, for example `ghcr.io/ncecere/pi-durable-platform:sha-0123456789ab@sha256:...` |
| `PLATFORM_TAG`, `RUNTIME_TAG` | Only a tag, on the chart's repository (not together with the `*_IMAGE` of the same image) |
| `SMOKE_PORT`, `SMOKE_CLUSTER`, `KEEP_CLUSTER=1`, `SMOKE_HELM_ARGS` | Local port (8098), kind cluster name (`pdp-smoke`), keep the cluster, more `helm install` arguments |

The script prints the images it installed (the platform's, the runtime's and the digest the agent pod ran). Nothing it
checks depends on the images being released ones: the platform's reported version is not checked, and an image built
for a commit says `sha-<12>`.

In CI, [`wait-for-images.sh`](scripts/wait-for-images.sh) waits for the `sha-<12>` images that `images.yml` pushes for
every branch commit (up to 20 minutes, anonymous registry reads) and prints `PLATFORM_IMAGE` and `RUNTIME_IMAGE`,
pinned by digest. So the smoke job tests the commit's own code, and a release commit passes before its tag exists.
A pull request is tested at its head commit; a pull request from a fork has no images (they need registry
credentials), so the smoke job is skipped and `validate` is the check. By hand, `workflow_dispatch` takes `images`
(`release`, the default, or `sha` for the commit of the branch it runs on) or a free-form `tag`.

[`validate.sh`](scripts/validate.sh) needs helm, kubectl, kubeconform, yq and jq (`scripts/install-tools.sh` installs
pinned versions on Linux). The smoke test also needs kind and docker; it pulls the images it is given (see above), by default the released ones.
`.github/workflows/deploy.yml` runs both for changes under `deploy/`, and `.github/workflows/chart.yml` publishes and
signs the chart for `v*` tags.
