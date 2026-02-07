#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."

# E2E suite: deterministic integration tests + DOT validation contract.

echo "== go test ./... =="
go test ./...

echo

echo "== validate contract dotfiles =="
DOTS=(
  "docs/strongdm/dot specs/consensus_task.dot"
  "docs/strongdm/dot specs/semport.dot"
  "docs/strongdm/dot specs/simple_example.dot"
  "research/green-test-vague.dot"
  "research/green-test-moderate.dot"
  "research/green-test-complex.dot"
  "research/refactor-test-vague.dot"
  "research/refactor-test-moderate.dot"
  "research/refactor-test-complex.dot"
)

# Build the local binary once for validation.
go build -o ./kilroy ./cmd/kilroy

fail=0
for f in "${DOTS[@]}"; do
  echo "-- kilroy attractor validate --graph '$f'"
  if ./kilroy attractor validate --graph "$f"; then
    :
  else
    echo "VALIDATE FAIL: $f" >&2
    fail=1
  fi
  echo
  
  # Avoid huge output and keep the loop responsive.
  sleep 0.1
done

if [[ $fail -ne 0 ]]; then
  echo "One or more dotfiles failed validation" >&2
  exit 1
fi

echo "All validations passed"
