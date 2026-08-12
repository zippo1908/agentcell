# Security Policy

AgentCell runs autonomous AI agents against real repositories and holds forge
and model credentials, so we take security seriously and design for the case
where agent- or repo-controlled code is hostile.

## Reporting a vulnerability

**Do not open a public issue for a security problem.**

Report privately via GitHub's **[Report a vulnerability](https://github.com/zippo1908/agentcell/security/advisories/new)**
(Security → Advisories). Include:

- affected version / commit,
- a description and, ideally, a reproduction,
- the impact you believe it has.

We aim to acknowledge within a few working days, agree a fix timeline, and
credit you in the advisory unless you prefer to stay anonymous. Please give us
reasonable time to release a fix before any public disclosure.

## Supported versions

Pre-1.0: only the latest `v0.1.0-alpha.*` release and `main` receive fixes.

## Security model (what we defend, and current limits)

Read [docs/adr/0001-architecture.md](docs/adr/0001-architecture.md) and
[docs/adr/0005-git-broker.md](docs/adr/0005-git-broker.md) for the full
design. In short:

- **Cross-project isolation is strong; within a project it is advisory.** Each
  Cell gets its own namespace, non-root pods (PSS restricted, seccomp,
  drop-ALL caps), and default-deny NetworkPolicy. Sessions of the *same* Cell
  share the workspace PVC by design.
- **Credentials are scoped tightly.** Model keys are injected per session
  (never in a pod spec literal). With the git-broker (default), **no workload
  pod holds the forge token** — anchor/settle/prod authenticate to the broker
  with an audience-bound ServiceAccount token; only the settle role may push,
  and only to its own `session/<id>` branch. Session pods carry no token at
  all.
- **Preview and production content is untrusted.** `/preview/<cell>/` and
  `/app/<cell>/` serve repo- and agent-authored code from the control
  plane's own origin. It is therefore confined two ways: the UI frames it
  with a `sandbox` attribute that omits `allow-same-origin`, and celld
  stamps an equivalent `Content-Security-Policy: sandbox …` on every
  proxied response so the confinement also applies when an operator opens
  the URL directly. Both give the document an **opaque origin**: its
  scripts cannot read the console's DOM, cannot use our cookie, and cannot
  navigate the top-level page. Trade-off: a previewed app that relies on
  its own cookies/storage degrades. A dedicated preview origin remains the
  stronger long-term answer.
- **Cookie-authenticated writes require same-origin provenance.** Because
  untrusted preview content is *same-site* with the console, SameSite
  cookies provide no protection at all. Every state-changing request
  authenticated by cookie must carry a matching `Origin` (or same-origin
  `Referer`); `Origin: null` — what a sandboxed document sends — and
  requests with no provenance are refused with 403. Bearer-token callers
  (CLI/API) are exempt, since a browser cannot attach a header on someone
  else's behalf.
- **Trust assumption — tenants must not have direct Kubernetes access to
  `cell-*` namespaces.** The broker binds identity to the pod's
  audience-scoped token, verifies the pod's uid and its controller
  ownerReference (settle pushes require a real `settle-<id>` Job pod), and
  requires a `repo_url` binding on each credential. These raise the bar, but
  a tenant who can create pods/Jobs in a cell namespace could still forge a
  workload identity — so cluster RBAC must not grant tenants that access.
- **Known limits (tracked, not hidden):** single celld replica (no HA); the
  git-broker is a high-value component holding all forge credentials (harden
  the cluster accordingly; its RBAC has no cluster-wide secret access); a
  non-enforcing CNI silently ignores our NetworkPolicies — verify yours
  enforces them.

If you find a way to cross any of these boundaries — exfiltrate a token,
reach another cell, push outside `session/*`, or escape a pod — that is
exactly what we want to hear about privately.
