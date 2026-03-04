#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PINNED_PATH="${ROOT_DIR}/internal/attractor/modeldb/pinned/openrouter_models.json"
PROVIDER_REGEX='^(openai|anthropic|google)/'
LIVE_URL="https://openrouter.ai/api/v1/models"
DRY_RUN=0

usage() {
  cat <<'USAGE'
Usage:
  scripts/refresh-openrouter-provider-catalog.sh [--dry-run] [--providers-regex <regex>] [--pinned <path>]

Description:
  Refreshes provider entries in the pinned OpenRouter catalog from live data.
  By default, refreshes:
    - openai/*
    - anthropic/*
    - google/*

Options:
  --dry-run                Show planned changes, do not write file
  --providers-regex <re>   Provider ID regex (default: ^(openai|anthropic|google)/)
  --pinned <path>          Path to pinned catalog JSON file
USAGE
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --dry-run)
      DRY_RUN=1
      shift
      ;;
    --providers-regex)
      PROVIDER_REGEX="${2:-}"
      if [[ -z "${PROVIDER_REGEX}" ]]; then
        echo "error: --providers-regex requires a value" >&2
        exit 1
      fi
      shift 2
      ;;
    --pinned)
      PINNED_PATH="${2:-}"
      if [[ -z "${PINNED_PATH}" ]]; then
        echo "error: --pinned requires a value" >&2
        exit 1
      fi
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "error: unknown argument: $1" >&2
      usage >&2
      exit 1
      ;;
  esac
done

if [[ ! -f "${PINNED_PATH}" ]]; then
  echo "error: pinned catalog not found: ${PINNED_PATH}" >&2
  exit 1
fi

if ! command -v jq >/dev/null 2>&1; then
  echo "error: jq is required" >&2
  exit 1
fi

if ! command -v curl >/dev/null 2>&1; then
  echo "error: curl is required" >&2
  exit 1
fi

tmp_live="$(mktemp)"
tmp_new="$(mktemp)"
tmp_old_ids="$(mktemp)"
tmp_live_ids="$(mktemp)"
tmp_added="$(mktemp)"
tmp_removed="$(mktemp)"
trap 'rm -f "$tmp_live" "$tmp_new" "$tmp_old_ids" "$tmp_live_ids" "$tmp_added" "$tmp_removed"' EXIT

curl -fsSL "${LIVE_URL}" > "${tmp_live}"

jq -e '.data and (.data | type == "array")' "${tmp_live}" >/dev/null
jq -e '.data and (.data | type == "array")' "${PINNED_PATH}" >/dev/null

jq -r --arg re "${PROVIDER_REGEX}" '.data[] | select(.id | test($re)) | .id' "${PINNED_PATH}" | sort > "${tmp_old_ids}"
jq -r --arg re "${PROVIDER_REGEX}" '.data[] | select(.id | test($re)) | .id' "${tmp_live}" | sort > "${tmp_live_ids}"

comm -13 "${tmp_old_ids}" "${tmp_live_ids}" > "${tmp_added}"
comm -23 "${tmp_old_ids}" "${tmp_live_ids}" > "${tmp_removed}"

jq --slurpfile live "${tmp_live}" --arg re "${PROVIDER_REGEX}" '
  ($live[0].data
    | map(select(.id | test($re)))
    | map({key: .id, value: .})
    | from_entries) as $freshByID
  | ($freshByID | keys) as $freshIDs
  | (.data
      | map(
          if (.id | test($re)) then
            ($freshByID[.id] // empty)
          else
            .
          end
        )) as $replaced
  | ($replaced
      | map(.id)
      | map({key: ., value: true})
      | from_entries) as $present
  | ($freshIDs
      | map(select($present[.] | not) | $freshByID[.])) as $missing
  | .data = ($replaced + $missing)
' "${PINNED_PATH}" > "${tmp_new}"

dups="$(jq -r '.data[].id' "${tmp_new}" | sort | uniq -d)"
if [[ -n "${dups}" ]]; then
  echo "error: duplicate model IDs introduced:" >&2
  echo "${dups}" >&2
  exit 1
fi

old_total="$(jq '.data | length' "${PINNED_PATH}")"
new_total="$(jq '.data | length' "${tmp_new}")"
old_provider_total="$(wc -l < "${tmp_old_ids}" | tr -d ' ')"
new_provider_total="$(wc -l < "${tmp_live_ids}" | tr -d ' ')"

echo "Pinned total models: ${old_total} -> ${new_total}"
echo "Target-provider models: ${old_provider_total} -> ${new_provider_total}"
echo "Providers regex: ${PROVIDER_REGEX}"
echo

echo "Added IDs:"
if [[ -s "${tmp_added}" ]]; then
  cat "${tmp_added}"
else
  echo "(none)"
fi
echo

echo "Removed IDs:"
if [[ -s "${tmp_removed}" ]]; then
  cat "${tmp_removed}"
else
  echo "(none)"
fi
echo

if cmp -s "${PINNED_PATH}" "${tmp_new}"; then
  echo "No changes detected."
  exit 0
fi

if [[ "${DRY_RUN}" == "1" ]]; then
  echo "Dry run only, no file was written."
  exit 0
fi

mv "${tmp_new}" "${PINNED_PATH}"
echo "Updated ${PINNED_PATH}"
