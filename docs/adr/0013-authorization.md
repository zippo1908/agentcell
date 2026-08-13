# ADR-0013: Roles on the Cell, not a policy engine (yet)

Status: accepted

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

The creator is the first maintainer. A Cell with no members listed is open to
every authenticated user — which is exactly today's behaviour, so existing
deployments do not change until someone adds a member.

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
