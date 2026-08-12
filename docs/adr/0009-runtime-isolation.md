# ADR-0009: Runtime isolation — the pod is the boundary, the UID expresses it

Status: accepted
Builds on: [ADR-0008](0008-user-identity-and-ownership.md) (who you are)

## Context

ADR-0008 gave AgentCell users, ownership and a control plane that refuses to
show one person another's work. It stopped at the control plane on purpose.
Underneath, nothing had changed: every workload still ran as `uid=1000` on
one shared volume, so the guarantee was only as strong as "nobody execs into
a pod". `deploy/identity/README.md` said so in as many words.

That is the gap this ADR closes for files and processes.

## Decision

### 1. A user's pods run as that user's own Unix UID

The pod is the boundary; the UID is how the filesystem expresses it. Two
users' sessions were already separate pods — now they are separate *users*,
so one cannot read the other's files even though both mount the same project
volume.

`RunAsGroup` and `fsGroup` stay the project group. That is not an oversight:
group membership is precisely what still lets privately-owned processes
collaborate on the shared checkout. The UID withholds everything else.

### 2. UIDs are allocated and recorded, never derived, never recycled

Hashing a user id into the UID space would need no state and is wrong:
hashes collide, and a collision here means two people *are* the same Unix
user — the exact property this layer exists to prevent.

Allocations are also never released. A recycled UID silently inherits the
previous holder's files, so a departed user keeps their number forever. The
record (`agentcell-uids` ConfigMap) is a tombstone as much as a mapping, and
a corrupt entry is a hard error rather than a fresh allocation — handing
someone a second identity while their files still belong to the first is
worse than refusing to start the session.

The range starts at 100000, above anything a distribution hands out, so a
platform UID can never coincide with one baked into a container image.
Exhausting it is a loud error, never a wrap.

### 3. Private state lives in a private tree, mode 0700

`/workspace/users/<uid>/` holds the user's worktrees and `$HOME` — which is
where the agent CLIs keep configuration, credentials and transcripts. It is
created `0700` explicitly, not by `MkdirAll` alone, because `MkdirAll`
honours the umask and would leave `0755` under the usual `022`.

Worktrees stay on the same volume as the shared clone because a git worktree
cannot span filesystems; the mode bits, not a separate volume, are what
separate them.

### 4. A followed session serves its own preview

"Watch the agent work while you recalibrate" used to be served by the anchor,
which read the session's worktree directly. It cannot any more, and should
not: the anchor belongs to the project, the worktree belongs to a user.

So the session pod serves its own preview, and the Cell's preview Service
selects that pod for as long as it is the followed one. The capability is
unchanged from the user's point of view; what moved is which process holds
the file handle. This is also the shape the resident per-user runtime wants,
so it is a step toward that rather than a workaround.

## Consequences

- celld needs `configmaps` in the control namespace for the allocation
  record.
- Per-user UIDs only activate when an identity provider is configured. With
  static tokens every caller is the same principal, and the shared project
  identity is the honest representation of that — no behaviour change.
- Existing Cells keep working: sessions with no recorded owner run as the
  project UID exactly as before, and their worktrees are simply owned by
  that user.
- The devbox image no longer dictates the UID its processes run as, so it
  must not depend on a `/etc/passwd` entry existing for the running UID.
  Tools that insist on a resolvable user need `nss_wrapper` or a writable
  `/etc/passwd`; the agent CLIs in the reference image do not.

## What is still not isolated

Honesty about the remaining edges matters more than a clean-sounding ADR:

- **The shared project volume is shared.** `/workspace/repo`, the knowledge
  directory and the settled branches are the collaboration layer by design.
  A user can read the project's code — that is the point.
- **No per-user network policy.** Two users' session pods can reach each
  other on the pod network. Nothing sensitive is served there today, but it
  is not a boundary.
- **No resident runtime yet.** Sessions are still one-shot pods, so there is
  no long-lived tmux server to attach to and no CLI-native resume. Session
  memory therefore survives only as files under `$HOME` in the private tree
  — which is what makes resume possible later, but resume itself is not
  implemented.
- **Kernel-level isolation is what the node gives you.** Same node, same
  kernel. For tenants that do not trust each other, separate node pools or a
  sandboxed runtime (gVisor, Kata) is the answer, not a UID.
