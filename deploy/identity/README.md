# Registration, login and the gateway

This directory deploys the identity path AgentCell recommends:

```
browser ──► Apache APISIX®  ──► celld (console + API)
              │  TLS, routing, rate limiting
              ▼
           Casdoor           registration, login, user store
```

**Both are optional.** celld verifies OIDC ID tokens itself and runs the
authorization-code + PKCE flow itself, so it works with any standards-
compliant provider and with no gateway at all. Use this path when you want
registration and login handled for you; swap Casdoor for Keycloak or your
corporate IdP by changing one issuer URL.

See [THIRD_PARTY.md](../../THIRD_PARTY.md) for licensing (everything here is
Apache-2.0) and trademark notes.

## Why celld verifies the token instead of trusting a header

The tempting wiring is to let the gateway authenticate and forward
`X-Forwarded-User`. Do not do that. celld is reachable on its ClusterIP from
anywhere in the pod network, so anything that can send an HTTP request could
then assert any identity — the login page would be decorative. celld
therefore checks the ID token's signature against the provider's JWKS,
along with issuer, audience and expiry, and never reads an identity header.

This is also why `--trust-forwarded-headers` remains off by default. It
governs `X-Forwarded-Proto/Host` only — used to reconstruct our own origin,
never identity — and is safe only behind a gateway that *overwrites* them.

## Install

### 1. Casdoor

```sh
kubectl apply -f casdoor.yaml
```

Then, in the Casdoor UI:

1. create an organization (or use `built-in`);
2. create an application named `agentcell`;
3. set the redirect URL to `https://<console-host>/auth/callback`;
4. copy the **Client ID** and **Client secret**.

Casdoor is what actually provides registration — self-service sign-up,
password policy, MFA, and (optionally) WeCom / LDAP / GitHub sign-in.
AgentCell deliberately implements none of that itself.

### 2. Tell celld about it

```sh
kubectl -n agentcell-system create secret generic oidc \
  --from-literal=clientSecret='<client secret>'

helm upgrade --install agentcell oci://ghcr.io/zippo1908/charts/agentcell \
  --namespace agentcell-system \
  --set oidc.issuer=https://casdoor.example.com \
  --set oidc.clientID=<client id> \
  --set oidc.existingSecret=oidc
```

`/login` now redirects to Casdoor. The static token form stays reachable at
`/login/token` as break-glass for when the IdP is down — keep at least one
token configured for exactly that reason.

### 3. APISIX (optional)

```sh
kubectl apply -f apisix.yaml
```

The route terminates TLS and forwards to celld. It deliberately does **not**
use the `openid-connect` plugin to authenticate: celld does that itself, and
two layers doing it independently is how redirect loops and confused-deputy
bugs appear. APISIX here is a gateway, not an auth proxy.

If you do want APISIX to enforce auth at the edge as well — reasonable if
celld shares the gateway with other services — enable `openid-connect` with
`bearer_only: true` and let it pass the token through untouched. celld will
verify it again. Never let the gateway strip the token and substitute a
header.

## What each user gets today, and what is still shared

With identity on, a Session records its creator and:

- another user's **running** session is invisible — task text included;
- a **settled** session is visible to the whole project: settle is the
  controlled publication step;
- asking about a session you do not own answers **404, not 403**;
- a model credential can only be spent by the user who owns it.

Not yet: per-user Unix UIDs, private storage, a resident per-user runtime
and per-user tmux sockets. Those are runtime isolation and get their own
ADR; today all workloads still run as `uid=1000` on the Cell's shared
volume, so **a user who can exec into a session pod can read another user's
worktree**. Until that lands, treat the Cell as a trust boundary between
projects, not between users.
