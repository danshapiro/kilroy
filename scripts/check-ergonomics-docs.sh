#!/usr/bin/env bash
set -euo pipefail

if command -v rg >/dev/null 2>&1; then
  FIND=(rg -n)
else
  FIND=(grep -n)
fi

"${FIND[@]}" "attractor status --logs-root" README.md docs/strongdm/attractor/README.md docs/strongdm/attractor/reliability-troubleshooting.md
"${FIND[@]}" "attractor stop --logs-root" README.md docs/strongdm/attractor/reliability-troubleshooting.md
"${FIND[@]}" "runtime_policy:" README.md demo/rogue/run.yaml
"${FIND[@]}" "preflight:" README.md demo/rogue/run.yaml

if [[ -f demo/dttf/run.yaml ]]; then
  "${FIND[@]}" "runtime_policy:" demo/dttf/run.yaml
  "${FIND[@]}" "preflight:" demo/dttf/run.yaml
fi
