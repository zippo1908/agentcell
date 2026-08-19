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
        STORE[("accounts · roles · grants<br/>SQLite on a volume<br/>uploaded bytes beside it, not in it")]
    end
    subgraph cell["cell-shop (one project)"]
        ANCHOR["anchor pod<br/>clone + resident preview"]
        PVC[("workspace PVC")]
        RT["runtime pod, one per PERSON<br/>tmux · agents · sessions as windows<br/>(no SA token)"]
        SESS["session pod (non-resident)<br/>agent in a worktree<br/>(no SA token)"]
        SETTLE["settle job<br/>push session/&lt;id&gt;"]
        PROD["prod deployment<br/>isolated release"]
    end
    FORGE[("GitHub / GitLab")]
    UI -->|"HTTPS (bearer/cookie)"| CELLD
    CELLD -->|reconcile| ANCHOR & RT & SESS & SETTLE & PROD
    CELLD --- STORE
    ANCHOR & SETTLE & PROD -->|"git + SA token"| BROKER
    BROKER -->|"real credential"| FORGE
    SESS & RT -.->|shared| PVC
    ANCHOR -.-> PVC
```

## Cell anatomy — one warm project, one runtime per person

A session is a **window in its owner's runtime**, not a pod of its own
(ADR-0009, ADR-0010): the agent CLIs already manage several conversations, and
one tmux server per person is what the platform actually has to provide. Each
person gets a Unix uid allocated once and never reused, and a `0700` tree —
which is why one person's unfinished work is invisible to another without any
policy being consulted.

```mermaid
flowchart TB
    subgraph CELL["Cell = namespace + anchor + PVC (warm)"]
        ANCHOR["anchor: clone · base preview · heartbeat · reap"]
        OBJ[("/workspace/repo<br/>shared object store, read-only to sessions")]
        KN[("/workspace/knowledge<br/>persistent, shared across sessions")]
        subgraph U1["/workspace/users/1001 — Alice, 0700"]
            T1["tmux server"]
            W1["worktrees/&lt;session-id&gt;"]
            C1[("credentials/<br/>ONE connected account, shared by her sessions")]
        end
        subgraph U2["/workspace/users/1002 — Bob, 0700"]
            T2["tmux server"]
            W2["worktrees/&lt;session-id&gt;"]
        end
    end
    T1 --> W1
    T2 --> W2
    W1 & W2 -.->|"git alternates"| OBJ
    T1 & T2 -.->|"read / distil"| KN
```

Two directories inside a worktree are put there by the platform rather than by
git: `.agentcell/library/` holds the project's uploaded files as text the agent
reads with its ordinary tools, and it is **topped up while the session runs** —
a file uploaded mid-conversation arrives within a reconcile, without a restart.

## Session lifecycle — settle is mandatory

```mermaid
flowchart LR
    D[dispatch] --> Q{slot free?}
    Q -->|no| QU[Queued]
    Q -->|yes| R[Running<br/>agent in worktree]
    QU --> R
    R -->|"idle ~15min"| DO[Dormant<br/>slot and runtime given back<br/>worktree + conversation kept]
    DO -->|"open terminal / follow-up"| R
    R -->|exit / TTL / delete / pod dies| SE[Settling<br/>settle job]
    SE -->|commits pushed| OK[Settled<br/>session/&lt;id&gt; on remote]
    SE -->|nothing produced| DI[Discarded]
    OK & DI --> RC[worktree + lease reclaimed]
    DO -->|"TTL after sleeping"| SE
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

Neither session pods nor per-user runtimes carry a ServiceAccount token
(`automountServiceAccountToken: false`) or broker egress, so the processes that
run repository and model code cannot reach the broker at all. See [ADR-0005](adr/0005-git-broker.md) for the full trust
model.
