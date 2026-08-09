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
