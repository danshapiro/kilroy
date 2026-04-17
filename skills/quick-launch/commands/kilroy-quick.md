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
- Tag the run with at least one specific `--label task=<slug>` so you can find it later with `--latest --label task=<slug>`.
- For any task longer than ~100 words or anything that would need `\n` escaping in JSON, write the prompt to a file first and use `--prompt-file <path>` instead of `--input '{"prompt":"..."}'`. This is the default, not the exception.
- After launching, print the `run_id` (and the `runs wait --latest --label` command) so the user can follow up.
- If the user asked you to wait for the result, use `kilroy attractor runs wait --latest --label task=<slug> --timeout <reasonable>` — don't poll by hand.
