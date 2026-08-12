# Contributing to AgentCell

Thanks for your interest. AgentCell is a workshop for AI coding agents — one
resident Kubernetes-backed instance per project, disposable session slots,
and an SDLC loop closed in-instance. This guide gets you productive fast.

## Ground rules

- Be respectful; see the Code of Conduct (Contributor Covenant).
- Discuss non-trivial changes first — open an issue or a Discussion before a
  large PR, so we agree on the approach.
- **Security issues are never reported in public.** See [SECURITY.md](SECURITY.md).

## Architecture in one screen

Read these before a substantial change; they explain *why* the code is shaped
the way it is, and PRs are reviewed against them:

- [docs/PLAN.md](docs/PLAN.md) — milestones and the big picture
- [docs/adr/](docs/adr/) — the accepted design decisions (K8s foundation,
  provider access layer, two-zone release, git-broker, …)
- [docs/DEPLOYMENT.md](docs/DEPLOYMENT.md) — how it runs in a cluster

Design rule worth internalizing: **the platform layer is composed from system
primitives** (K8s API, git, tmux, cgroups) — we orchestrate, we don't re-build
what Kubernetes already provides. New dependencies need a reason in the PR.

## Development

Requirements: Go 1.26+, `make`, and (for images) `podman` or docker.

```sh
make build        # bin/{celld,git-broker,cell-runtime,cellctl}
make test         # unit tests
make lint         # gofmt + go vet
```

- `internal/access` and the ref-policy / id packages import **no Kubernetes
  types** on purpose — keep core logic unit-testable without a cluster.
- Every behavioral change needs a test. Controller changes use the
  fake-client pattern in `internal/controller/*_test.go`; git logic uses real
  `git` (`cmd/cell-runtime/settle_test.go`); the broker's policy/JWT logic is
  pure and unit-tested (`cmd/git-broker/broker_test.go`).

### Real-cluster e2e

Some things (TokenReview audiences, readiness, NetworkPolicy) only show up on
a real cluster. See [docs/E2E.md](docs/E2E.md) to run `scripts/e2e-local.sh`
against single-node k3s. If your change touches pod specs, RBAC, network
policy, or the git-broker, run it and attach the result to your PR.

## Pull requests

1. Branch from `main`. Keep PRs focused — one concern each.
2. `make lint test` must pass; CI runs fmt + vet + test + build.
3. Write a clear description: what changed, why, and how you verified it
   (unit tests, and e2e output if relevant).
4. Sign off your commits (`git commit -s`) to certify the
   [Developer Certificate of Origin](https://developercertificate.org/).
5. Update the relevant docs/ADR in the same PR — the README
   "implemented vs designed" table is the source of truth for status; don't
   let it drift.

Honesty over polish: if a claim isn't verified, say so. The project's
credibility rests on the docs never over-promising what the code delivers.

## Good first issues

Look for the [`good first issue`](https://github.com/zippo1908/agentcell/labels/good%20first%20issue)
label. If nothing fits, open a Discussion and we'll help you find a starting
point.

## License

By contributing you agree your contributions are licensed under Apache-2.0
(see [LICENSE](LICENSE)).
