# ADR-0008: User identity, and the Session as a user-private boundary

Status: accepted
Supersedes parts of: [ADR-0001](0001-architecture.md) (which modelled a
Session as belonging to a Cell and to nobody in particular)

## Context

Up to alpha.3 AgentCell has no concept of a user. The HTTP surface is gated
by static bearer tokens (`internal/webui/auth.go`), so **everyone holding a
token is the same anonymous principal**. `SessionSpec` carries no owner. All
workloads run as a fixed `uid=1000` on one shared per-Cell PVC.

That was a deliberate M0 cut — it removed the "anyone who reaches celld can
dispatch" hole with the least machinery — but it was never written down as
debt, and it does not survive contact with the actual use case: several
engineers working on one project, each driving their own agent sessions.

It has one concrete consequence today, not just in the future: `Session.spec.
credentialSecret` names a Secret in the control namespace and is injected
into the session pod. With no owner on a Session, **any token holder can
name someone else's model credential and have it injected into a session
they control**.

The principle this ADR encodes:

> A project is shared. A user's runtime, sessions, private context and
> private storage are not. Collaboration happens at the project layer,
> never at the process layer.

## Decision

### 1. Identity comes from OIDC, verified by celld itself

celld verifies the OIDC ID token directly — issuer, audience, signature
against the provider's JWKS, expiry. It does **not** trust an identity
header from a gateway.

This is the whole point. `X-Forwarded-User`-style trust means anything that
can reach celld's ClusterIP can assert any user. celld is reachable from
inside the cluster by construction, so header trust would make the identity
layer decorative. Verifying the token keeps the guarantee independent of
network position.

A consequence worth stating plainly: **APISIX is recommended, not required**,
and neither is Casdoor. Any standards-compliant OIDC provider works, which
matters for an open-source project that people deploy into environments we
do not control. celld implements the authorization-code + PKCE flow itself,
so the browser login works with no gateway at all.

Static tokens remain, demoted to break-glass and CI use.

### 2. The principal is uniform; single-user mode is not a special case

Every request resolves to a `Principal` with a stable `Subject`. An OIDC
user's subject is `oidc:<issuer-hash>:<sub>`; a static-token caller's is
`token:static`.

Authorization is then always the same rule — *you see what you own* — and
token-only deployments keep working unchanged, because in that mode there is
exactly one principal and it owns everything it created. No `if
multiUserEnabled` branches, which is where this class of bug lives.

### 3. `Session.spec.ownerUserID` is set at creation and is immutable

Immutability is enforced by the CRD itself (`x-kubernetes-validations`,
`self == oldSelf`), not by the API handler, so it holds for anything that
edits the CR — kubectl included.

### 4. Non-ownership is indistinguishable from non-existence

A user asking about a Session they do not own gets **404, never 403**. 403
confirms the Session exists; over a handful of probes that leaks the shape
of other people's work. This applies to every surface that can answer for a
Session — the console API, the preview origin, and the git-broker — not just
the API the UI happens to call.

### 5. A model credential can only be spent by its owner

`credentialSecret` must be a Secret the requesting principal owns
(`agentcell.io/owner` label). This closes the hole described above.

## What this ADR deliberately does not decide

Runtime isolation — per-user Unix UIDs, private PVCs, a resident per-user
runtime Pod and per-user tmux sockets — is the *next* layer and gets its own
ADR. This one stops at the control plane: who you are, what you own, and
what you are refused.

Two things are worth recording now, because they constrain that design:

- **A Unix UID is not the boundary; the Pod is.** Same node means a shared
  kernel. A UID separates files, not processes. The runtime boundary has to
  be a per-user Pod, with the UID as its filesystem expression.
- **UIDs must never be recycled.** A reused UID silently inherits the
  previous holder's files. Allocation needs a monotonic, tombstoned
  allocator, not "next free".

## Consequences

- celld gains an OIDC dependency (`github.com/coreos/go-oidc/v3`,
  Apache-2.0) and an outbound dependency on the IdP's JWKS endpoint.
  Discovery is lazy and retried, so a cold IdP delays logins rather than
  crash-looping celld.
- Existing single-token deployments are unaffected: no OIDC configured means
  one principal, and every list is already "everything it owns".
- Sessions created before this change have an empty `ownerUserID`. They are
  visible to the static-token principal only; there is no migration to guess
  an owner that was never recorded.
