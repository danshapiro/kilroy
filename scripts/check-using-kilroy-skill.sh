#!/usr/bin/env bash
set -euo pipefail

SKILL="skills/using-kilroy/SKILL.md"

if command -v rg >/dev/null 2>&1; then
  FIND=(rg -n)
else
  FIND=(grep -n)
fi

"${FIND[@]}" "attractor status --logs-root" "$SKILL"
"${FIND[@]}" "attractor stop --logs-root" "$SKILL"
"${FIND[@]}" "runtime_policy" "$SKILL"
"${FIND[@]}" "preflight.prompt_probes" "$SKILL"
