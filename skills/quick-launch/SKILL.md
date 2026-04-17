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

## Step 0: Preflight checklist

Before invoking the skill, confirm:

- `kilroy` is on PATH. (`which kilroy` — if missing, the skill is not installed; tell the user and stop.)
- `~/.local/share/kilroy/workflows/quick-launch/` exists. This is the canonical workflow package path; all invocations below reference it directly.
- Current working directory is a git repo. The engine auto-detects and creates an isolated run branch + worktree, so the user's source tree is never touched. If cwd is not a git repo, the run still works but isolation degrades to plain-directory mode — warn the user.

## Step 1: Pick the agent

The quick-launch package ships three graph variants:

- `graph.dot` — claude (default)
- `graph.codex.dot` — OpenAI codex CLI
- `graph.gemini.dot` — gemini CLI

The agent is encoded in the graph file. Pick one with `--graph` when launching; otherwise the package loader uses `graph.dot`.

## Step 2: Prepare the task

All inputs are written to `.kilroy/INPUT.md` in the worktree at run start, with one `## <key>` section per input. The agent reads that file to find the task and any context.

Two ways to pass the task (pick one):

**Short tasks — inline JSON:**
```bash
--input '{"prompt":"Investigate X and summarize findings"}'
```

**Longer or multi-line tasks — write the prompt to a file and use `--prompt-file`:**
```bash
--prompt-file /abs/path/to/request.md
```
This reads the file contents verbatim into the `prompt` input. No JSON escaping, no quoting nightmares. **Strongly prefer this when the task is more than ~100 words or contains anything that would be painful to inline: multi-paragraph briefings, code blocks, markdown tables, lists with embedded quotes.** The extra step of writing a file is worth it the moment you reach for `\n` escapes.

Optional extras (pass alongside either form via `--input '{"...":"..."}`):

- `context_file` — absolute path to a file for background context. The agent reads it from its original location; no copy happens. Use when the context is a document that already exists on disk (briefing, design doc, log file). Example: `--input '{"context_file":"/abs/path/briefing.md"}'` combined with `--prompt-file`.
- `context` — an inline string for background context. Use when the context is short enough to inline but you want to keep it separate from the task description.

If the task is short and self-contained (a few sentences), skip the context entirely.

## Step 3: Launch

From the user's current working directory (which should be a git repo — see Step 0):

```bash
kilroy attractor run --detach --tmux \
  --package ~/.local/share/kilroy/workflows/quick-launch \
  --label task=<SHORT_SLUG> \
  --prompt-file <PROMPT_FILE_PATH>
```

Or the inline-JSON form for a short prompt:
```bash
kilroy attractor run --detach --tmux \
  --package ~/.local/share/kilroy/workflows/quick-launch \
  --label task=<SHORT_SLUG> \
  --input '{"prompt":"<SHORT_TASK>"}'
```

That is the whole invocation. No `--config` needed — when no run.yaml is supplied, kilroy auto-builds a default config for cwd and auto-detects installed provider CLIs. No `--no-cxdb` or `--skip-cli-headless-warning` needed either — those are applied automatically when there's no config and stdin isn't interactive. The `workflow=quick-launch` label is added automatically by the package's `workflow.toml`.

Optional additions:
- `--graph ~/.local/share/kilroy/workflows/quick-launch/graph.codex.dot` to pick codex. Swap `.gemini.dot` for gemini. Omit for claude.
- Additional `--label KEY=VALUE` flags to tag owner, ticket id, run group, etc. The `task=` tag is the minimum — use a specific slug so `runs show --latest --label task=<slug>` finds exactly this run later.
- `--workspace <dir>` to run against a different directory than cwd. Use this when the user wants the run to operate against a repo they're not currently in.

On success the command prints `run_id=<ulid>` and `logs_root=...` and returns immediately. The run continues in a detached tmux session. Print the `run_id` (or the `--latest --label task=<slug>` form) to the user so they can follow up.

## Step 4: Wait for the run

The fastest way — block until the run reaches a terminal state, then return:

```bash
kilroy attractor runs wait --latest --label task=<SHORT_SLUG> --timeout 10m
```

`runs wait` polls the run DB every ~2s (configurable with `--interval`), prints each status transition to stderr, and exits 0 on `success` / 1 on `fail`/`canceled` / 2 on `--timeout` expiry. Use this whenever you want synchronous behavior on top of the detached run.

If you don't want to block, check status on demand instead:

```bash
kilroy attractor runs show --latest --label task=<SHORT_SLUG>
```

or list all runs matching a tag:

```bash
kilroy attractor runs list --label task=<SHORT_SLUG>
```

Show output accepts `--json` for machine-readable detail. Interesting fields for a caller:
- `status` — `running`, `success`, `fail`, `canceled`
- `worktree_dir` — where the agent worked; still on disk with the final git state
- `outputs[].path` — absolute path to each collected output file (e.g. `result.md`)

## Step 5: Retrieve result.md

Once status is `success`:

```bash
kilroy attractor runs show --latest --label task=<SHORT_SLUG> --print result.md
```

(or use a run id / prefix instead of `--latest`). That streams the file straight to stdout — pipe it, redirect it, or read it into your current conversation.

If the agent wrote additional files you care about, list them:

```bash
kilroy attractor runs show 01KP646Y --outputs
```

…and `--print <filename>` any of them. Only files declared in `outputs=` on the graph or the node are collected into `logs_root/outputs/` — everything else stays in the worktree (`worktree_dir` from `runs show`).

## Minimal run.yaml (only if you need it)

You almost never need a run.yaml for quick-launch — the default behavior handles it. Supply one only when you need non-default settings: specific model selection beyond what the graph's stylesheet declares, a remote cxdb, custom runtime policy, or a non-cwd workspace via config instead of `--workspace`. A working minimum:

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
