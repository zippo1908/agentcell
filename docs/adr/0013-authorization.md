# ADR-0013: Roles on the Cell, not a policy engine (yet)

Status: accepted; superseded in part (2026-08, see "What changed since")

## The question

Should AgentCell adopt Casbin for authorization, and lean on Apache APISIX®
for more of it?

## What is actually broken

Auditing the write paths turns up something more basic than a missing policy
engine: **there are barely any authorization rules to enforce.**

| Operation | Guarded by |
| --- | --- |
| dispatch | credential ownership |
| continue / discard a session | session ownership |
| create a Cell | git-credential ownership |
| **release to production** | **nothing** |
| **edit a Cell's description** | **nothing** |
| **approve or reject a review** | **nothing** |

Any authenticated user can ship any Cell to production. That is not a
policy-expressiveness problem. Reaching for Casbin here would produce a
policy engine whose entire policy is three `if` statements nobody wrote yet.

## Decision

**Add roles to the Cell. Do not adopt Casbin yet. Leave APISIX where it is.**

### Roles

A Cell carries members with one of three roles:

| Role | Can |
| --- | --- |
| `viewer` | see the Cell, its settled sessions and reviews |
| `member` | everything a viewer can, plus dispatch, run sessions, review |
| `maintainer` | everything a member can, plus release, edit settings, manage members |

The creator is the first maintainer. `spec.access` says whether the member
list is enforced: unset with no members means `open` — exactly the pre-roles
behaviour, so nothing breaks on upgrade — and adding the first member closes
the Cell, because naming somebody is an unambiguous statement that this
project has an inside and an outside.

An empty array is a poor way to express a dangerous state, so the controller
records the conclusion in `status.access`. "Who can touch this project" is
then answerable from `kubectl get cell` rather than inferred from a missing
field, and the console says plainly when the answer is "anyone who can log
in".

Membership is managed through the API (`PUT`/`DELETE
/api/cells/{cell}/members`) and the console, not only by editing the CR —
"the maintainer manages members" was documented before it was possible, and
a maintainer of a project does not necessarily have cluster access. Removing
the last maintainer of a restricted Cell is refused, because the recovery
would need exactly the access this avoids requiring.

**Release is the line that matters.** Everything else is recoverable; a
release is the one action that puts code in front of users, and it now needs
`maintainer`.

### Why not Casbin

Not "never" — "not for this".

A policy engine earns its keep when policies are **complex** (hierarchies,
attribute conditions, per-resource ACLs) and **change without a redeploy**.
AgentCell has three roles and about eight verbs. Expressed in Go that is a
table and a lookup: readable in one screen, exhaustively testable, and it
fails closed by construction.

Expressed in Casbin it is a model file, a policy store (file? database? a
CRD?), a matcher expression, and an evaluation path where a subtly wrong
matcher silently grants rather than denies. For three roles, the engine is
the larger risk.

What would change this:

- **policies that operators must edit at runtime**, without shipping a
  release;
- **organisation-level structure** — teams owning many Cells, inherited
  roles, deny rules;
- **per-resource ACLs** users manage themselves.

Any of those, and a hand-rolled checker becomes the wrong answer fast. So the
check is written behind one function — `can(principal, cell, action)` — which
is the seam a policy engine would slot into. No handler learns how the
decision is made.

### Why APISIX stays where it is

It is already integrated as what it is good at: TLS termination, routing,
rate limiting, one edge for several services. That path ships in
`deploy/identity/`.

What it must NOT become is the authorization point. celld is reachable on its
ClusterIP from anywhere in the pod network, so a decision made at the gateway
and passed as a header is a decision anything on that network can forge. The
gateway can enforce as well — defence in depth is welcome — but it cannot be
the only enforcement, which means celld needs these rules regardless. Adding
APISIX policy would not remove a single check from this codebase.

### Reads are governed too

Closing the write paths first made it easy to believe the Cell was closed. It
was not: an outsider could list every project, read a settled session's diff,
and — worst — be handed a **preview ticket**, which is a capability rather
than a label. Filtering happens before the view is built, not after.

## Teams arrived. Casbin still has not.

This ADR listed three things that would overturn "no policy engine". The
first was **organisation-level structure — teams owning many Cells,
inherited roles**. That has now been built, so the judgement has to be made
again rather than inherited.

The answer is still no, and the reason is that the rule which arrived is one
line:

```go
// an entry on the Cell wins; otherwise the team's role applies
for _, m := range cell.Spec.Members { if m.UserID == id { return m.Role } }
if team != nil { return team.RoleOf(id) }
```

Not "inheritance" in the sense that motivates a policy engine — no
hierarchy, no transitive groups, no deny rules, no attribute conditions. One
default and one override, resolved without a graph walk.

The override direction is the part worth stating: a Cell entry wins **in both
directions**, so it can lower a team role as well as raise it. Taking the
higher of the two would look generous and would make "a viewer on this one
project" unsayable — which is precisely the exception a team exists to have.

`roleOf(principal, cell, team)` stays a pure function. That is deliberate:
the team is passed in rather than fetched inside, so the entire rule set is
still readable on one screen and testable without a cluster. When an engine
is finally warranted, this is still the one seam it goes into.

What would change it now: deny rules, teams containing teams, or policies an
operator edits at runtime without a release. None of those are here.

## Consequences

- Deployments that never add members behave exactly as before. This is
  deliberate: a permission model that locks people out of their own Cells on
  upgrade would be reverted before it was understood.
- Membership lives on the Cell CR, so it is visible in `kubectl get cell -o
  yaml` and reviewable in git if Cells are managed declaratively.
- With static tokens there is one principal, so it is a maintainer
  everywhere — the single-user story is unchanged.
- `can()` is the only place a decision is made. If it ever needs an engine,
  that is where the engine goes.


## What changed since

Two parts of this decision no longer describe the system, and pretending
otherwise would make the record worse than useless — somebody would follow
it and be wrong.

**Teams are gone.** This ADR gave a Cell an optional team whose member list
supplied defaults, with a name on the Cell overriding it in both directions.
That was a second membership model for a product where every capability —
a terminal, a preview, a release — belongs to a project. Two answers to one
question drift: somebody in the team but off the project could read the
whole conversation about work they could not open. The project's own member
list is now the only scope, and the board's conversation belongs to the
project too.

The removal had to be safe for deployments already running. A Cell written
before it carries `spec.team` and usually no members of its own — the team
WAS its list — so the access rule was changed to read an empty member list
as OPEN. Left as it was, such a project would have reported itself
restricted while naming nobody: closed to everyone, administrators included.
A rule that turns an upgrade into a lockout is worse than one that is
briefly too generous.

**Sharing a session is not a flaw to prevent.** The original text treated a
personal runtime as something that could never be shared. It can, and the
board's conversation is exactly that case. What may NOT be shared silently
is the bill: a session records the real person who opened it, that person's
credential funds every turn, and an operator typing into it does not become
the sponsor. Per-user uids and 0700 directories remain the default isolation
topology — that is an implementation strategy, not a reason a project's
conversation cannot have several people in it.
