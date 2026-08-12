# ADR-0010: Resident sessions — a slot you can attach to

Status: accepted
Builds on: [ADR-0009](0009-runtime-isolation.md) (whose files these are)

## Context

A Session has been a one-shot pod: create a worktree, run the agent, exit,
settle. That shape is what makes the dispatch → settle → review loop easy to
reason about, and it is why "the work is never lost" is provable.

It is also the wrong shape for the way the tool is actually used. When the
agent finishes and the result is nearly right, the only move available is to
dispatch a *new* session, which starts from a clean worktree and has to
rediscover everything the last one learned. The context that made the work
good — what was tried, what was rejected, what the user said halfway through
— dies with the pod.

The original sketch for this product was "a project keeps a resident
instance; a tmux takes one slot; when you log off it is reclaimed". This ADR
is the first half of that: the slot.

## Decision

### 1. Resident is opt-in, and one-shot stays the default

`Session.spec.resident` keeps the slot alive after the agent finishes. It is
off by default because the one-shot shape is what the SDLC loop is built on,
and because a resident session holds a slot (and a `maxSessions` seat) until
somebody ends it.

Making it a mode rather than a replacement means the dispatch → settle →
review loop that Runs 1-5 verify keeps working exactly as it did.

### 2. Mandatory settle survives

A resident session still settles: on TTL, on an explicit request, or if its
pod disappears. What changes is *who decides when*, not *whether*. The
publication gate and the "unconfirmed push fails the job" guarantee are
untouched.

This is the seam between the two models, and it is deliberately drawn here:
the worktree may live as long as its owner wants, but nothing reaches the
project layer without going through settle.

### 3. The agent runs as a command typed into a shell, not as the window's
process

`tmux new-session` starts a shell; the agent command is sent to it. So the
window outlives the agent, which is the entire point — you can look at what
happened and say one more thing in the same context.

It also makes "continue" trivially the same mechanism as "start": more text
sent to the same window.

### 4. The tmux socket is in the owner's private tree

Never tmux's default `/tmp/tmux-<uid>/default`. That path is derived from the
uid on a filesystem several users share, so it is precisely where two users
can collide — and **a tmux socket is an authority, not a name**: anything that
can open it can attach to that terminal and type into it. Application-level
user separation would not help; the socket is the thing being checked.

The socket sits in the `0700` private tree from ADR-0009, so the kernel is
what enforces it.

### 5. The console reaches the session by exec, not by giving the pod a token

Telling whether the agent is still working, and sending it another
instruction, both happen by exec'ing into the session pod.

The alternative — a ServiceAccount token in the session pod so it could
report its own status — is rejected. That pod runs untrusted repository and
model code; ADR-0005 deliberately gives it no token at all. Trading that for
a status field is a bad trade.

The agent's completion is therefore reported the only way a pod with no
credentials can: a marker file the shell writes after the agent returns, read
on demand.

## Consequences

- celld needs `pods/exec`. That is a real privilege increase for the control
  plane, and it is scoped to what it is for: the console already holds the
  cluster rights to create these pods.
- Text sent into a session is user input handed to a shell. It is passed as a
  single argv element and quoted, never spliced — a semicolon in a task
  description stays a semicolon. Two tests cover it, round-tripped through a
  real shell.
- A resident session occupies a slot until it ends. That is the intended
  behaviour ("one tmux takes one thread"), but it means `maxSessions` now
  bounds concurrent *people*, not concurrent agent runs.

## What this does not yet do

The rest of the original sketch:

- **One runtime per user, many sessions inside it.** Today each resident
  session is still its own pod with its own tmux server. The user-level tmux
  server that hosts several windows is the next step.
- **Resume.** The three tiers — attach a live tmux, resume the CLI from its
  own state, rebuild from a checkpoint — need the CLI-native state to be
  captured deliberately. Today it lands in the private `$HOME` because that
  is where the CLIs write it, which makes tier 2 possible but does not
  implement it.
- **A terminal in the browser.** Attaching is a `kubectl exec` today. The API
  surface that a web terminal needs is the same exec path.
