# Runners: adding an agent CLI

A **runner** is the agent CLI a Session runs. AgentCell ships a few and treats
them as data, because a CLI's flags are the fastest-moving thing in this
system — an upstream release that renames `--resume` should cost you one file,
not an AgentCell release.

```sh
kubectl -n agentcell-system create configmap my-runners --from-file=mine.yaml
# or, with the chart:
helm upgrade ... --set-file 'presets.runners.mine\.yaml=mine.yaml'
```

Anything you drop in `/etc/agentcell/runners.d/*.yaml` merges over the
built-ins, per runner name.

## What AgentCell needs from a CLI

Three things, in order of how much they matter:

1. **A non-interactive form** that takes a task and works in the current
   directory. Everything else is optional; without this the CLI cannot be
   dispatched at all.
2. **Endpoint configuration through the environment** — an OpenAI- or
   Anthropic-compatible base URL and key. This is what lets the same CLI run
   against any provider in the registry instead of one vendor's cloud.
3. **A way to address a conversation again**, so a follow-up in a resident
   session continues rather than starts over. Either shape works:
   - the CLI accepts an id you choose (`session_id: uuid` + `start`/`resume`);
   - or it resumes the most recent one, in which case give it a per-session
     state directory (`session_home_env`) so "most recent" cannot mean a
     sibling session's conversation.

A runner with only (1) and (2) is perfectly usable. It just means a follow-up
opens a new context, and the UI says so rather than letting you assume
otherwise.

## The schema

```yaml
version: 1
runners:
  example:
    display_name: Example CLI
    vendor: example-inc          # who publishes it; drives the cross-vendor note
    protocols: [anthropic, openai]   # preference order
    headless: ["example", "-p", "{{task}}"]
    session_id: uuid             # or omit: the CLI names its own
    start:  ["example", "--session", "{{session}}", "-p", "{{task}}"]
    resume: ["example", "--resume", "{{session}}", "-p", "{{task}}"]
    session_home_env: EXAMPLE_HOME   # only for recency-resuming CLIs
```

`{{task}}` and `{{session}}` are substituted as **whole argv elements**. A
task containing a semicolon stays one argument; nothing is ever assembled
into a command line and split. Definitions that cannot work — unknown
placeholders, `resume` by id with no `session_id`, no `headless` — are
refused when celld starts, not at 3am.

## Candidates worth integrating

Open-source CLIs with the properties above. **Verify the flags against the
version you pin** — this table is a starting point for where to look, not a
promise about a release you have not tested:

| CLI | Licence | Conversation handling | Extensibility | Notes for AgentCell |
| --- | --- | --- | --- | --- |
| **OpenAI Codex CLI** | Apache-2.0 | resumes; `exec resume` | prompt files become slash commands | Built in. Recency-based, so it gets a per-session `CODEX_HOME`. |
| **Gemini CLI** | Apache-2.0 | session save/resume | custom commands as files; MCP; extensions | Strong fit. Configure through env for an OpenAI-compatible endpoint where supported. |
| **Qwen Code** | Apache-2.0 | inherits Gemini CLI's | inherits Gemini CLI's | A Gemini CLI fork aimed at Qwen and OpenAI-compatible endpoints. The most obvious fully-domestic pairing for a team in mainland China. |
| **opencode** | MIT | named sessions, continue | custom commands, MCP | Provider-agnostic by design, which is exactly the shape this registry wants. |
| **Goose** | Apache-2.0 | `session --resume` | extensions (MCP), recipes | Sessions are first-class rather than bolted on. |
| **Aider** | Apache-2.0 | chat history restore | a large built-in command set | Closer to a pair programmer than an autonomous agent; excellent at focused edits. |
| **Kimi CLI** | see upstream | not yet verified here | — | Built in as a runner; deliberately declares no resume until someone checks a pinned version. |

Two things to check before wiring any of them up, because they are where
integrations actually fail:

- **Does the non-interactive mode really exit?** An agent that drops into a
  TUI and waits will hold a slot until its TTL.
- **Does it write its state under `$HOME`?** AgentCell gives each user a
  private `$HOME` (ADR-0009) and each recency-resuming session its own state
  directory. A CLI that keeps state somewhere global instead will leak
  context between a user's sessions — the failure Codex needed `CODEX_HOME`
  to avoid.

## Slash commands

Most of these CLIs implement `/commands` themselves, and that keeps working
inside AgentCell — a resident session is a real terminal, so attaching and
typing `/help` behaves exactly as it does locally.

AgentCell deliberately does not wrap them. A command set is the CLI's own
interface; intercepting it would mean tracking every upstream change and
breaking the muscle memory of anyone who already uses the tool.

What AgentCell adds around them is the part a CLI cannot do for itself: a
private runtime, a worktree, a review gate, and a settle that is the only way
work reaches the project.
