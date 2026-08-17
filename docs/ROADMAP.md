# Roadmap

Living view of where AgentCell is and where it's going. The
[README implemented-vs-designed table](../README.md) is the authoritative
per-feature status; this page groups it into themes and sequence, and says
what each thing was verified against.

Nothing is listed as done here unless it has been exercised on a real
cluster — see [E2E_RESULTS.md](E2E_RESULTS.md) for the runs.

## Done (through v0.1.0-alpha.6)

- **Core model** — resident Cell (namespace + anchor StatefulSet + workspace
  PVC), Session slots, mandatory settle (push-confirmed-or-retry, real-git
  tested). *Runs 1–3.*
- **Calibration loop** — resident product preview + web console, two-zone
  release with rollback, review queue → PR with merge tracking
  ([ADR-0006](adr/0006-review-queue-and-pr.md)). *Run 4.*
- **git-broker** — the forge token is in no workload pod; per-role
  ServiceAccounts, audience-bound tokens, repo↔credential binding, create-only
  `session/<id>` ([ADR-0005](adr/0005-git-broker.md)). GitHub and self-hosted
  **GitLab**. *Run 4.*
- **Untrusted content isolated by origin** — each Cell *zone* on its own host,
  single-use tickets, no platform credential ever reaches repo code
  ([ADR-0007](adr/0007-preview-origin-separation.md)). *Run 4.*
- **User identity** — OIDC verified by celld itself, no trusted headers and no
  gateway required; immutable Session owner; not-yours answers 404
  ([ADR-0008](adr/0008-user-identity-and-ownership.md)). Casdoor + Apache
  APISIX® ship as an optional path.
- **Runtime isolation** — per-user Unix uid (allocated, never recycled),
  `0700` private tree for worktrees, `$HOME`, CLI state and the tmux socket
  ([ADR-0009](adr/0009-runtime-isolation.md)). *Run 5: two users, one Cell,
  neither can read the other.*
- **Resident sessions** — the slot outlives the agent; follow-ups continue the
  CLI's own conversation; settle stays mandatory
  ([ADR-0010](adr/0010-resident-sessions.md)). *Run 6.*
- **One runtime per user** — a user's sessions are windows in one tmux server;
  the model key never appears in argv. *Runs 7–8.*
- **Git isolation without a git proxy** — per-user repositories over a shared
  read-only base ([ADR-0012](adr/0012-git-isolation-decision.md)). *Run 8:
  an unpublished commit is present in its author's object store and absent
  from the shared one.*
- **Capacity is enforced, not conventional** — every workload declares
  requests and limits; a ResourceQuota caps each Cell namespace. It bounds
  the Cell, not each session: a resident session is a window inside its
  owner's runtime and shares that pod's budget, because Kubernetes reserves
  per pod and a window is not one.
- **Runners and providers are data** — add a CLI or fix a renamed flag in
  `runners.d/*.yaml` without a release ([docs/RUNNERS.md](RUNNERS.md)).
- **Production can live elsewhere** — a Cell either runs production in its own
  isolated zone or hands the release off to a system that owns running it,
  with a signed webhook.
- **Packaging** — Helm chart and images on GHCR, cloud presets for k3s /
  Alibaba ACK / Tencent TKE.

### Added in alpha.5 → alpha.6

Everything here was exercised on the single-node k3s cluster; the ones that
were not are named as such.

- **A terminal you can open and type into** — xterm.js attached over a
  WebSocket to the same tmux window the agent is running in. Only the
  session's owner may attach; a project maintainer may not. Sessions are
  resident by default, because a headless agent prints nothing until it
  finishes and "working" and "stuck" then look identical.
- **Idle means asleep, not finished** — a session nobody is using gives back
  its slot and its runtime after ~15 minutes and keeps its worktree and
  conversation. Opening its terminal or asking one more thing wakes it where
  it was; the agent is not re-run. A session nobody returns to is published
  after a week, not deleted.
- **One live session per person per Cell** — the agent CLIs already open and
  switch conversations; a second layer of that only made it possible to be
  locked out of your own work when slots filled. The slot cap now bounds
  people, not tasks.
- **Teams and a board** — a membership list that outlives one project, and a
  stream where `@cell do the thing` dispatches and answers in the same place.
  The board's conversation with a project is the team's own session, not the
  asker's.
- **A project made of several repositories** — one workspace, one agent, N
  repositories side by side. Each keeps its own remote, base branch and
  credential, and a session that touches three produces three branches,
  reviewed separately: there is no cross-repository atomicity to be had, so
  the platform does not imply one. Existing single-repo projects are
  unchanged — same paths, same URLs, no migration.
- **PlacementClass** — an administrator offers machine pools; a maintainer
  chooses from that list and can express nothing else. Replaces a control
  that accepted any node label and derived tolerations from the taints it
  found, which let a project role cross a cluster-admin boundary.
- **celld leader election** — one replica reconciles, every replica serves
  the console; ~4s takeover measured. Preview tickets are now single-use
  across replicas rather than once per process.
- **Prometheus metrics** — `agentcell_cell_active_sessions` and
  `agentcell_cells_total` (#41, thanks @OdaloV). Known gap on multi-replica
  deployments: #42.
- **Creating a project is choosing, not typing** — devbox, runner and
  provider offered as cards narrowed to what can actually be driven.
- **Database configuration slot** — dev and prod name separate secrets; the
  platform injects a connection and never provisions a database.
- **In-cluster registry** for clusters that cannot reach ghcr.io, plus an
  814 MB alpine devbox.

## Next

### Near term
- **A terminal in the browser** — tmux over WebSocket. Attaching works today
  (`kubectl exec … cell-runtime attach <id>`), over the same exec path a web
  terminal would use; what is missing is the front end.
- **Release automation** — the workflow cannot push images because the
  container packages are user-scoped and unlinked from the repository
  ([#16](https://github.com/zippo1908/agentcell/issues/16)). Release artifacts
  are complete; only the automation is not.
- **Resume across a replaced runtime, automatically** — a lost runtime is
  rebuilt and the window restored, but the restored window is a fresh shell:
  it is the NEXT follow-up that continues the conversation, because
  `continue` resumes by the recorded id (Claude) or the per-session state
  directory (Codex). Re-attaching at recovery time, without waiting for the
  user to type, is not wired up.

### Medium term
- **Multi-node** — the workspace PVC is ReadWriteOnce and every pod of a Cell
  is pinned to the anchor's node, so a Cell cannot outgrow one machine. RWX
  storage is what lifts that ceiling.
- **A control plane that scales** — celld is a single replica with no leader
  election, and preview traffic for every Cell is proxied through it.
- **Knowledge** — indexed retrieval over `/workspace/knowledge`, with review
  feedback distilled back in.
- **agent-sandbox substrate** ([ADR-0004](adr/0004-agent-sandbox-adoption.md))
  — run the anchor on the K8s SIG `Sandbox` CRD behind a driver seam.

### Exploratory
- **Audited git** — if a deployment must be able to reconstruct every git
  operation an agent performed, [ADR-0011](adr/0011-git-boundary.md) is the
  design for it. ADR-0012 explains why isolation alone does not need it, and
  names this as the condition that would flip the decision.
- Per-user NetworkPolicy; short-lived GitHub App tokens as the default path;
  broker policy beyond `session/*`; metrics and per-session quality signals.

## Known limits

Stated here rather than discovered later; [SECURITY.md](../SECURITY.md) has
the full list.

- A resident session shares its owner's runtime budget; a slot's CPU and
  memory are not reserved for one window.
- A model key is private to a **user**, not to a session: windows in one
  runtime share a uid, and `/proc` lets a sibling read an environment.
- One runtime is one OOM envelope — it takes that user's sessions with it.
- Same node, same kernel. Tenants who do not trust each other need separate
  node pools or a sandboxed runtime, not a uid.

## How to influence this

Open a [Discussion](https://github.com/zippo1908/agentcell/discussions) under
**Ideas** for direction, or an issue for a concrete unit of work. Items marked
[`good first issue`](https://github.com/zippo1908/agentcell/labels/good%20first%20issue)
are scoped for newcomers.
