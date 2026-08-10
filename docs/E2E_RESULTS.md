# Local k3s E2E Results

## Run 1 — 2026-08-10 (single-node k3s, podman, E2E_IMPORT=1)

The control-plane chain passed on a real cluster; the app-serving checks
did not genuinely pass and were caught as a follow-up.

| Step | Check | Result |
| --- | --- | --- |
| 1 | Build binaries and images | ✅ |
| 2 | Install CRDs, control plane, secrets | ✅ celld rollout completed |
| 3 | Auth enforced | ✅ 401 unauthenticated, 200 authenticated |
| 4 | Create Cell | ✅ `cell/e2e` created |
| 5 | Cell reaches Ready | ✅ |
| 6 | Preview served through proxy | ⚠️ **HTTP 502** — passed only because the script accepted any non-000 |
| 7 | Dispatch → settle → remote branch | ✅ `session/<id>` pushed to the remote |
| 8 | Release → production served | ⚠️ **HTTP 502** — same lenient check |

**Genuinely verified on real k3s:** authentication, Cell reconciliation,
and the full dispatch → settle → pushed `session/<id>` branch path
(the data-safety core). **Not verified:** preview / production actually
serving HTTP.

### Root cause of the 502s

The anchor and prod containers had no readiness probe, so the pod (and the
Cell) reported Ready the instant the container process started — before the
in-container `git clone` finished and the dev server bound its port. The
proxy therefore hit a not-yet-listening upstream and returned 502.

### Fix (committed, awaiting re-run)

- Anchor (when a preview command is set) and the prod serving container now
  carry a TCP readiness probe on their port; Ready now means "listening".
- `scripts/e2e-local.sh` steps 6 and 8 now retry and require a real
  application response (2xx/3xx/4xx); a persistent 5xx/000 now **fails** the
  run instead of passing.

Re-run `scripts/e2e-local.sh` to confirm steps 6 and 8 reach a 2xx/3xx/4xx.

## Run 2 — 2026-08-10 (strict script, single-node k3s on WSL)

The readiness fix worked; a second, unrelated issue surfaced (GitHub #2).

| Step | Check | Result |
| --- | --- | --- |
| 1–5 | build / install / auth / create / ready | ✅ |
| 6 | Preview served through proxy | ✅ **HTTP 200** (was 502; readiness probe fixed it) |
| 7 | Dispatch → settle → remote branch | ⚠️ branch verified, but the harness matched a *stale* session (items[0]) not the one just dispatched |
| 8 | Release → production served | ❌ **HTTP 502** — prod init clone failed: `Could not resolve host: github.com` |

**Genuinely fixed since Run 1:** preview now serves (readiness probe).
**Step 8 root cause:** transient cluster DNS/egress at pod startup — the
prod clone ran before CoreDNS/egress was ready; the same host resolved fine
moments later from the anchor. Not a platform logic bug; the strict script
correctly refused to call it a pass.

### Fixes (committed, awaiting Run 3)

- Network git ops (anchor + prod clone/fetch) now retry with backoff
  (1/4/9/16s) via `gitNet`, so a pod that starts before DNS/egress is ready
  recovers instead of failing the release.
- `scripts/e2e-local.sh` step 7 now parses the dispatched session name from
  `cellctl` output and verifies *that* session, never `items[0]`.

### Re-run prompt (for a local Codex CLI)

> Pull the latest `main` of https://github.com/zippo1908/agentcell. Rebuild
> the images and re-import them (the readiness-probe fix changed the anchor
> and prod pod specs). Re-run `scripts/e2e-local.sh` with the same E2E_* env
> as before. It now retries and REQUIRES a real 2xx/3xx/4xx on steps 6
> (preview) and 8 (production) — a persistent 502/000 fails the run.
>
> If step 6 or 8 still fails: the upstream dev server isn't serving. Check
> the anchor pod — `kubectl -n agentcell-system` won't have it; it's in
> `cell-e2e`. Run `kubectl -n cell-e2e get pods`, then
> `kubectl -n cell-e2e logs <anchor-pod> -c anchor` and look for whether
> `busybox httpd` actually started and bound port 3000 (the e2e image is
> alpine; confirm the preview command `busybox httpd -f -p 3000 -h .` is
> valid there and the working dir has readable files). Also check
> `kubectl -n cell-e2e get endpoints preview` has an address. Fix the root
> cause (preview command, port, or probe), re-run until it prints
> `E2E PASSED`, and append a "Run 2" section here with the final per-step
> table and the exact HTTP codes for steps 6 and 8.

## Run 2 - 2026-08-10 (strict serving re-run; failed)

This run used `main` at `e47fa90`, rebuilt both images, and imported them
separately into local k3s. It exercised the stricter serving checks added in
that revision.

| Step | Check | Result | Evidence |
| --- | --- | --- | --- |
| 1 | Build binaries and images | Passed | `celld` and `devbox-e2e` rebuilt and imported. |
| 2 | Install CRDs, control plane, and secrets | Passed | `celld` rollout completed. |
| 3 | Auth enforced | Passed | 401 without token and 200 with token. |
| 4 | Create Cell | Passed | `cell/e2e` created. |
| 5 | Cell reaches Ready | Passed | Anchor readiness probe passed after the image fix. |
| 6 | Preview served through proxy | Passed | HTTP 200. |
| 7 | Dispatch, settle, remote branch | Passed by script | The script completed its branch check; see observation below. |
| 8 | Release, production served | Failed | HTTP 502 after retries. |

### Fix verified by Run 2

The original E2E image assumed Alpine base BusyBox included the `httpd`
applet. It does not. The image now installs `busybox-extras`, and the E2E
Cell command uses its standalone `httpd -f -p 3000 -h .`. This changed the
anchor from repeated `httpd: applet not found` exits to a ready Pod and
turned the preview check into HTTP 200.

### Remaining production blocker

The production init container never completed its release clone:

```text
fatal: unable to access '<E2E_REPO_URL>': Could not resolve host: github.com
cell-runtime prod-clone: clone release "main": exit status 128
```

The `prod` Service consequently had no endpoints, so the strict production
check correctly failed with HTTP 502. CoreDNS later resolved `github.com`
from the ready anchor, which indicates an intermittent cluster/WSL DNS
startup or egress condition rather than an `httpd` failure. This requires a
separate production-clone reliability fix.

### Additional observation

Step 7 reported a settled branch that did not match the newly dispatched
session ID. The script should select and verify the Session it just created,
rather than accepting a branch from an earlier E2E attempt.

### Validation performed

```text
bash -n scripts/e2e-local.sh
go test ./internal/controller
podman run alpine:3.20 + busybox-extras: httpd served a static file
strict local k3s E2E: steps 1-7 completed; step 8 failed with HTTP 502
```
