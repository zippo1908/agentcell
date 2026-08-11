# Deploying AgentCell

This guide takes you from a bare Kubernetes cluster to a running AgentCell
control plane with a first project Cell serving a live preview. It reflects
what the code actually does as of `v0.1.0-alpha.2`.

> **Maturity.** Alpha. The full path — auth → Cell → preview → dispatch →
> settle → pushed branch → release → production — passes real-cluster e2e
> ([E2E_RESULTS.md](E2E_RESULTS.md)). What is **not** here yet: a review /
> PR approval queue (dispatch→settle→branch works; approval is roadmap),
> terminal attach, and a git-token broker (the anchor still holds the git
> token for clone/fetch). Evaluate accordingly before trusting it with
> sensitive repositories.

---

## 1. What you are deploying

Two tiers (see [ADR-0003](adr/0003-kubernetes-foundation.md)):

- **`celld`** — a controller-manager (reconciles the `Cell` and `Session`
  CRDs) plus the HTTP surface: authenticated API, calibration web UI, and a
  reverse proxy that exposes each Cell's preview at `/preview/<cell>/` and
  production at `/app/<cell>/`. Runs in the `agentcell-system` namespace.
- **Per-project Cells** — each `Cell` gets its own namespace `cell-<name>`
  containing an anchor pod (clone + resident preview), a workspace PVC,
  session pods (dispatched work), and, after a release, an isolated
  production deployment.

`cellctl` is the operator CLI; it talks to the Kubernetes API directly
using your kubeconfig (not through celld), so it needs RBAC to manage
`Cell`/`Session` resources.

---

## 2. Prerequisites

- **A Kubernetes cluster** (v1.27+). Single-node **k3s** is the supported
  quick path:
  ```sh
  curl -sfL https://get.k3s.io | sh -                       # global
  curl -sfL https://get.k3s.io | INSTALL_K3S_MIRROR=cn sh - # China mirror
  export KUBECONFIG=/etc/rancher/k3s/k3s.yaml
  ```
- **A CNI that enforces NetworkPolicy.** AgentCell locks every Cell
  namespace to default-deny. k3s enforces NetworkPolicy out of the box;
  on a custom cluster confirm your CNI does too (Calico, Cilium, …) — with
  a non-enforcing CNI the policies are silently inert (no error, just no
  isolation).
- **An RWO StorageClass** (k3s ships `local-path`). Multi-node caveat in §9.
- **`kubectl`** with cluster-admin (for install) and, for operators, RBAC to
  the `agentcell.io` group.
- To build images: **Go 1.26+**, **podman** (or docker), **make**.

---

## 3. Build and load the images

Until images are published to a registry, build them and make the cluster
see them.

```sh
git clone https://github.com/zippo1908/agentcell && cd agentcell
make build build-runtime-static
podman build -t ghcr.io/agentcell/celld  -f images/celld/Containerfile .
podman build -t ghcr.io/agentcell/devbox -f images/devbox/Containerfile .
```

**Single-node k3s** — import into its containerd (import each image
separately; a multi-image archive can collapse tags):

```sh
podman save ghcr.io/agentcell/celld  | sudo k3s ctr images import -
podman save ghcr.io/agentcell/devbox | sudo k3s ctr images import -
```

**Multi-node / real cluster** — push to a registry every node can pull, and
update the image refs in `config/install.yaml` (celld) and in each Cell's
`--image`. The manifests use `imagePullPolicy: IfNotPresent`, so imported
images are used without a registry round-trip.

> The **devbox** image is what anchor and session pods run. Its only
> contract: `/agentcell/cell-runtime` exists (baked in) and it runs
> **non-root as uid 1000** (Cell namespaces enforce Pod Security
> `restricted`). Build project-specific images freely as long as they honor
> that; add the agent CLIs your runners need (`claude`, `codex`, …).

---

## 4. Install the control plane

```sh
kubectl apply -f config/crd/ -f config/install.yaml
```

This creates the `agentcell-system` namespace, the CRDs, celld's
ServiceAccount + ClusterRole/Binding, the celld Deployment and Service.

**celld will not become ready yet** — it refuses to start unauthenticated
(see next step).

---

## 5. Configure secrets

### 5a. API access token (required)

celld reads bearer tokens from a mounted Secret and **refuses to start
without one** (unless you pass `--allow-no-auth`, dev only). Put one or more
whitespace-separated tokens under the key `tokens`; multiple values let you
rotate without downtime.

```sh
kubectl -n agentcell-system create secret generic celld-tokens \
  --from-literal=tokens="$(openssl rand -hex 24)"
kubectl -n agentcell-system rollout restart deploy/celld
kubectl -n agentcell-system rollout status  deploy/celld
```

### 5b. Git credentials (per project, `kubernetes.io/basic-auth`)

Cells that clone a private repo need a basic-auth secret in
`agentcell-system`. The keys **must** be `username` and `password` (celld
maps them to the git askpass helper).

```sh
kubectl -n agentcell-system create secret generic git-cred \
  --type=kubernetes.io/basic-auth \
  --from-literal=username=<git-user> \
  --from-literal=password=<PAT-with-push-access>
```

### 5c. Model credentials (per session, key `key`)

Each dispatch injects a model API key into that session only. Create one
secret per provider key you use, with the single key `key`:

```sh
kubectl -n agentcell-system create secret generic bailian-key \
  --from-literal=key=sk-...        # Alibaba Cloud Bailian, e.g.
```

---

## 6. (Optional) private or extra model providers

Built-in providers (Anthropic, OpenAI, Alibaba Bailian, Tencent Hunyuan,
DeepSeek, Moonshot, Zhipu, OpenRouter — see
[ADR-0002](adr/0002-provider-access-layer.md)) work with no config; you only
supply the API key secret in §5c. To add a provider or override an endpoint
(e.g. an internal proxy), mount a YAML overlay at
`/etc/agentcell/providers.d/` — it merges over the built-ins, user wins:

```sh
kubectl -n agentcell-system create configmap provider-overrides \
  --from-file=corp.yaml=./corp-providers.yaml
# then add a volume+mount for it to the celld Deployment at
# /etc/agentcell/providers.d and restart.
```

---

## 7. Create your first Cell and drive it

`cellctl` uses your kubeconfig. Point it at the control namespace (default
`agentcell-system`).

```sh
# A project Cell with a resident preview
cellctl cell create shop \
  --repo https://github.com/you/shop.git --image ghcr.io/agentcell/devbox \
  --secret git-cred \
  --preview "npm run dev -- --host" --preview-port 5173 \
  --description "极简电商:商品列表 + 购物车"

cellctl cells                      # watch it reach Ready

# Dispatch work; --follow points the preview at this session's worktree
cellctl dispatch shop --task "把商品卡片改成两列" \
  --runner claude --provider aliyun-bailian --model qwen3-coder-plus \
  --cred bailian-key --follow

cellctl sessions shop              # Running → Settled (branch session/<id> pushed)

# Ship the reviewed branch to the isolated production zone
cellctl release shop --ref <branch-or-tag-or-sha>
```

---

## 8. Access the web UI

celld's Service is ClusterIP. For a quick look, port-forward:

```sh
kubectl -n agentcell-system port-forward svc/celld 8080:80
```

Open `http://localhost:8080`, and at `/login` paste one of the tokens from
§5a. The UI is split: product description + dispatch + session list on the
left, the **resident preview** on the right; a "发布到正式区" button releases.
API/CLI callers send `Authorization: Bearer <token>` instead.

**Real access** — put an Ingress (or a k3s ServiceLB / LoadBalancer) in
front of `svc/celld` with TLS. The token is the only credential, so **serve
celld over HTTPS**; the login cookie is `HttpOnly` + `SameSite=Lax`.

---

## 9. Production notes

- **Single celld replica.** celld runs without leader election; keep
  `replicas: 1`. Running two reconcilers is not supported yet.
- **Storage & multi-node.** The workspace PVC is RWO and session pods have
  node affinity to their anchor, so a Cell lives on one node — fine on
  single-node k3s. For a Cell to survive node loss / reschedule across
  nodes, set `spec.storageClassName` to an **RWX** class (NFS, Ceph, cloud
  NAS/CFS). This is where cloud presets (Alibaba ACK NAS, Tencent TKE CFS)
  will plug in.
- **Egress policy.** Cell namespaces allow egress only to **DNS (53)** and
  **HTTPS (443)**. Your git remote and model endpoints must be reachable
  over 443. A self-hosted git server on another port would be blocked —
  adjust the `allow-egress` policy if you need that.
- **Pod Security.** Cell namespaces enforce `restricted`; custom devbox
  images must run non-root (uid 1000). celld itself runs non-root with a
  read-only root filesystem.
- **Sizing.** Rule of thumb per active Cell: ~1–2 CPU / 1–2 GiB for the
  anchor+preview, plus each session's `spec.sessionResources` budget
  (default 1 CPU / 2 GiB). Slot count is `spec.maxSessions` (default 2).
- **Backup.** All platform state is in the CRDs (etcd) plus each Cell's
  workspace PVC. Back up etcd (k3s: `/var/lib/rancher/k3s/server/db`) and
  your PVs. The git remote is the source of truth for code.
- **Upgrades.** `kubectl apply -f config/crd/` (additive), rebuild+reload
  the celld image, `kubectl -n agentcell-system rollout restart deploy/celld`.
  Cells reconcile to the new controller without recreation; `cell rebuild`
  semantics let you roll a Cell to a clean state.

---

## 10. Uninstall

```sh
kubectl delete cells --all -A          # finalizers tear down each cell-<name> ns
kubectl delete -f config/install.yaml -f config/crd/
```

Delete Cells first: the Cell finalizer waits for its workload namespace to
be fully removed before releasing, so removing the CRDs first would strand
namespaces.

---

## 11. Troubleshooting

| Symptom | Likely cause / fix |
|---|---|
| celld pod not Ready, logs "refusing to expose an unauthenticated control plane" | No `celld-tokens` secret — create it (§5a) and restart. |
| `cellctl` errors on create | Your kubeconfig lacks RBAC to `agentcell.io`, or wrong `--namespace`. |
| Cell stuck `Pending` | Anchor not Ready — `kubectl -n cell-<name> logs sts/anchor -c anchor`; often a clone failure (bad `git-cred`, repo URL, or blocked egress). |
| Preview / `/app` returns 502 | Upstream not serving yet. Check `kubectl -n cell-<name> get pods,endpoints`; confirm the preview command actually binds `--preview-port`. Readiness probes gate this, so a persistent 502 means the dev server is crashing. |
| Session stuck `Queued` | All slots busy — raise `spec.maxSessions`, or a prior session leaked a lease (the controller sweeps stale leases each reconcile). |
| Session `Settled` but no branch on remote | It produced no commits (Discarded), or push failed and the settle job is retrying — `kubectl -n cell-<name> logs job/settle-<id>`. |
| Namespace stuck `Terminating` on delete | A protected PVC is finalizing; the Cell finalizer intentionally waits. Give it a moment; check for stuck PVs. |
| Image pull errors on a custom cluster | Push images to a registry every node can reach and update the refs; imported-only images work single-node with `IfNotPresent`. |

For the full "what's implemented vs designed" matrix, see the
[README](../README.md).
