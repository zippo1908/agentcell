#!/usr/bin/env bash
# Local end-to-end test for AgentCell against a real (k3s) cluster.
#
# It validates the whole platform chain with a token-free fake agent:
#   auth enforced → Cell reconciles → anchor+preview reachable through the
#   authenticated proxy → dispatch → settle pushes session/<id> to the real
#   remote → release → /app reachable.
#
# What it needs (a real git path is intentional — it exercises the
# credential mapping and the HTTPS egress NetworkPolicy):
#   - a reachable Kubernetes cluster (single-node k3s is fine) + kubectl
#   - podman (or docker) to build images
#   - a git repo the test can push to, with a main branch already present:
#       E2E_REPO_URL   e.g. https://github.com/you/agentcell-e2e.git
#       E2E_GIT_USER   e.g. you
#       E2E_GIT_TOKEN  a PAT with push access
#   - optional: E2E_IMPORT=1 to `k3s ctr images import` the built images
#
# It is intended to be run by a human or driven by a local coding agent
# (see docs/E2E.md). Re-runnable: it tears down its Cell at the start.
set -euo pipefail

: "${E2E_REPO_URL:?set E2E_REPO_URL to a pushable git repo with a main branch}"
: "${E2E_GIT_USER:?set E2E_GIT_USER}"
: "${E2E_GIT_TOKEN:?set E2E_GIT_TOKEN}"
NS=agentcell-system
CELL=e2e
BUILDER=${BUILDER:-podman}
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

log()  { printf '\n\033[1;32m== %s\033[0m\n' "$*"; }
fail() { printf '\n\033[1;31mFAIL: %s\033[0m\n' "$*"; exit 1; }

# http_ok retries a proxied URL until it returns a real application response
# (2xx/3xx/4xx). A 5xx or 000 means the upstream isn't actually serving —
# that is a failure, not "reachable". $1 url, $2 label.
http_ok() {
  local url="$1" label="$2" code
  for _ in $(seq 1 30); do
    code=$(curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $TOKEN" "$url" || true)
    case "$code" in
      2??|3??|4??) echo "$label HTTP status: $code"; return 0 ;;
    esac
    sleep 3
  done
  fail "$label never served a real response (last status: $code; 5xx/000 means the upstream app is down)"
}

log "1/8 build binaries + images"
make build build-runtime-static
$BUILDER build -t ghcr.io/agentcell/celld       -f images/celld/Containerfile .
$BUILDER build -t ghcr.io/agentcell/git-broker  -f images/git-broker/Containerfile .
$BUILDER build -t ghcr.io/agentcell/devbox-e2e  -f images/devbox-e2e/Containerfile .
if [ "${E2E_IMPORT:-0}" = "1" ]; then
  # Import each image separately: k3s ctr can collapse tags from a multi-image
  # Docker archive, making tags point at the wrong image.
  $BUILDER save ghcr.io/agentcell/celld      | sudo k3s ctr images import -
  $BUILDER save ghcr.io/agentcell/git-broker | sudo k3s ctr images import -
  $BUILDER save ghcr.io/agentcell/devbox-e2e | sudo k3s ctr images import -
fi

log "2/8 install CRDs + control plane + secrets"
kubectl apply -f config/crd/ -f config/install.yaml
TOKEN="e2e-$(head -c16 /dev/urandom | od -An -tx1 | tr -d ' \n')"
kubectl -n "$NS" delete secret celld-tokens git-cred e2e-model >/dev/null 2>&1 || true
kubectl -n "$NS" create secret generic celld-tokens --from-literal=tokens="$TOKEN"
kubectl -n "$NS" create secret generic git-cred --type=kubernetes.io/basic-auth \
  --from-literal=username="$E2E_GIT_USER" --from-literal=password="$E2E_GIT_TOKEN" \
  --from-literal=repo_url="$E2E_REPO_URL"
# Fake agent ignores the model key, but the controller requires the secret.
kubectl -n "$NS" create secret generic e2e-model --from-literal=key=dummy
kubectl -n "$NS" rollout restart deploy/celld
kubectl -n "$NS" rollout status deploy/celld --timeout=120s
# git-broker holds the forge token; workloads reach git only through it.
kubectl -n "$NS" rollout status deploy/git-broker --timeout=120s

log "3/8 auth is enforced (401 without token, 200 with)"
kubectl -n "$NS" port-forward svc/celld 18080:80 >/tmp/e2e-pf.log 2>&1 &
PF=$!; trap 'kill $PF >/dev/null 2>&1 || true' EXIT
sleep 3
code=$(curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:18080/api/cells || true)
[ "$code" = "401" ] || fail "unauthenticated /api/cells returned $code, want 401"
code=$(curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $TOKEN" http://127.0.0.1:18080/api/cells || true)
[ "$code" = "200" ] || fail "authenticated /api/cells returned $code, want 200"

log "4/8 create the Cell (fake-agent image, httpd preview)"
kubectl delete cell "$CELL" -n "$NS" --ignore-not-found --wait=true
go run ./cmd/cellctl cell create "$CELL" \
  --repo "$E2E_REPO_URL" --image ghcr.io/agentcell/devbox-e2e --secret git-cred \
  --preview "httpd -f -p 3000 -h ." --preview-port 3000 \
  --description "e2e" --namespace "$NS"

log "5/8 wait for the Cell to be Ready"
for i in $(seq 1 60); do
  phase=$(kubectl -n "$NS" get cell "$CELL" -o jsonpath='{.status.phase}' 2>/dev/null || true)
  [ "$phase" = "Ready" ] && break
  sleep 5
done
[ "$phase" = "Ready" ] || fail "Cell not Ready (phase=$phase)"

log "6/8 preview actually serves through the authenticated proxy"
http_ok "http://127.0.0.1:18080/preview/$CELL/README.md" "preview"

log "7/8 dispatch a session and wait for a produced settle"
# Capture THIS session's name from cellctl output; never assume items[0],
# which on a re-run can be a leftover session from a prior attempt.
disp=$(go run ./cmd/cellctl dispatch "$CELL" --task "e2e change" \
  --runner codex --provider deepseek --cred e2e-model --namespace "$NS")
echo "$disp"
sess=$(printf '%s\n' "$disp" | sed -n 's#^session/\([^ ]*\) dispatched.*#\1#p' | head -1)
[ -n "$sess" ] || fail "could not determine dispatched session name from: $disp"
for i in $(seq 1 60); do
  phase=$(kubectl -n "$NS" get session "$sess" -o jsonpath='{.status.phase}' 2>/dev/null || true)
  [ "$phase" = "Settled" ] && break
  [ "$phase" = "Error" ] && fail "session $sess errored: $(kubectl -n "$NS" get session "$sess" -o jsonpath='{.status.message}')"
  sleep 5
done
[ "$phase" = "Settled" ] || fail "session $sess not Settled (phase=$phase)"
branch=$(kubectl -n "$NS" get session "$sess" -o jsonpath='{.status.branch}')
[ -n "$branch" ] || fail "settled session $sess has no branch recorded"
git ls-remote "https://$E2E_GIT_USER:$E2E_GIT_TOKEN@${E2E_REPO_URL#https://}" "$branch" \
  | grep -q "$branch" || fail "settled branch $branch not found on remote"
echo "settled branch on remote: $branch"

log "8/8 release to production and reach /app"
go run ./cmd/cellctl release "$CELL" --namespace "$NS"
for i in $(seq 1 60); do
  pp=$(kubectl -n "$NS" get cell "$CELL" -o jsonpath='{.status.productionPath}' 2>/dev/null || true)
  [ -n "$pp" ] && break
  sleep 5
done
[ -n "$pp" ] || fail "production path never set"
http_ok "http://127.0.0.1:18080/app/$CELL/README.md" "production"

log "E2E PASSED — auth, reconcile, preview served, dispatch→settle→branch, release served"
