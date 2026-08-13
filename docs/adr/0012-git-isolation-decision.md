# ADR-0012: Isolate the object store instead of mediating git

Status: accepted
Supersedes the necessity claim in: [ADR-0011](0011-git-boundary.md)

## The question I was asked to decide

Is `aip-git` — every git operation an agent performs going through a
scheduler-side service, with no `git` binary in the agent image — necessary?

**My answer: not for the security property it was proposed to buy. It is
necessary for auditability, and only if auditability is a requirement.**

Here is the reasoning, and what would change my mind.

## What the leak actually is

Sessions are git *worktrees* of one shared repository. A worktree shares the
object store, so every commit an agent makes lands in
`/workspace/repo/.git/objects` — which ADR-0009 had to make group-readable so
that several uids could write there at all.

So the concrete exposure is exactly this: **one user's agent can read another
user's unpublished commits.** Not "an agent can run git" — running git
against your own work is the job.

That distinction matters, because the two problems have very different
prices.

## Why mediation is the expensive answer

ADR-0011 proposed a four-verb allow-list. That number is the problem.

A coding agent uses git constantly and unpredictably: `log`, `blame`,
`show`, `diff` between two arbitrary refs, `stash`, `bisect`, checking out a
sibling branch to compare an implementation. Every one of those is either
proxied — growing the allow-list until it approximates git — or lost, which
degrades exactly the agents this platform exists to host.

An allow-list that grows to approximate git has become a worse git with a
custom protocol, a new daemon per Cell, and a new single point of failure.
That is a large, permanent tax to pay for a property that can be bought
another way.

## The cheaper answer: give each user their own repository

Each user gets their own repository whose object store is private, with the
project's published history shared read-only underneath it:

```
/workspace/repo                     shared mirror, owned by the project, read-only to users
/workspace/users/<uid>/repo         this user's repository (0700)
    objects/info/alternates      →  /workspace/repo/.git/objects
```

`git clone --shared` (or an explicit `alternates` entry) makes reads resolve
through to the shared base while **writes land in the user's own object
store**. Sessions become worktrees of the *user's* repository.

The result:

- a user's unpublished commits are in a `0700` directory owned by them — the
  kernel refuses a peer's agent, exactly as it does for their worktrees and
  `$HOME` today (ADR-0009);
- the shared base stays readable by everyone, which is correct: that is the
  project's published code, and every member is entitled to it;
- the shared store becomes **read-only**, so `core.sharedRepository=group`
  and the group-writable object directories from ADR-0009 stop being needed —
  a hardening that goes away rather than accumulating;
- agents keep native git, all of it.

The cost is one extra repository directory per user per Cell: refs, an index,
and only the objects that user actually created. Not one copy of the
repository's history.

## What mediation still buys, and when to pay for it

**An audit trail.** A mediated git is the only design where "who ran what
against this repository, and when" is answerable. Per-user object stores give
isolation, not a record.

So the decision is conditional, and the condition is a real one:

- **Isolation is the requirement** → per-user object stores. Simpler, no new
  component, no capability regression.
- **A record of every git operation is a requirement** — a regulated
  environment, a customer who must be able to reconstruct what an agent did —
  → mediation, and pay the tax knowingly.

ADR-0011 stays as the design for the second case, with one correction it
already carries: if it is ever built, it must be the aip-git execution
backend rather than a second protocol.

## Why I am not treating "the agent could run git" as the threat

Because the capabilities that would make that dangerous are already gone:

- **push** needs a forge credential, which no workload pod holds (ADR-0005);
- **reaching the forge at all** goes through the broker, which refuses
  anything outside this session's branch;
- **reading other users' work** is what this ADR removes.

What remains is an agent doing arbitrary git inside its own repository —
which is the tool doing its job. Removing `git` from the image would stop a
prompt injection from running `git checkout`, but that injection already runs
arbitrary code in a container with a writable worktree. `git` is not the
capability worth taking away there; if that is the threat model, the answer
is a sandboxed runtime, not a git proxy.

## Consequences

- ADR-0009's `core.sharedRepository=group` and the group-writable `.git`
  repair become unnecessary once this lands. They are removed, not left as
  dead hardening that suggests a boundary it no longer implements.
- The anchor keeps the shared mirror and remains the only writer to it.
- `git worktree add` moves from the shared repository to the user's, and
  settle pushes from there — both through the broker, unchanged.
- Disk: one extra repository per user per Cell, objects only.

## What would change my mind

- A requirement to *audit* git operations, as above.
- Evidence that per-user repositories cost meaningfully more disk than
  expected on a large monorepo — measurable, and worth measuring before
  building either design.
- A threat model that treats the agent itself as hostile rather than the
  code it runs. That is a different product, and it needs a sandboxed runtime
  (gVisor, Kata) rather than a git boundary.
