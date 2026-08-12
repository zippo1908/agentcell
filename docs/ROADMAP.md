# Roadmap

Living view of where AgentCell is and where it's going. The
[README implemented-vs-designed table](../README.md) is the authoritative
per-feature status; this page groups it into themes and sequence.

## Done (through v0.1.0-alpha.2 + main)

- **Core model** — resident Cell (namespace + anchor StatefulSet + workspace
  PVC), disposable Session slots, mandatory settle (push-confirmed-or-retry,
  real-git tested).
- **Calibration loop** — resident product preview + web UI (edit description,
  dispatch, watch the agent's work live), two-zone release (isolated `/app`
  production, branch/tag/SHA, rollback).
- **Provider access** — runner × provider registry; Alibaba Bailian, Tencent
  Hunyuan, DeepSeek, Moonshot, Zhipu first-class ([ADR-0002](adr/0002-provider-access-layer.md)).
- **Security** — API auth (bearer + login cookie, refuses to start open),
  per-Cell NetworkPolicy, PSS restricted, non-root pods, race-free slot
  leases, **git-broker**: forge token in no workload pod, role/session/
  audience-scoped ([ADR-0005](adr/0005-git-broker.md)).
- **Verified** — full 8-step e2e on real k3s (preview + production HTTP 200).

## Next

### Near term
- **Review queue → PR** (M7/M9) — approve/reject settled `session/*`
  branches in the UI, auto-open PRs, track merge state. *This closes the last
  gap between "settle pushes a branch" and a true SDLC loop.*
- **Terminal attach** (M5) — tmux over WebSocket to watch/take over a session.
- **Broker on real k3s** — validate audience-bound tokens + bound-token
  identity end to end, then cut v0.1.0-alpha.3.

### Medium term
- **agent-sandbox substrate** ([ADR-0004](adr/0004-agent-sandbox-adoption.md)
  Phase 1) — run the Cell anchor on the K8s SIG `Sandbox` CRD behind a driver
  seam; evaluate WarmPool for near-zero dispatch latency (Phase 2).
- **Helm chart + cloud presets** (M10) — `deploy/presets/` for k3s / Alibaba
  ACK / Tencent TKE; goreleaser + published images.
- **Multi-node** — RWX StorageClass support so a Cell survives node loss.
- **Knowledge** — indexed retrieval over `/workspace/knowledge`, review
  feedback distilled back in.

### Exploratory
- Short-lived GitHub App tokens end-to-end (ADR-0005 v3 mechanism exists;
  wire the default deployment path).
- Broker action-level policy beyond `session/*` (per-repo rules).
- Metrics/observability dashboards; completeness/quality signals per session.

## How to influence this

Open a [Discussion](https://github.com/zippo1908/agentcell/discussions) under
**Ideas** for direction, or an issue for a concrete unit of work. Items marked
[`good first issue`](https://github.com/zippo1908/agentcell/labels/good%20first%20issue)
are scoped for newcomers.

## Next: the git boundary (ADR-0011)

ADR-0005 took the forge credential away from every workload. Local git
authority was never taken away: an agent can still run git against the shared
object store, which since per-user identity means it can read another user's
unpublished commits.

[ADR-0011](adr/0011-git-boundary.md) proposes the fix — a `gitd` per Cell
owning the repository at `0700`, worktrees materialized as plain directories
with no `.git`, and a four-operation allow-list — with a migration ordered so
the capability regression lands only after the replacement is proven.
