# ADR-0011: Only the scheduler runs git

Status: proposed
Builds on: [ADR-0005](0005-git-broker.md) (who holds the forge credential),
[ADR-0009](0009-runtime-isolation.md) (whose files these are)

## Context

ADR-0005 took the forge credential away from every workload: no session pod
holds a token, and pushes go through the git-broker, which checks the pod's
identity and refuses anything outside `session/<id>`.

That is remote authority. **Local git authority was never taken away.** As of
today:

- `cell-runtime` runs `git worktree add` inside the session's own process;
- settle runs `git add`/`commit`/`push` in a Job that mounts the same volume;
- the agent's container has `git` on `$PATH` and a real `.git` directory
  reachable from its worktree — the *shared* object store, which
  `core.sharedRepository=group` deliberately made group-writable so several
  users could commit into it (ADR-0009).

So an agent can `git checkout` another branch, rewrite history in its
worktree, read every object in the store — including objects belonging to
other users' unpublished worktrees — and generally do anything git can do
locally. The broker stops it from *publishing* that, which is the property
that protects the forge. It does not stop it from *doing* it.

This is the gap between "the scheduler owns git" and what is implemented, and
it is an architecture gap, not a documentation one.

Why it matters more now than it did at M0: the object store is shared between
users. Before per-user identity there was one tenant per Cell and "the agent
can read the repo" was uninteresting. Now `/workspace/repo/.git` is
group-readable by every member so that commits work, which means **one user's
agent can read another user's unpublished commits** the moment they are
written into the shared store.

## Decision

The agent's working directory contains **no repository**. Every git
operation, local included, is performed by a component the agent cannot
reach, and the agent asks for the few it is allowed to have.

### 1. `gitd` — one local git authority per Cell

A pod per Cell, running as its own uid (`ProjectUID`, distinct from every
user uid), owning `/workspace/repo` at mode `0700`. Nobody else can read the
object store — not another user, not the agent, not the settle Job.

It exposes exactly four operations over a unix socket in each user's private
tree, mounted read-write for that user alone:

| Operation | Meaning |
| --- | --- |
| `materialize(session, ref)` | lay down the working files for a session |
| `status(session)` | what changed, as a list |
| `diff(session[, path])` | the patch, for review |
| `publish(session, message)` | commit and push `session/<id>` via the broker |

The shape is deliberately ADR-0005's: a fixed allow-list, not a git proxy.
"Run this git command for me" would hand back everything this ADR removes.

### 2. A worktree is a plain directory

`gitd` materializes files with `git --git-dir=<repo>/.git --work-tree=<dir>
checkout`, using a per-session index (`GIT_INDEX_FILE`) so sessions never
share one. The directory the agent gets has **no `.git`** — not a directory,
not a link, not a `.git` file pointing anywhere.

The consequence is exact: `git status` in an agent's worktree fails with "not
a git repository". There is nothing to reach, so there is nothing to
authorize.

### 3. The agent keeps read-only history, through the allow-list

Losing `git log` and `git blame` would be a real regression — agents use
history to understand code, and pretending otherwise would make this ADR a
trade of capability for tidiness. `status` and `diff` cover review; `log`
and `show` for the *base ref* are read-only queries `gitd` can answer safely
and should, as a second allow-listed pair, before this lands.

What stays gone: reading other sessions' objects, mutating refs, rewriting
history, fetching arbitrary remotes.

### 4. settle becomes `publish`

The settle Job stops running git. It calls `publish`, and `gitd` does the
commit and the broker push. The Job keeps its role — the thing that *decides*
work is finished and reports the verdict — but stops being a second place
that can write to the object store.

The push-confirmed-or-retry guarantee moves with it, unchanged: `publish`
returns produced/branch only after the push is confirmed, and a failure keeps
the working directory.

## Consequences

- **The devbox image no longer needs `git`.** Removing it turns a policy into
  a fact: an agent that cannot find the binary cannot be talked into using it
  by a prompt injection.
- **`core.sharedRepository=group` goes away.** It exists only because several
  uids write to one object store; with one writer, the store returns to
  `0700` and the ADR-0009 hardening for it is no longer load-bearing.
- **`gitd` is a new high-value component**, in the same tier as the broker: it
  can read every branch in the project. It holds no forge credential — the
  broker still owns that — so compromising it yields the code, not the
  ability to publish.
- **A new failure mode**: `gitd` down means no session can start or settle.
  It must be as boring as the anchor, and sessions already tolerate a slow
  start (they wait for the clone today).
- **Latency**: `materialize` is a checkout, which is what worktree creation
  already costs. `status`/`diff` become RPCs rather than local commands —
  negligible against an agent's turn.

## Alternatives rejected

**Keep git, drop write permission on the object store.** Attractive because
it changes almost nothing: the agent runs git read-only. But `git worktree`
needs to write to `.git/worktrees/<id>` (index, HEAD), so either the agent
can write into the store or the worktree cannot exist. Half-measures here
produce a repo that is writable in exactly the places that matter.

**A git-shaped proxy** (`gitd` exposes "run this git command"). This is the
same mistake as a forge proxy that takes arbitrary REST paths: the allow-list
is the security property, and a command-shaped API has no allow-list.

**Copy the repo per user.** Isolates reads at the cost of one object store
per user per Cell — for a big repository, multiplied by everyone working on
it. The shared store with a single writer gets the same isolation without the
copies.

## Migration

1. `gitd` alongside the current path, unused (`materialize`/`status`/`diff`).
2. Sessions materialize through it; worktrees stop being git worktrees.
   Agents still have `git`, so anything that regresses is visible before the
   binary disappears.
3. `publish`; the settle Job stops calling git.
4. `git` leaves the reference devbox image; the object store returns to
   `0700` and `core.sharedRepository` is dropped.

Each step is verifiable on a cluster, and the order means the capability
regression (step 4) lands only after the replacement is proven.
