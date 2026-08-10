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
