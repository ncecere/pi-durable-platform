# Security policy

## Reporting a vulnerability

Please report vulnerabilities **privately**. Don't open a public issue, pull request or discussion.

Use GitHub's private vulnerability reporting: **[Report a vulnerability](https://github.com/ncecere/pi-durable-platform/security/advisories/new)** on the repository's Security tab. Only the maintainers see the report, and we work on the fix with you in a private advisory.

Include:
- the affected version or commit;
- what an attacker can do, and what they need first (an account, a team or platform role, a personal API key, a service account key, an OAuth Connect app, control of a model provider or MCP server, an agent's runtime, network position);
- steps or a proof of concept to reproduce it;
- any suggested fix.

Pi Durable Platform has one maintainer. We aim to reply within 5 business days and to agree a disclosure date with you once we understand the problem. We credit reporters in the fix's release notes unless you'd rather we didn't. Please give us reasonable time to fix the problem before you disclose it. Don't access or change data that isn't yours while testing, and don't send traffic to model providers, MCP servers or third-party services through an installation you don't operate.

## Supported versions

Pi Durable Platform is pre-1.0 development software. Security fixes are made on `main` and released as a patch of the latest minor release only.

| Version | Supported |
|---|---|
| `main` and the latest release (v0.1.0) | Yes |

## Scope

In scope: the code in this repository, including
- the `pi-durable-platform` binary (management API, agent proxy, model gateway and MCP proxy, OAuth authorization server, launchers, background loops, migrations and CLI) and its default configuration;
- the agent runtime in `runtime/` and its PostgreSQL storage;
- the web UI in `web/`;
- the container images built from `Dockerfile` and `runtime/Dockerfile`.

Areas we care most about:
- **agent isolation:** one agent reading or writing another agent's schema, taking over its runtime, or activating a standby runtime; a runtime accepting requests without its own token;
- **conversation privacy:** reading a private thread you didn't start, a service-managed agent's conversations without being one of its team's service accounts, or a conversation as a platform admin or auditor without the reasoned, audited read;
- **team isolation and roles:** a member reading or changing another team's agents, templates, models, MCP servers, service accounts, usage or audit log, using another team's models or MCP servers, or gaining a platform or team role;
- **credentials:** API keys, service account keys, OAuth tokens or codes, provider keys or MCP headers that keep working after revocation or expiry, are stored in plain text, reach the browser, the logs, the audit log or an agent that shouldn't have them;
- **the OAuth authorization server:** PKCE, redirect URI and resource checks, consent, client metadata documents and dynamic registration, refresh token rotation, an app token getting its person's platform role;
- **the model gateway and MCP proxy:** an agent using a model or a tool its template version doesn't allow, or another agent's credential;
- **SSRF:** reaching loopback, link-local, metadata or internal addresses that aren't allowed through team model providers, team MCP servers, app-supplied thread MCP servers, client metadata documents or redirects;
- **audit:** changing append-only records, or an audited action that isn't recorded;
- browser sessions, CSRF and Origin checks.

Out of scope:
- a specific installation's infrastructure, identity provider, Kubernetes cluster, NetworkPolicies, model providers, MCP servers or configuration choices (report those to the installation's operator);
- the development sign-in and the development credentials in `compose.yaml` and `.env.example`, all refused off a loopback `APP_URL`, and the `local` launcher (development only);
- what a model or an MCP server does with a request it was allowed to receive, and prompt injection through messages or tool results;
- code in templates: templates are trusted, admin-approved code (the known limits in [DESIGN.md](DESIGN.md) describe what that trust covers);
- denial of service by volume alone, and findings that need a compromised host, database owner role or `ENCRYPTION_KEY`;
- vulnerabilities in dependencies, including Pi and pi-durable, with no demonstrated impact on this project (report them upstream).
