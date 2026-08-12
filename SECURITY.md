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
- **Preview and production content is untrusted, and served from its own
  origin.** `/preview/<cell>/` and `/app/<cell>/` run repo- and
  agent-authored code, so celld serves them on a **separate origin** from
  the console (default port 8081; set `--preview-origin` to a distinct
  hostname in production). Cross-origin scripts cannot read the console's
  DOM, cookie-authenticated writes from that origin fail the Origin check
  below, and CORS keeps API responses from being exposed to them. Because
  the content owns its origin, a previewed app keeps full same-origin
  powers over *itself* — cookies, localStorage, service workers — and is
  not degraded. A residual CSP `sandbox` still forbids one thing:
  navigating or replacing the top-level console page. See
  [ADR-0007](docs/adr/0007-preview-origin-separation.md).
  Each Cell **zone** gets its own host — `<cell>-dev.<domain>` and
  `<cell>-prod.<domain>` — so one Cell cannot read another's, and the
  agent's unreviewed work cannot read, restyle or service-worker the
  released build.
  **The console credential is never accepted there:** the console mints a
  **2-minute, single-use** HMAC ticket bound to **cell + zone + host**
  (nonce-tracked, so one captured from history or a log cannot be
  replayed); the preview listener exchanges it for an 8-hour session cookie
  scoped to that zone's path. Neither the platform cookie nor a bearer
  token works against untrusted content — **and the proxy strips
  `Authorization` and every platform-reserved cookie from its outbound
  request**, so the upstream never observes them either. The previewed
  app's own cookies pass through.
  **Deployment requirements:** set `--preview-domain` (wildcard DNS +
  certificate); without it all Cells and both zones share one preview
  origin and only path-scoped tickets separate them. Prefer a **different
  registrable domain** from the console, so no subdomain relationship
  exists at all.
- **Cookie-authenticated writes require same-origin provenance.** Because
  untrusted preview content is *same-site* with the console, SameSite
  cookies provide no protection at all. Every state-changing request
  authenticated by cookie must carry a matching `Origin` (or same-origin
  `Referer`); `Origin: null` — what a sandboxed document sends — and
  requests with no provenance are refused with 403. Bearer-token callers
  (CLI/API) are exempt, since a browser cannot attach a header on someone
  else's behalf. Over TLS the session cookie carries the **`__Host-`
  prefix** (browser-enforced host-only, Secure, Path=/), which also defeats
  cookie tossing from a sibling subdomain.
- **`X-Forwarded-*` is not trusted unless you say so.** Honouring it blindly
  would let anyone who can reach celld directly declare what our origin is
  and walk through the same-origin check. Enable
  `--trust-forwarded-headers` only behind a gateway that **overwrites**
  those headers (APISIX: set, do not append) and where celld cannot be
  reached bypassing it.
- **Trust assumption — tenants must not have direct Kubernetes access to
  `cell-*` namespaces.** The broker binds identity to the pod's
  audience-scoped token, verifies the pod's uid and its controller
  ownerReference (settle pushes require a real `settle-<id>` Job pod), and
  requires a `repo_url` binding on each credential. These raise the bar, but
  a tenant who can create pods/Jobs in a cell namespace could still forge a
  workload identity — so cluster RBAC must not grant tenants that access.
- **User identity (ADR-0008):** celld verifies the OIDC ID token itself —
  issuer, audience, signature against the provider's JWKS. It never trusts an
  identity header, because anything on the pod network could send one. A
  Session's owner is immutable (enforced by the CRD, so `kubectl edit` cannot
  change it), a model credential can only be spent by the user who owns it,
  and asking about something you do not own answers **404, never 403** — 403
  confirms existence, and a few probes then map out other people's work.

- **Between users (ADR-0009):** each user's workloads run as their own
  allocated Unix uid — recorded, never derived, never recycled — and their
  worktrees, `$HOME`, CLI state and tmux socket live in a `0700` private tree
  on the shared volume. A peer's pod runs as a different uid, so the kernel
  is what withholds them. `fsGroup` stays the project group, which is what
  still lets everyone collaborate on the checkout.

- **Known limits (tracked, not hidden):** single celld replica (no HA); the
  git-broker is a high-value component holding all forge credentials (harden
  the cluster accordingly; its RBAC has no cluster-wide secret access); a
  non-enforcing CNI silently ignores our NetworkPolicies — verify yours
  enforces them.

  Specific to the per-user runtime (ADR-0010), stated plainly because they
  are easy to overestimate:

  - **A model key is private to a user, not to a session.** It never appears
    in argv, shell history or on disk beyond a `0600` file the window sources
    and unlinks — but every window in a runtime runs as the same uid, and
    under the default `/proc` model a process can read a sibling's
    environment. Per-session secrecy needs per-session uids or pods, which is
    what sharing a runtime deliberately trades away.
  - **One runtime is one resource envelope.** Kubernetes bounds the user, not
    the session: an OOM in a runtime takes every session that user has open
    in that Cell.
  - **No per-user NetworkPolicy.** Two users' workloads can reach each other
    on the pod network.
  - **The project layer is shared on purpose.** The checkout, the knowledge
    directory and settled branches are readable by every member — that is
    what a project is. Privacy applies to unpublished work, not to the code.
  - **Same node, same kernel.** For tenants who do not trust each other, use
    separate node pools or a sandboxed runtime (gVisor, Kata); a uid is a
    filesystem boundary, not an isolation one.

If you find a way to cross any of these boundaries — exfiltrate a token,
reach another cell, push outside `session/*`, or escape a pod — that is
exactly what we want to hear about privately.
