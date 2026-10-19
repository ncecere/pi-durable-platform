# Deploying Pi Durable Platform on Kubernetes

Two ways to the same objects, kept in step by CI:

| | Where | Use it when |
|---|---|---|
| **Helm chart** | [`helm/pi-durable-platform`](helm/pi-durable-platform/README.md), published as `oci://ghcr.io/ncecere/charts/pi-durable-platform` | you want values instead of patches (the source of the other) |
| **Kustomize** | [`kustomize`](kustomize/README.md): a base, overlays for each launcher, components, an example | your cluster is managed with Kustomize or Flux/Argo CD on plain manifests |

Also here: [`secrets.example.yaml`](secrets.example.yaml) (the Secrets the platform reads, with fake values) and
[`examples/cloudnative-pg`](examples/cloudnative-pg) (PostgreSQL 17 with CloudNativePG, and its NetworkPolicies).

## What gets deployed

- **The platform**: one Deployment (one replica, see below) with two Services: the web/API port (8098, Service port 80)
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

`doctor` checks the configuration, both databases, migrations and the agents provisioning role. Then sign in at your
`appUrl`: the person whose OIDC subject is `BOOTSTRAP_ADMIN_SUBJECT` becomes platform admin on their first sign-in
(once). Teams, agents, models and MCP servers are managed from the console, not from the deployment.

Agents use the scripted `demo` model until you add a provider (Admin > Models) or set `AGENTS_MODEL_*`.

## What the manifests do not include

- **PostgreSQL** and its backups (see above).
- The **ingress controller, TLS certificates** and DNS.
- **Agent Sandbox's controller and CRDs** (for that launcher).
- **Several replicas**: the platform runs one replica; the idle reaper only knows the streams open on its own process
  (roadmap item B, [#2](https://github.com/ncecere/pi-durable-platform/issues/2)). The chart's schema refuses more.
- **Metrics**, dashboards and tracing (not built yet).
- **Direct-mode providers and MCP servers on private addresses**: agents may reach only the database, the gateway and
  DNS. Give them more with `networkPolicy.agents.extraEgress` (and the platform's with `networkPolicy.egress.extra`).

## Upgrading

Set the new version (`--version` / the image digests of the release notes) and apply. The platform migrates its
database on start; the single replica is replaced, not rolled (a short outage). The warm pool replaces its standby
runtimes at once; running agents move to the new runtime image when they next wake.

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

[`validate.sh`](scripts/validate.sh) needs helm, kubectl, kubeconform, yq and jq (`scripts/install-tools.sh` installs
pinned versions on Linux). The smoke test also needs kind and docker; it pulls the released images the chart names.
`.github/workflows/deploy.yml` runs both for changes under `deploy/`, and `.github/workflows/chart.yml` publishes and
signs the chart for `v*` tags.
