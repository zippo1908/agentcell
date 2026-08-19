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

### Added since alpha.6 (unreleased)

- **A preview nobody configures** — the create form no longer asks for a
  command. The runtime reads the checkout and works one out; when it finds
  nothing to serve it says which, instead of serving nothing silently
  ([ADR-0014](adr/0014-preview-without-a-command.md)). This also fixed a
  one-element `preview.command` being exec'd as a filename by the anchor,
  which had held a real anchor `NotReady` for sixteen hours.
- **A project before its repository** — projects are usually agreed on
  before somebody creates the GitLab repo for them, so `repoURL` is optional
  and attached later. Replacing an attached repository is refused: that is a
  migration, not a form field.
- **Mentions that reach people** — typing `@` on the board lists the
  project's members and the agent. Mentions resolve by the name on somebody's
  address rather than a hashed id, which nobody typed — so in practice nobody
  had ever been mentioned. Ambiguity is refused rather than guessed, and an
  `@` that reaches nobody answers in the stream.
- **May-create-projects** — granted on the invitation, by whoever decides to
  bring somebody in. Existing accounts keep it across the upgrade.
- **Personal forge tokens** — bind your own GitLab/GitHub token in 我的凭据;
  it is projected as a credential your projects may use, and never lent.
- **The project's own page** — knowledge base, members (by address, not
  hash) and the project's credential, each a tab. The preview left the tab
  strip: it opens in its own window.

### Added in alpha.5 → alpha.6

- **Accounts** — invitations, email login, one principal per person, kept in
  a SQLite file on a volume. No self-registration: an account here comes
  with a shell inside the cluster. A password change ends every session,
  because the cookie signature covers the hash.
- **Project files** — upload a spec or a spreadsheet; text is extracted once,
  at upload, and lands in the worktree at `.agentcell/library/` so the agent
  reads it with the same Read and grep it uses for code.
- **The agent's own interface** — a resident session runs the CLI the way a
  person would, welcome screen and slash commands included, and a follow-up
  is typed at it rather than started as a new process.
- **Preview in the workspace** — a project serves its work-in-progress from
  the session that made it, shown in a tab beside the terminal on its own
  origin.
- **A session survives its runtime** — a rebuilt runtime reopens the CLI on
  the conversation already in that worktree, so what comes back is the
  session rather than a blank shell. Per runner, because the mechanism is
  the runner's: `claude --continue` and `kimi -c` have it; Codex does not
  declare an interactive resume and continues from its state directory on
  the next instruction instead.

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
- **One live session per person per project** — the agent CLIs already open
  and switch conversations; a second layer of that only made it possible to
  be locked out of your own work. This is a ROUTING rule, not the quota:
  `maxSessions` counts sessions, because a shared conversation is one slot
  whether two people type into it or ten.
- **A board per project** — a stream where saying something IS asking that
  project's agent. The conversation is the project's own session, funded by
  the person who opened it and drivable by anyone who may dispatch there;
  who pays and who types are different questions and only the second is
  shared. (There is no team layer: the project's member list is the scope.)
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

## Security and identity, in order

This order is deliberate, and it is not the order the work *looks* most
impressive in. Two things separate the top of this list from the bottom:
whether a failure needs an adversary, and whether the fix gets more expensive
the longer it waits.

**Assumed threat model.** Careless agents, ordinary internal users, and — the
one that is specific to this kind of platform — an agent acting on injected
instructions it read in a repository, an issue, or a web page. A malicious
administrator with host access is explicitly *not* a defence target at this
stage. Twenty to thirty-five developers is phase one, not the architectural
ceiling.

### P0 — Authorization fails closed ✅ done

An unreachable account store used to widen everyone's authority: the error
was indistinguishable from "this identity has no account row", which is a
permit. One disk-full event handed out project creation.
[ADR-0015](adr/0015-authorization-fails-closed.md). The control plane may
stop work when it cannot be read; it may not grant it. The deployment token
is the single, deliberate break-glass path, resolved before the store is
consulted so it survives the outage that makes it necessary.

### P0 — A Principal ID that does not move

Today `ID() = hash(subject)`, so identity is *derived from the way a person
authenticated*, and that value is denormalized into Kubernetes objects, Unix
uids, Secret labels and audit records — four places that cannot be updated in
one transaction. Changing IdP therefore changes who everybody is.

The correct shape inverts it: a permanent opaque internal id, with external
identities as attributes of it.

    principal_id = "01J…"          ← internal, opaque, allocated once
      ├── binding: casdoor / subject=…
      ├── binding: entra   / subject=…
      └── binding: email   / …

A Principal is an entity in the ontology; an OIDC identity is one of its
*identifiers*, not its primary key. Done now this is a model change. Done
after Casdoor, group IAM and org sync are connected, it is an identity
migration project touching four systems.

### P1 — Egress that is denied by default and attributed

Every pod in a cell namespace may currently reach any host on 443. That is
not gratuitous — model APIs, git, and package mirrors all need to go out —
but it means the carefully built merge/review/release gate controls only what
an agent can *bring back*. What it can *send out* is unrestricted, and
prompt injection makes that a live path rather than a theoretical one.

The answer is not to close 443; it is to make egress go through something
that can name what it allows and record what it did:

    agent runtime → egress gateway → { model APIs, forges, approved mirrors }

with default-deny, FQDN policy, request audit, and — the part that makes it
worth more than a firewall — attribution back to principal, cell and session,
so the record reads `user → session → agent → destination`. That is the audit
trail an enterprise agent platform actually needs, and it composes with the
identity work above rather than duplicating it.

### P1 — Separate the control and runtime fault domains

celld, the account store and the audit record currently share a node with the
agent workloads, whose requests are oversubscribed against their limits by
roughly 48× with no admission control. An agent that goes wide takes
authorization and audit down with it — and takes down the very thing that
would explain what happened.

This is fault containment, not scale: it wants one small control node and
one or more runtime nodes, plus quotas and an admission policy. It does not
want a microservice fleet, a dozen nodes, or a message bus.

### P2 — `open` must not mean `maintainer`

A memberless project currently treats every authenticated user as a
maintainer, which makes the default state of a new project "anyone on the
deployment can release from it". Replace the binary with `private` (default)
/ `organization` / `open`, where open grants viewer or member and maintainer
comes only from an explicit role binding.

### P2 — Casdoor, enterprise OIDC, org sync

See [AUTHENTICATION.md](AUTHENTICATION.md). Gated on the Principal ID work
above, not the other way round.

### P3 — Role / Permission / RoleBinding as data

The authorization control plane proper: policy that changes without a
deploy, a scope hierarchy, and a global view of who holds what. The seams
(`can`, `canPlatform`, `Decision`) exist for this and no caller changes when
it lands.

### P3 — Identity propagation to agents, MCP servers and tools

Once a principal is stable and egress is attributed, the same identity can
follow a call all the way into the tools an agent invokes.

> The ordering has one counter-intuitive consequence worth stating: **do not
> start by deciding which roles go in the JWT.** Identity correctness, the
> network boundary, the failure boundary and the trust boundary are all below
> that layer, and a role model built on top of unstable foundations is a
> console that looks governed.

## Next

### Near term
- **A terminal in the browser** — tmux over WebSocket. Attaching works today
  (`kubectl exec … cell-runtime attach <id>`), over the same exec path a web
  terminal would use; what is missing is the front end.
- **Release automation** — the workflow cannot push images because the
  container packages are user-scoped and unlinked from the repository
  ([#16](https://github.com/zippo1908/agentcell/issues/16)). Release artifacts
  are complete; only the automation is not.
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
