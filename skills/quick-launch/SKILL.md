---
name: quick-launch
description: "Start a one-shot Kilroy agent run from within another agent's conversation. Tags the run for later lookup, backgrounds it in a tmux session, and gives you the exact commands to check status and retrieve result.md when it's done. Use this for delegating research, investigations, diffs against a repo snapshot, or any single-agent task you want to fire-and-forget."
---

# Quick-Launch a Kilroy Run

Delegate a one-shot task to a Kilroy agent without blocking your own conversation. The run happens in its own git worktree, tagged with labels so you can find it again, and writes a single `result.md` you can read back.

Use this when:
- You want an agent to investigate something in parallel while you keep working.
- The task fits in one prompt + optional context file and one output file.
- You'd rather come back in a few minutes than wait synchronously.

Don't use this for:
- Multi-stage workflows that need more than "stage context → run agent → done". Author a custom graph instead (see `skills/create-dotfile`).
- Long-running build/test pipelines. Use a real workflow package.
- Tasks where you need live conversational back-and-forth with the agent.

## Step 1: Pick the agent

The quick-launch package (`workflows/quick-launch/`) ships three graph variants:

- `graph.dot` — claude (default)
- `graph.codex.dot` — OpenAI codex CLI
- `graph.gemini.dot` — gemini CLI

The agent is encoded in the graph file. Pick one with `--graph` when launching; otherwise the package loader uses `graph.dot`.

## Step 2: Prepare inputs

Two inputs land on disk for the agent:

- `.kilroy/TASK.md` — comes from `KILROY_INPUT_PROMPT` (required).
- `.kilroy/CONTEXT.md` — optional. Either:
  - `context_file`: absolute path to an existing file; the stage script copies it in.
  - `context`: an inline string; written verbatim.

Pass them as JSON to `--input`:

```bash
--input '{"prompt":"Investigate X and summarize findings","context_file":"/abs/path/to/briefing.md"}'
```

If the task is short and self-contained, you can skip the context entirely — the agent only reads `CONTEXT.md` if it exists.

## Step 3: Launch

From a directory containing a `run.yaml` (see "Minimal run.yaml" below):

```bash
kilroy attractor run --detach --tmux \
  --package <ABS_PATH>/workflows/quick-launch \
  --config run.yaml \
  --no-cxdb --skip-cli-headless-warning \
  --label task=<SHORT_SLUG> --label workflow=quick-launch \
  --input '{"prompt":"...","context_file":"/abs/path/to/context.md"}'
```

Optional additions:
- `--graph <ABS_PATH>/workflows/quick-launch/graph.codex.dot` to pick codex (or `.gemini.dot`).
- More `--label KEY=VALUE` flags to tag owner, run group, ticket id, etc. Tags are the only durable handle you have — use at least one specific one so you can find this run later.

On success the command prints `run_id=<ulid>` and `logs_root=...` and returns immediately. The run continues in a detached tmux session.

## Step 4: Check status

List tagged runs:

```bash
kilroy attractor runs list --label task=<SHORT_SLUG>
```

Show full detail (status, timing, paths, declared outputs) by run id or unique prefix:

```bash
kilroy attractor runs show 01KP646Y
```

Add `--json` for machine-readable output. The interesting fields for a caller are:
- `status` — `running`, `success`, `fail`, `canceled`
- `worktree_dir` — where the agent worked; still on disk with the final git state
- `outputs[].path` — absolute path to each collected output file (e.g. `result.md`)

## Step 5: Retrieve result.md

Once status is `success`:

```bash
kilroy attractor runs show 01KP646Y --print result.md
```

That streams the file straight to stdout. You can pipe it, redirect it, or read it directly into your current conversation.

If the agent wrote additional files you care about, list them:

```bash
kilroy attractor runs show 01KP646Y --outputs
```

…and `--print <filename>` any of them. Only files declared in `outputs=` on the graph or the node are collected into `logs_root/outputs/` — everything else stays in the worktree (`worktree_dir` from `runs show`).

## Minimal run.yaml

The quick-launch package does not include a run.yaml — callers provide one. A working minimum:

```yaml
version: 1

repo:
  path: /absolute/path/to/some/workspace

llm:
  cli_profile: real
  providers:
    anthropic:
      backend: cli
    openai:
      backend: cli
    google:
      backend: cli

git:
  require_clean: false
  run_branch_prefix: attractor/run
  commit_per_node: true

runtime_policy:
  stall_timeout_ms: 600000
  stall_check_interval_ms: 5000
  max_llm_retries: 2
```

`repo.path` is the workspace the run operates against. If it's a git repo, the engine creates a dedicated run branch + worktree automatically — your source tree is never touched in-place. If it's not a git repo, the run uses it as a plain directory.

## Inspecting a finished run

Everything lives in two places:

- `logs_root` — per-run operational state. `outputs/` holds collected files, `progress.ndjson` has the event stream, `<node_id>/` dirs hold per-stage artifacts.
- `worktree_dir` — the git worktree the agent worked in. Still present after the run finishes; you can `cd` there and inspect the final state, or `git log` the run branch.

Both paths are in `runs show` output.

For a GUI view of the graph, per-node prompts/responses, tool-call log, and output files, open:

```
http://localhost:9700/ui/#/run/<run_id>
```

(Start the server with `kilroy attractor serve` if it isn't running.)

## Failure handling

If launch itself fails (bad flags, missing CLI binary, config error), you get an error on stderr and no run is registered.

If launch succeeds but the run fails mid-flight, `runs show` reports `status=fail` with `failure_reason`. The worktree and logs are still on disk — go look. The per-node `Detail` and `Log` tabs in the UI are the fastest way to see what the agent actually tried.

Common failures:
- **`provider_prompt_probe fail`** — the provider CLI couldn't complete its probe. Check the provider's own config (`~/.codex/config.toml`, `~/.claude/settings.json`, etc.) for stale or invalid model/reasoning settings.
- **`missing llm.providers.X.backend`** — the graph references a provider that isn't declared in your run.yaml. Add `llm.providers.X.backend: cli` (or `api`).
- **`output contract: missing result.md`** — the agent finished without writing `result.md`. The Detail tab shows why; rerun with a clearer prompt, or drop the `outputs=` attribute on the agent node if the task genuinely doesn't produce a file.
- **`preflight aborted: declined provider CLI headless-risk warning`** — you forgot `--skip-cli-headless-warning` on a detached run.

## Do not

- Modify files under `workflows/quick-launch/` to tweak a single run. The package is shared. If you need a different prompt template or a non-trivial prepare step, author a custom graph and point `--graph` at it.
- Relaunch a failed run "just to see if it works now" before reading the failure. The logs are on disk; diagnose first.
- Invent new flags. The invocation template above is the whole interface — every flag in it is load-bearing. If something isn't covered, ask the user rather than guessing.
