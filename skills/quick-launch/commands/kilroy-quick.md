---
description: Delegate a one-shot task to a Kilroy agent (fire-and-forget, tagged, tmux-backgrounded). Task description follows.
disable-model-invocation: true
---

Invoke the `quick-launch` skill at `skills/quick-launch/SKILL.md` and follow it exactly as written.

The task to delegate: $ARGUMENTS

If the user did not supply a task description, ask them what they want the Kilroy agent to do before launching anything. Do not invent a task.

Key reminders from the skill (do not skip):

- Use the invocation template in Step 3 verbatim. Every flag is load-bearing.
- The workflow package lives at `~/.local/share/kilroy/workflows/quick-launch`.
- Tag the run with at least one specific `--label` so you can find it later.
- Pass the task via `--input '{"prompt":"..."}'`. If the user references a file as context, pass it via `"context_file":"<abs-path>"` in the same JSON.
- After launching, print the `run_id` so the user can follow up.
- Do not wait for the run synchronously — it is detached on purpose.
