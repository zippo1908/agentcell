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

The packages are private, so create a pull secret and point the chart at it:

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

Projects with a toolchain of their own (a JVM, Python, a private registry)
should build from `images/devbox/Containerfile` and add to it.

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
```

The last line is the only one that proves the rest.
