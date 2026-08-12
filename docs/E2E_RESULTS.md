# Local k3s E2E Results

Real single-node k3s runs of `scripts/e2e-local.sh` (token-free fake agent,
real git push path). Each run is recorded honestly, including checks that
only appeared to pass.

## Run 1 — lenient script

| Step | Check | Result |
| --- | --- | --- |
| 1–5 | build / install / auth / create / ready | ✅ |
| 6 | Preview served | ⚠️ HTTP 502, passed only because the script accepted any non-000 |
| 7 | Dispatch → settle → remote branch | ✅ `session/<id>` pushed |
| 8 | Release → production served | ⚠️ HTTP 502, same lenient check |

Genuinely verified: auth, reconcile, and the full dispatch → settle →
pushed-branch path. The serving checks were not real (any response counted).

## Run 2 — strict script

Steps 6 and 8 now retry and require a real 2xx/3xx/4xx.

| Step | Check | Result |
| --- | --- | --- |
| 1–5 | build / install / auth / create / ready | ✅ |
| 6 | Preview served | ✅ **HTTP 200** |
| 7 | Dispatch → settle → remote branch | ⚠️ branch verified, but the harness matched `items[0]` (a stale session), not the one just dispatched |
| 8 | Release → production served | ❌ **HTTP 502** |

Two root causes found:

1. **Preview (fixed in this run):** Alpine's base BusyBox has no `httpd`
   applet, so the anchor looped on `httpd: applet not found` and never
   served. The e2e image now installs `busybox-extras` and the Cell uses
   `httpd -f -p 3000 -h .`. Combined with the readiness probe (Ready now
   means "listening"), preview became HTTP 200.
2. **Production (GitHub #2):** the prod init clone failed with
   `Could not resolve host: github.com` — a transient DNS/egress condition
   at pod startup; the same host resolved moments later from the anchor. Not
   a platform-logic bug; the strict check correctly refused to pass it.

Plus a harness bug: step 7 verified `items[0]` rather than the session it
just dispatched.

### Fixes for Run 3 (committed)

- Network git ops (anchor + prod clone/fetch) retry with backoff
  (1/4/9/16s) via `gitNet`, so a pod that starts before DNS/egress is ready
  self-heals instead of failing the release. Real clusters see the same
  startup blips, not only WSL.
- `scripts/e2e-local.sh` step 7 parses the dispatched session name from
  `cellctl` output and verifies exactly that session/branch.

## Re-run prompt (for a local Codex CLI)

> Pull the latest `main` of https://github.com/zippo1908/agentcell, rebuild
> both images and re-import them into k3s (pod specs and the e2e image
> changed), then re-run `scripts/e2e-local.sh` with the same E2E_* env. All
> 8 steps must pass; steps 6 and 8 require a real 2xx/3xx/4xx (a persistent
> 5xx/000 fails the run).
>
> If a step fails, the resources live in namespace `cell-e2e` (not
> `agentcell-system`): `kubectl -n cell-e2e get pods,endpoints`,
> `kubectl -n cell-e2e logs <pod> -c <anchor|prod|clone>`,
> `kubectl -n cell-e2e get events`. Fix the root cause, re-run until it
> prints `E2E PASSED`, and append a "Run 3" section here with the per-step
> table and the exact HTTP codes for steps 6 and 8.

## Run 3 - strict rerun after finalizer fix

| Step | Check | Result |
| --- | --- | --- |
| 1 | Build binaries and both images; import into k3s | PASS |
| 2 | Install CRDs, control plane, and E2E secrets | PASS |
| 3 | Auth rejects no token and accepts the configured token | PASS |
| 4 | Delete the prior Cell, then create `e2e` | PASS |
| 5 | Cell becomes Ready | PASS |
| 6 | Preview through the authenticated proxy | PASS, HTTP 200 |
| 7 | Dispatch the named Session, settle, and verify its remote branch | PASS, `session/01kzsjysdmwhqc3ks1gncbqcbn` |
| 8 | Release and reach production through `/app/e2e/` | PASS, HTTP 200 |

The strict script printed `E2E PASSED` on the final run. The first Run 3
attempt exposed a cleanup race: the Cell controller removed its finalizer as
soon as it requested Namespace deletion, allowing a same-name Cell to
reconcile against `cell-e2e` while that namespace was still Terminating.
Kubernetes then rejected copied credentials and per-session secrets. The
controller now keeps the Cell finalizer and requeues until the workload
namespace is actually gone; `TestCellFinalizeWaitsForWorkloadNamespaceDeletion`
covers that ordering.
## Run 4 - broker mode against a real cluster and a self-hosted GitLab

Runs 1-3 exercised a k3s node that could reach GitHub. Run 4 is the case the
project actually targets: an air-gapped-ish private cluster whose only forge is
an internal GitLab, driven end to end through the git-broker. It is the first
run where no workload pod ever held a forge credential and where the review
queue opened a real merge request.

Environment: k3s v1.36.3+k3s1 on an internal host, reached through a reverse
SSH tunnel; forge `http://git.tinci.com:6006/zhumingze/agentcell_e2etest.git`;
control plane `celld`/`git-broker` at `run4` (commit `598f590`); runtime image
pulled from a **private** registry.

| Step | Check | Result |
| --- | --- | --- |
| 1 | Cell reconciles; workspace PVC bound | PASS |
| 2 | No `GIT_TOKEN` in anchor/prod pod specs | PASS |
| 2 | Forge Secret absent from the Cell namespace | PASS |
| 2 | Per-role ServiceAccounts (anchor/settle/prod) exist | PASS |
| 3 | Anchor clones through the broker and serves | PASS |
| 4 | Console issues a ticketed preview URL | PASS |
| 4 | Preview serves through the ticket | PASS, HTTP 200 |
| 4 | Console credential refused on the preview origin | PASS, HTTP 401 |
| 4 | Ticket is single-use | PASS, first 303, replay 403 |
| 5 | Dispatch -> settle -> branch pushed to GitLab | PASS, `session/01kzten9y4zwa68t6zgpfv6kdk` |
| 6 | Diff served through the broker | PASS, HTTP 200 |
| 6 | Approve opens a merge request | PASS, `merge_requests/3` |
| 7 | Release advertises a production URL | PASS |
| 7 | Production zone serves | PASS, HTTP 200 |

`passed=16 failed=0`.

Three things this run surfaced, all fixed before the green result:

**GitLab was not a supported forge.** The broker only spoke the GitHub REST
API, so `compare` / `pull-find` / `pull-create` / `pull-get` all failed against
GitLab. `cmd/git-broker/forge_gitlab.go` adds the adapter behind the same
four-operation allow-list: project addressed as URL-encoded `group/name`,
`PRIVATE-TOKEN` auth, merge requests keyed by `iid`, and diff line counts
derived locally because GitLab does not summarise them.

**Private registries were unusable.** kubelet resolves image pull secrets
namespace-locally, so images in a private registry could never be pulled into
the dynamically created Cell namespaces - every Cell would have stalled in
`ImagePullBackOff`. `image.pullSecret` now names a docker-registry Secret in
the control namespace; the operator mirrors it into each Cell namespace and
attaches it to the accounts its pods run as. This is not an edge case: a
private registry is the norm on the private clouds this project targets.

**Two apparent failures were the harness, not the product**, and both became
assertions instead. The preview retry loop replayed one single-use ticket, so
attempts after the first were correctly refused with 403 - the run now asserts
that replay is refused. And the first hit answers 303, not 200, because the
ticket is exchanged for a session cookie and the URL rewritten without it; the
check accepts 3xx there rather than treating the design as a failure.

## Run 5 - two users in one Cell (ADR-0009)

Runs 1-4 all had a single principal, so the entire runtime ran as one Unix
user and nothing about isolation was exercised. Run 5 puts two owners in one
Cell and checks the property that matters: neither can read the other's
work, both can still use the project.

Environment: the same internal k3s and self-hosted GitLab as Run 4; celld at
`iso2`, runtime image at `iso5`.

| Check | Result |
| --- | --- |
| Two owned Sessions both settle, concurrently | PASS |
| Allocator records distinct uids | PASS, 100000 / 100001 |
| uids come from the reserved user range | PASS |
| The pods really ran as those uids | PASS, settle pods 100000 / 100001 |
| fsGroup is still the project group | PASS, 1000 |
| Private tree is 0700 and owned by its user | PASS |
| The other user's pod is refused by the kernel | PASS, `Permission denied` |
| Both users still read the project checkout | PASS |

`passed=8 failed=0`.

Three real defects surfaced here that no unit test reached, all of them
consequences of per-user uids:

**One user's private tree locked out every other user.** `MkdirAll` creates
the parent with the child's mode, so `/workspace/users` became 0700 owned by
whichever user started first, and the second user's session could not start
at all. The anchor — which holds the project identity — now creates that
directory group-writable and **sticky**: `/workspace` is world-writable, so
without the sticky bit any user could delete a peer's private directory.
Being unable to read it is not the whole property worth having.

**git refused the shared checkout.** Sessions run as their owner while the
checkout belongs to the project identity, so `git worktree add` failed with
"detected dubious ownership". Trust is now granted for that exact path, never
the `*` wildcard.

**The shared object store was unwritable by anyone but its creator.** git
creates object directories 0755, so the first uid to create a prefix
directory owned it and everyone else hit "insufficient permission for adding
an object to repository database". It fails by luck of the object hash, so it
presents as an intermittent flake — in the first run of this suite alice
settled and bob did not, with identical code. `core.sharedRepository=group`
is the mechanism for exactly this, applied at clone and repaired on every
anchor start so Cells created before per-user uids are fixed too.

A fourth finding was in the harness, not the product: the fixture reused
fixed session ids, which collided with the branch its own previous run had
pushed. Real session ids are ULIDs.

Settle also stopped swallowing failure causes. "autosave commit failed" with
no reason attached is what made the object-store bug look intermittent; the
verdict now carries the error onto the Session status.

## Run 6 - resident sessions (ADR-0010)

A one-shot session is verified by Runs 1-5. Run 6 checks the other shape: a
slot that outlives its agent, so the owner can look at the result and keep
going in the same context.

Environment: the same internal k3s and self-hosted GitLab; celld `res4`,
runtime image `res3`.

| Check | Result |
| --- | --- |
| Resident session accepted and started | PASS, HTTP 201 |
| Slot stays alive after the agent finishes | PASS, phase still `Running` |
| State reports the agent finished, with its exit status | PASS, `working:false exitCode:"0"` |
| Attach command is printed and self-contained | PASS |
| A follow-up instruction reaches the live session | PASS, HTTP 200 |
| It lands in the SAME worktree | PASS, `AGENT_RAN.md` + `FOLLOWUP.md` |
| Explicit settle still publishes | PASS, branch pushed to GitLab |

That last row is the one that matters most. The point of a resident slot is
that the user decides when it ends — and mandatory settle has to survive
that, or the model has traded a real guarantee for convenience. It does not:
ending the session ran settle, which committed the follow-up work and pushed
the branch.

Two defects surfaced, both in the seam between the pod and the console:

**The completion marker was unreadable from outside.** It was written
relative to the worktree, but an exec starts in the image's working directory
and inherits neither the worktree path, the uid, nor the session id. The
marker moved to an absolute path in the pod's own filesystem. Before the fix
a finished agent reported `working:true` forever.

**`cell-runtime` is not on `$PATH`.** The console execs it by name; images
bake it at `/agentcell/cell-runtime`. Now referenced through the constant
that already existed for exactly this.

A third finding was about failing clearly rather than correctly: the e2e image
had no tmux, so a resident session simply failed and the Session reported
"agent finished (Failed)" — which points at the agent rather than at the
image. The runtime now checks for tmux up front and says so in a sentence an
operator can act on.
