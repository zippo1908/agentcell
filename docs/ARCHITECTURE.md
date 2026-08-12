# Architecture

A visual tour of AgentCell. Rationale for each decision lives in
[docs/adr/](adr/); this page shows how the pieces fit.

## Control plane and Cells

Two tiers: a non-root-ish control plane in `agentcell-system`, and one
resident namespace per project. The git-broker is the only component holding
forge credentials.

```mermaid
flowchart TB
    subgraph user["User"]
        UI["Browser UI / cellctl"]
    end
    subgraph cp["agentcell-system (control plane)"]
        CELLD["celld<br/>operator (Cell/Session CRDs)<br/>API · auth · preview/app proxy"]
        BROKER["git-broker<br/>holds forge creds<br/>TokenReview + push policy"]
    end
    subgraph cell["cell-shop (one project)"]
        ANCHOR["anchor pod<br/>clone + resident preview"]
        PVC[("workspace PVC")]
        SESS["session pods<br/>agent in a worktree<br/>(no SA token)"]
        SETTLE["settle job<br/>push session/&lt;id&gt;"]
        PROD["prod deployment<br/>isolated release"]
    end
    FORGE[("GitHub / GitLab")]
    UI -->|"HTTPS (bearer/cookie)"| CELLD
    CELLD -->|reconcile| ANCHOR & SESS & SETTLE & PROD
    ANCHOR & SETTLE & PROD -->|"git + SA token"| BROKER
    BROKER -->|"real credential"| FORGE
    SESS -.->|shared| PVC
    ANCHOR -.-> PVC
```

## Cell anatomy — resident instance, disposable slots

```mermaid
flowchart TB
    subgraph CELL["Cell = namespace + anchor + PVC (warm)"]
        PID1["cell-runtime PID 1<br/>clone · preview · heartbeat · reap"]
        OBJ[("/workspace/repo<br/>git object store")]
        subgraph S1["Slot s01 — occupied"]
            W1["worktree .cells/s01"]
            A1["agent (claude/codex/pi)"]
        end
        S2["Slot s02 — vacant"]
        KN[("/workspace/knowledge")]
    end
    PID1 --> S1 & S2
    A1 -->|cwd| W1
    W1 -.->|shares| OBJ
    A1 -.->|reads/distills| KN
```

## Session lifecycle — settle is mandatory

```mermaid
flowchart LR
    D[dispatch] --> Q{slot free?}
    Q -->|no| QU[Queued]
    Q -->|yes| R[Running<br/>agent in worktree]
    QU --> R
    R -->|exit / TTL / delete / pod dies| SE[Settling<br/>settle job]
    SE -->|commits pushed| OK[Settled<br/>session/&lt;id&gt; on remote]
    SE -->|nothing produced| DI[Discarded]
    OK & DI --> RC[worktree + lease reclaimed]
```

## git-broker — forge token in no workload pod

```mermaid
flowchart LR
    W["anchor / settle / prod<br/>(broker-client, audience-bound SA token)"]
    B["git-broker"]
    K["kube API<br/>TokenReview"]
    C[("Cell CR + git Secret")]
    F[("forge")]
    W -->|"git + SA token"| B
    B -->|"verify token + audience"| K
    K -->|"namespace + SA + pod-name"| B
    B -->|"resolve remote + creds"| C
    B -->|"push only session/&lt;id&gt; from settle role"| F
```

Session pods have **no** token and no broker egress, so they cannot reach the
broker at all. See [ADR-0005](adr/0005-git-broker.md) for the full trust
model.
