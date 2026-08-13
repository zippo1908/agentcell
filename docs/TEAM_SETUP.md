# Standing up AgentCell for a team

What a working deployment needs, in the order it needs it, and which parts
somebody has to decide rather than run.

The single-operator path in [DEPLOYMENT.md](DEPLOYMENT.md) works with a
shared token and no identity provider. This page is the other case: several
colleagues, each with their own sessions, opening a URL and starting work.

## Decide first

These are choices, not commands, and every one of them blocks something.

| Decision | Why it blocks |
| --- | --- |
| **Which host, and may we install on it** | k3s claims ports 80/443 and writes iptables rules. Deciding this on a machine that already serves something is how an outage happens. |
| **Two DNS names** | The console needs one. Untrusted preview content needs a *different registrable domain* — not a subdomain of the console's (ADR-0007), or a previewed app can set cookies the console receives. |
| **A wildcard TLS certificate for the preview domain** | Every Cell gets `<cell>-dev.<domain>` and `<cell>-prod.<domain>`. |
| **Who the identity provider is** | Casdoor ships in `deploy/identity/`; any OIDC provider works. Without one, everyone is the same principal and nothing is private. |
| **Whose model API key, and how many** | Keys are per user and per session; a shared key means shared spend and shared blame. |
| **In-Cell production, or handoff** | If production already lives somewhere with its own pipeline, hand off — do not run a second copy here. |

## Prepare

### 1. A node that can actually hold the work

Per Cell, at 2 slots and the defaults, the scheduler reserves roughly
**3.8 CPU / 3.5 GiB in requests** — sessions, the anchor, production, settle
headroom, and one runtime per slot. Limits go higher; a dev server is the
largest resident consumer and is bounded, not reserved.

A Cell **cannot span nodes**: the workspace PVC is ReadWriteOnce and every
pod is pinned to the anchor's node. So size for the busiest project, and add
Cells to add capacity — not slots.

### 2. Images the node can pull

**If the cluster cannot reach ghcr.io — or reaches it slowly — run a registry
inside it.** This is the normal case on an internal network, and it is worth
doing before anything else: without it, every image change becomes a manual
transfer, and "somebody downloads a 2 GB tarball and copies it to the node"
is not a deployment procedure.

```sh
kubectl apply -f deploy/registry/registry.yaml     # registry:2, NodePort 30500
```

Then tell containerd it may talk to it over plain HTTP — **once, on each
node**, because a registry without TLS is refused by default:

```sh
printf 'mirrors:\n  "NODE_IP:30500":\n    endpoint:\n      - "http://NODE_IP:30500"\n' \
  > /etc/rancher/k3s/registries.yaml
systemctl restart k3s
```

Push images from a machine that *can* reach the internet, through the
kube-api tunnel — no extra ingress, no firewall change:

```sh
kubectl -n agentcell-system port-forward svc/registry 5000:5000 &
podman push --tls-verify=false 127.0.0.1:5000/devbox-slim:<tag>
```

`podman push` is resumable, so a dropped tunnel costs the remaining layers,
not the whole image.

**Give each build a new tag.** Pods are rendered with `IfNotPresent`, so
rebuilding a tag the node has already pulled leaves that node running the old
image indefinitely — with no error anywhere to say so. Overwriting a tag is
the single most common way to spend an afternoon debugging a change that did
deploy.

If the cluster *can* reach ghcr.io, the packages are private, so create a
pull secret and point the chart at it instead:

```sh
kubectl -n agentcell-system create secret docker-registry regcred \
  --docker-server=ghcr.io --docker-username=<user> --docker-password=<token>
helm upgrade --install agentcell oci://ghcr.io/zippo1908/charts/agentcell \
  --version 0.1.0-alpha.5 --namespace agentcell-system --create-namespace \
  --set image.pullSecret=regcred
```

The operator mirrors that secret into every Cell namespace, because kubelet
resolves pull secrets locally.

### 3. A devbox image with the CLIs your team uses

**This is the item most likely to be missed.** The platform runs whatever
image a Cell names, and it must contain the agent CLIs, `git`, and `tmux`
(resident sessions need it). The published one —
`ghcr.io/zippo1908/devbox:v0.1.0-alpha.5` — carries Claude Code and Codex CLI
on Node 22, and is about 2 GB, so pre-pull it on the node rather than
discovering the wait during a demo:

```sh
crictl pull ghcr.io/zippo1908/devbox:v0.1.0-alpha.5   # or: nerdctl / docker pull
```

On a slow internal link that 2 GB is roughly an hour before anyone can try
the product, so there is a second image —
`images/devbox/Containerfile.slim`, **814 MB** — which trades the Debian
userland the agents never touch for alpine and keeps exactly what the
platform requires: `/agentcell/cell-runtime`, `git`, `tmux`, `httpd` for
previews, and the agent CLIs.

Projects with a toolchain of their own (a JVM, Python, a private registry)
should build from `images/devbox/Containerfile` and add to it.

Whichever you use, **the image must contain whatever a Cell's preview
command names**. That command is written per project against the image the
project had; moving a Cell onto a leaner image without it is how a preview
starts failing with nothing but `executable file not found` to show for it.

### 4. Identity

```sh
kubectl apply -f deploy/identity/casdoor.yaml
# in Casdoor: create an application, redirect URL https://<console>/auth/callback
kubectl -n agentcell-system create secret generic oidc --from-literal=clientSecret=<secret>
helm upgrade ... --set oidc.issuer=https://<casdoor> --set oidc.clientID=<id> \
                 --set oidc.existingSecret=oidc
```

celld verifies the token itself, so no gateway is required. Keep at least one
static token configured as break-glass for when the IdP is down —
`/login/token` stays reachable.

### 5. Credentials, per person

```sh
# a model key, owned by the person who will spend it
kubectl -n agentcell-system create secret generic alice-model --from-literal=key=sk-...
kubectl -n agentcell-system label secret alice-model agentcell.io/owner=<their u-id>

# the forge credential, bound to one repository
kubectl -n agentcell-system create secret generic git-cred \
  --type=kubernetes.io/basic-auth \
  --from-literal=username=oauth2 --from-literal=password=<token> \
  --from-literal=repo_url=https://git.example.com/team/shop.git \
  --from-literal=forge=gitlab
```

A user's `u-` id is shown in the console sidebar; it is a hash, not an email.

### 6. Close the Cells

A Cell with no members is open to **every authenticated user** — right for a
single operator, wrong for a team. Add the first member from the console's
settings tab and the Cell closes automatically.

## Using another vendor's models with Claude Code

Supported, and it is the normal path for a team that cannot reach
`api.anthropic.com`:

```sh
cellctl dispatch shop --task "..." --runner claude --provider moonshot \
  --model kimi-k2-turbo-preview --cred alice-model
```

AgentCell injects the provider's Anthropic-compatible endpoint, the key, the
model, and the model's **real context window** — without that last one the
CLI assumes its own default for an unfamiliar model name and starts
compacting early, quietly truncating work the model could have held.

Two things to know:

- the console shows a note naming both vendors. The endpoint is published for
  this; whether a CLI's licence permits pointing it elsewhere is that
  vendor's to define and yours to read. AgentCell states the pairing and does
  not choose it for you;
- a fully domestic pairing avoids the question entirely — Kimi CLI with
  Moonshot, or Qwen Code with DashScope. See [RUNNERS.md](RUNNERS.md).

## Running celld with more than one replica

celld holds a lease, so exactly one replica reconciles while **every** replica
serves the console — the controllers are the only exclusive part, because
they are the only part that writes. Two reconciling at once would both claim
the same slot and both create the same session pod: the slot gate is
optimistic locking on one object, which is sound against concurrent sessions
and says nothing about concurrent controllers.

Scaling needs one more thing, and the chart refuses to render without it:

```sh
kubectl -n agentcell-system create secret generic celld-preview-key \
  --from-literal=previewKey="$(openssl rand -hex 32)"
helm upgrade ... --set celld.replicas=2 --set previewKeySecret=celld-preview-key
```

Without a shared key each replica signs preview tickets with its own, so a
ticket minted by one is refused by the others and previews fail
intermittently — only under load, which is the worst way to find out.

Measured takeover on a killed leader: about 4 seconds. Sessions already
running are untouched either way; they are self-contained pods.

## First-run checklist

```
[ ] host agreed, and installing on it agreed
[ ] console DNS + TLS
[ ] preview DNS on a different registrable domain + wildcard TLS
[ ] pull secret created, chart installed with image.pullSecret
[ ] devbox image pre-pulled on the node
[ ] OIDC issuer + client, and one break-glass token
[ ] git credential with repo_url and forge
[ ] one model key per person, labelled with their owner id
[ ] first Cell created, first member added (which closes it)
[ ] a dispatch that reaches the model, and a settle that reaches the forge
[ ] if celld.replicas > 1: previewKeySecret set
```

The last line is the only one that proves the rest.
