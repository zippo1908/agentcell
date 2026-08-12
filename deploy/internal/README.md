# Internal deployment line

This branch (`deploy/internal`) carries the configuration for the on-prem
AgentCell deployment. `main` stays the clean upstream: nothing
company-specific belongs there.

## What lives here

- `deploy/internal/values.yaml` — Helm values for our cluster.
- Anything else deployment-shaped and specific to us (ingress hosts,
  storage classes, gateway wiring).

## What must NEVER live here

**No secrets.** No API tokens, forge PATs, model keys, kubeconfigs or
certificates — not even base64'd, not even "temporarily". Create them on
the cluster:

```sh
kubectl -n agentcell-system create secret generic celld-tokens \
  --from-literal=tokens="$(openssl rand -hex 24)"
kubectl -n agentcell-system create secret generic git-cred \
  --type=kubernetes.io/basic-auth \
  --from-literal=username=<user> --from-literal=password=<PAT> \
  --from-literal=repo_url=<https clone url>
```

Two reasons this rule is absolute: the repository may become public (this
branch would go with it), and secrets committed to git survive in history
even after deletion.

## Staying in sync with upstream

Take upstream fixes regularly — the security work lands on `main`:

```sh
git fetch origin
git checkout deploy/internal
git merge origin/main        # or rebase, if this branch stays config-only
```

Keeping this branch **config-only** is what makes that merge boring. The
moment internal *code* lands here, every upstream change becomes a manual
port — the divergence tax the downstream-layer plan exists to avoid. If
internal behaviour is needed, prefer:

1. upstream it behind a flag (most internal needs are somebody else's
   needs too — SSO hooks, notification webhooks), or
2. build it as a separate service that drives AgentCell's CRDs/API,
   in its own private repository.

## Deploying

```sh
helm upgrade --install agentcell deploy/charts/agentcell \
  -n agentcell-system --create-namespace \
  -f deploy/internal/values.yaml
```
