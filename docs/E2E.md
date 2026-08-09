# Running the end-to-end test locally

`scripts/e2e-local.sh` stands up AgentCell on a real cluster and verifies
the whole chain with a **token-free fake agent** (no model credentials
spent): auth is enforced → a Cell reconciles → the preview is reachable
through the authenticated proxy → a dispatched session settles and pushes
`session/<id>` to the real remote → a release brings up `/app`.

## Prerequisites

- A reachable Kubernetes cluster + `kubectl` context. Single-node **k3s**:
  ```sh
  curl -sfL https://get.k3s.io | sh -          # or: ...| INSTALL_K3S_MIRROR=cn sh -
  export KUBECONFIG=/etc/rancher/k3s/k3s.yaml
  ```
- `podman` (or `docker` via `BUILDER=docker`), Go 1.26+.
- A git repo the test can push to, **with a `main` branch already present**
  (an empty commit is enough). A throwaway private GitHub repo is ideal.

## Run

```sh
export E2E_REPO_URL=https://github.com/you/agentcell-e2e.git
export E2E_GIT_USER=you
export E2E_GIT_TOKEN=ghp_xxx        # push access
export E2E_IMPORT=1                  # import built images into k3s containerd
./scripts/e2e-local.sh
```

Green means the platform chain works. It is re-runnable (it deletes its
`e2e` Cell at the start).

## Two roles of "codex" here — don't conflate them

- **Fake agent (default):** the e2e devbox ships a stub `codex` that makes a
  deterministic edit. This validates the *platform*, not a model. Keep it
  for the first green run.
- **Real Codex runner:** to also exercise the runner×provider layer, build
  the real `images/devbox` image (it installs `@openai/codex`), point the
  Cell at it, and give the model-credential secret a real key:
  ```sh
  kubectl -n agentcell-system create secret generic openai --from-literal=key=sk-...
  cellctl dispatch e2e --task "…" --runner codex --provider openai --cred openai
  ```
  For a China-cloud provider instead, use `--provider deepseek` / `aliyun-bailian`
  with that provider's key — see docs/adr/0002.

## Driving it with a local Codex CLI

If you want your local Codex CLI to run and debug the e2e on your machine,
give it this task (it has shell access):

> Clone https://github.com/zippo1908/agentcell, read docs/E2E.md, then run
> `scripts/e2e-local.sh` against the local k3s cluster with the E2E_* env
> vars I provide. If a step fails, read the failing resource
> (`kubectl -n agentcell-system describe cell e2e`, pod logs of the anchor
> and session pods, `kubectl get events`), fix the root cause in the repo or
> the manifests, and re-run until it prints "E2E PASSED". Report each fix
> you made and the final status of every one of the 8 steps.

The script logs 8 numbered steps and fails loudly with the offending
resource, so an agent can localize breakage without extra instrumentation.
