---
name: starting-a-project
description: Use when bootstrapping a new project repository for Kilroy Attractor from a clean directory using existing spec, DoD, graph, and run config artifacts.
---

# Starting A Project

Use this skill to prepare a clean repo for `kilroy attractor validate/run`. This skill does not create requirements artifacts; it only wires and verifies them.

## Required Inputs

- Target repo path (clean directory)
- Existing project artifacts: spec (`*.md`), DoD (`*.md`), graph (`*.dot`), run config (`*.yaml` or `*.json`)
- Path to the Kilroy source repo (for canonical skill content and helper scripts)

## Workflow

1. Create the target directory and initialize git.
2. Add canonical skill mounts:
- `skills -> <kilroy-repo>/skills`
- `.claude/skills/<name> -> ../../skills/<name>`
- `.agents/skills/<name> -> ../../.claude/skills/<name>`
3. Copy project artifacts into the new repo (commonly under `demo/`): spec, DoD, graph, run config, and any graph-referenced companion files.
4. Update run config for the new repo:
- Set `repo.path` to the absolute target repo path.
- Ensure absolute helper paths (for example `modeldb` path or `scripts/start-cxdb.sh`) still resolve on this machine.
5. Create a base commit so Attractor can create run branches/worktrees.
6. Verify readiness:
- `kilroy attractor validate --graph <graph.dot>`
- Confirm required executables and env vars expected by config/setup exist.

## Guardrails

- Do not invent or regenerate spec/DoD/run config content in this skill.
- If any required artifact is missing or empty, stop and report the gap.
- Keep all setup deterministic and local-first (no production run side effects).

## Done Checklist

- Target directory is a git repo with at least one commit.
- Skill symlink layout resolves (`skills`, `.claude/skills`, `.agents/skills`).
- Spec, DoD, graph, and run config are present and non-empty.
- `repo.path` in run config points at the new repo.
- Graph validation returns no errors.
