# Third-party components

AgentCell is Apache-2.0. Everything below is compatible with it; nothing
here is copyleft, and nothing imposes a license obligation on your own
project code.

## Deployed alongside AgentCell (optional)

These run as **separate services**. AgentCell talks to them over the
network; their code is not linked into any AgentCell binary, so they do not
create a derivative work.

| Component | License | Role | Required? |
| --- | --- | --- | --- |
| Apache APISIX® | Apache-2.0 | Edge gateway: TLS, routing, rate limiting | No |
| Casdoor | Apache-2.0 | Identity provider: registration, login, user store | No |

**Neither is required.** celld verifies OIDC ID tokens itself, so any
standards-compliant provider works — Keycloak, Auth0, Okta, Alibaba Cloud
IDaaS, or Casdoor. And celld runs the browser login flow itself, so it
needs no gateway in front of it. We ship a first-class deployment path for
APISIX + Casdoor because it is a good default, not because AgentCell
depends on it.

If you mirror their images into your own registry you are redistributing
them, and Apache-2.0 §4 then applies: carry the LICENSE and any NOTICE file
the upstream artifact ships with. Referencing upstream images from a chart,
as the default deployment does, is not redistribution.

### Trademarks

Apache®, Apache APISIX® and the Apache feather logo are trademarks of the
Apache Software Foundation. AgentCell is **not** affiliated with, endorsed
by, or sponsored by the ASF. We name APISIX only to describe what AgentCell
interoperates with.

Casdoor is a trademark of its respective owners; the same applies.

## Go dependencies

Direct dependencies with a bearing on identity:

| Module | License |
| --- | --- |
| `github.com/coreos/go-oidc/v3` | Apache-2.0 |
| `golang.org/x/oauth2` | BSD-3-Clause |
| `github.com/go-jose/go-jose/v4` | Apache-2.0 |
| `k8s.io/*`, `sigs.k8s.io/controller-runtime` | Apache-2.0 |

The full transitive set is in `go.sum`; `go-licenses report ./...` prints it
with licenses attached.

## Container base images

AgentCell images build `FROM gcr.io/distroless/static-debian12:nonroot`
(Google, Apache-2.0 tooling / Debian-licensed contents).

We deliberately do **not** use Bitnami-packaged images for APISIX or
Casdoor: that catalogue's distribution terms changed in 2025 and the free
tier no longer carries versioned tags. The manifests reference each
project's own official images.
