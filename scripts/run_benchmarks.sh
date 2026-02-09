#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."
ROOT="$PWD"

# Benchmark runs can take hours. This script is meant to be interruptible without
# losing the location of partial artifacts (so you can `kilroy attractor resume`).
CURRENT_BENCH_DOT=""
CURRENT_BENCH_WORKDIR=""
CURRENT_BENCH_LOGS_ROOT=""
CURRENT_BENCH_PID=""

kill_pid_best_effort() {
  local pid="$1"
  [[ -z "$pid" ]] && return 0

  if ! kill -0 "$pid" >/dev/null 2>&1; then
    return 0
  fi

  kill -TERM "$pid" >/dev/null 2>&1 || true
  for _ in {1..50}; do
    if ! kill -0 "$pid" >/dev/null 2>&1; then
      return 0
    fi
    sleep 0.1
  done
  kill -KILL "$pid" >/dev/null 2>&1 || true
}

cleanup() {
  # Best-effort: if we're interrupted mid-run, kill the active Kilroy process.
  kill_pid_best_effort "${CURRENT_BENCH_PID:-}"
}

on_signal() {
  local sig="$1"

  echo "BENCH INTERRUPTED ($sig)" >&2
  if [[ -n "${CURRENT_BENCH_WORKDIR:-}" ]]; then
    {
      echo "interrupted=1"
      echo "signal=$sig"
      echo "dot=$CURRENT_BENCH_DOT"
      echo "logs_root=$CURRENT_BENCH_LOGS_ROOT"
      echo "resume_cmd=./kilroy attractor resume --logs-root $CURRENT_BENCH_LOGS_ROOT"
    } > "$CURRENT_BENCH_WORKDIR/interrupted.txt" || true

    # If the run didn't reach normal completion, ensure there's at least an
    # exit_code breadcrumb.
    if [[ ! -f "$CURRENT_BENCH_WORKDIR/exit_code.txt" ]]; then
      echo "exit_code=interrupted" > "$CURRENT_BENCH_WORKDIR/exit_code.txt" || true
    fi
  fi

  cleanup

  case "$sig" in
    SIGINT) exit 130 ;;
    SIGTERM) exit 143 ;;
    *) exit 1 ;;
  esac
}

trap cleanup EXIT
trap 'on_signal SIGINT' INT
trap 'on_signal SIGTERM' TERM

# Full agent capability benchmarks (current: refactor trio).
# Runs with real LLM providers (as specified by the DOT graph) and a real CXDB.
#
# IMPORTANT:
# - The DOT file is the contract. This script MUST NOT silently change providers/models.
# - If you want to override the graph's model_stylesheet, you must opt in via env vars.

BENCH_DOTS=(
  "research/refactor-test-vague.dot"
  "research/refactor-test-moderate.dot"
  "research/refactor-test-complex.dot"
)

ROOT_OUT="${KILROY_BENCH_OUT:-$PWD/.ai/benchmarks}"
mkdir -p "$ROOT_OUT"
STAMP="$(date -u +%Y%m%dT%H%M%SZ)"
OUT_DIR="$ROOT_OUT/$STAMP"
mkdir -p "$OUT_DIR"

echo "bench_out=$OUT_DIR"

# Build Kilroy once.
go build -o ./kilroy ./cmd/kilroy

# Default pinned OpenRouter model info snapshot shipped in-repo.
PINNED_CATALOG_DEFAULT="$PWD/internal/attractor/modeldb/pinned/openrouter_models.json"
PINNED_CATALOG="${KILROY_BENCH_OPENROUTER_MODEL_INFO_PATH:-${KILROY_BENCH_LITELLM_CATALOG_PATH:-$PINNED_CATALOG_DEFAULT}}"
if [[ ! -f "$PINNED_CATALOG" ]]; then
  echo "missing OpenRouter pinned model info snapshot: $PINNED_CATALOG" >&2
  exit 1
fi

# Real CXDB is required; no local fake server bootstrap.
CXDB_URL="${KILROY_BENCH_CXDB_HTTP_BASE_URL:-${CXDB_HTTP_BASE_URL:-}}"
CXDB_BIN_ADDR="${KILROY_BENCH_CXDB_BINARY_ADDR:-${CXDB_BINARY_ADDR:-}}"
if [[ -z "${CXDB_URL:-}" || -z "${CXDB_BIN_ADDR:-}" ]]; then
  cat >&2 <<'EOF'
missing real CXDB endpoints.
Set both:
  KILROY_BENCH_CXDB_HTTP_BASE_URL=http://127.0.0.1:9010
  KILROY_BENCH_CXDB_BINARY_ADDR=127.0.0.1:9009
or:
  CXDB_HTTP_BASE_URL=...
  CXDB_BINARY_ADDR=...
EOF
  exit 1
fi

if ! curl -s -S -m 2 "$CXDB_URL/health" >/dev/null 2>&1; then
  if ! curl -s -S -m 2 "$CXDB_URL/healthz" >/dev/null 2>&1; then
    echo "real CXDB health check failed for $CXDB_URL (/health and /healthz)" >&2
    exit 1
  fi
fi

echo "cxdb_url=$CXDB_URL"
echo "cxdb_bin_addr=$CXDB_BIN_ADDR"

run_one() {
  local dot="$1"
  local name
  name="$(basename "$dot" .dot)"
  local workdir
  workdir="$OUT_DIR/$name"
  mkdir -p "$workdir"

  # Snapshot the graph used for this run into the benchmark output directory.
  local graph
  graph="$workdir/graph.dot"
  cp "$dot" "$workdir/graph.original.dot"
  cp "$dot" "$graph"

  # Optional, explicit override: replace the graph's model_stylesheet.
  # - Preferred: provide a file with the stylesheet content (one rule per line).
  # - Alternative: set KILROY_BENCH_OVERRIDE_PROVIDER=openai to use an OpenAI preset.
  if [[ -n "${KILROY_BENCH_MODEL_STYLESHEET_FILE:-}" ]]; then
    if [[ ! -f "${KILROY_BENCH_MODEL_STYLESHEET_FILE}" ]]; then
      echo "KILROY_BENCH_MODEL_STYLESHEET_FILE does not exist: ${KILROY_BENCH_MODEL_STYLESHEET_FILE}" >&2
      exit 1
    fi
    echo "WARNING: overriding model_stylesheet from file: ${KILROY_BENCH_MODEL_STYLESHEET_FILE}"
    python3 - "$graph" "${KILROY_BENCH_MODEL_STYLESHEET_FILE}" <<'PY' || return 1
import pathlib, re, sys
graph_path = pathlib.Path(sys.argv[1])
stylesheet_path = pathlib.Path(sys.argv[2])
src = graph_path.read_text()
stylesheet = stylesheet_path.read_text()

lines = [ln.strip() for ln in stylesheet.splitlines() if ln.strip()]
indented = "\n".join("            " + ln for ln in lines)
replacement = 'model_stylesheet="\\n' + indented + '\\n        "'

out, n = re.subn(r'model_stylesheet\s*=\s*".*?"', replacement, src, count=1, flags=re.S)
if n != 1:
    raise SystemExit(f"expected to replace exactly 1 model_stylesheet, replaced {n}")
graph_path.write_text(out)
PY
  elif [[ -n "${KILROY_BENCH_OVERRIDE_PROVIDER:-}" ]]; then
    if [[ "${KILROY_BENCH_OVERRIDE_PROVIDER}" != "openai" ]]; then
      echo "KILROY_BENCH_OVERRIDE_PROVIDER is set to an unsupported value: ${KILROY_BENCH_OVERRIDE_PROVIDER} (supported: openai)" >&2
      exit 1
    fi
    base_model="${KILROY_BENCH_OVERRIDE_MODEL_BASE:-gpt-5.2-codex}"
    hard_model="${KILROY_BENCH_OVERRIDE_MODEL_HARD:-$base_model}"
    verify_model="${KILROY_BENCH_OVERRIDE_MODEL_VERIFY:-$base_model}"
    review_model="${KILROY_BENCH_OVERRIDE_MODEL_REVIEW:-$hard_model}"
    echo "WARNING: overriding model_stylesheet preset: provider=openai base=$base_model hard=$hard_model verify=$verify_model review=$review_model"
    python3 - "$graph" "$base_model" "$hard_model" "$verify_model" "$review_model" <<'PY' || return 1
import pathlib, re, sys
graph_path = pathlib.Path(sys.argv[1])
base_model, hard_model, verify_model, review_model = sys.argv[2:6]
src = graph_path.read_text()

stylesheet = f"""
* {{ llm_model: {base_model}; llm_provider: openai; reasoning_effort: medium; }}
.hard {{ llm_model: {hard_model}; llm_provider: openai; reasoning_effort: high; }}
.verify {{ llm_model: {verify_model}; llm_provider: openai; reasoning_effort: medium; }}
.review {{ llm_model: {review_model}; llm_provider: openai; reasoning_effort: high; }}
""".strip()

lines = [ln.strip() for ln in stylesheet.splitlines() if ln.strip()]
indented = "\n".join("            " + ln for ln in lines)
replacement = 'model_stylesheet="\\n' + indented + '\\n        "'

out, n = re.subn(r'model_stylesheet\s*=\s*".*?"', replacement, src, count=1, flags=re.S)
if n != 1:
    raise SystemExit(f"expected to replace exactly 1 model_stylesheet, replaced {n}")
graph_path.write_text(out)
PY
  else
    echo "NOTE: using graph model_stylesheet as-is (no overrides)."
  fi

  # Fresh git repo to operate on.
  local repo="$workdir/repo"
  mkdir -p "$repo"
  # Seed the benchmark repo with in-repo spec fixtures used by some graphs (e.g. demo/dttf/dttf-v1.md).
  # This keeps runs isolated (fresh repo) while still allowing pipelines to reference stable spec inputs.
  if [[ -d "$ROOT/demo" ]]; then
    cp -R "$ROOT/demo" "$repo/"
  fi
  (cd "$repo" && git init -q && git config user.name tester && git config user.email tester@example.com && echo "hello" > README.md && git add -A && git commit -qm init)

  local providers
  providers="$(python3 - "$graph" <<'PY'
import pathlib, re, sys
path = pathlib.Path(sys.argv[1])
txt = path.read_text()
providers = set()
for m in re.finditer(r'llm_provider\s*:\s*([a-zA-Z0-9_-]+)\s*;', txt):
    p = m.group(1).strip().lower()
    if p == "gemini":
        p = "google"
    providers.add(p)
for p in sorted(providers):
    print(p)
PY
)" || return 1

  # Fail fast if the graph references an unsupported provider.
  while read -r p; do
    [[ -z "$p" ]] && continue
    case "$p" in
      openai|anthropic|google) : ;;
      *) echo "unsupported provider in graph: $p" >&2; exit 1 ;;
    esac
  done <<< "$providers"

  local cfg="$workdir/run.yaml"
  cat > "$cfg" <<YAML
version: 1
repo:
  path: $repo
cxdb:
  binary_addr: $CXDB_BIN_ADDR
  http_base_url: $CXDB_URL
modeldb:
  openrouter_model_info_path: $PINNED_CATALOG
  openrouter_model_info_update_policy: ${KILROY_BENCH_OPENROUTER_UPDATE_POLICY:-${KILROY_BENCH_LITELLM_UPDATE_POLICY:-pinned}}
git:
  run_branch_prefix: attractor/run
  commit_per_node: true
llm:
  providers:
YAML
  # By default, configure ONLY the providers referenced by the graph (no implicit substitutions).
  # If you want engine-level API failover, opt in to including all providers.
  local include_all="${KILROY_BENCH_INCLUDE_ALL_PROVIDERS:-0}"
  if [[ "$include_all" == "1" ]]; then
    echo "NOTE: benchmark run config includes failover-capable providers: openai, anthropic, google"
    providers=$'openai\nanthropic\ngoogle'
  else
    echo "NOTE: benchmark run config includes only providers referenced in the graph (strict mode)"
  fi
  local backend_default="${KILROY_BENCH_BACKEND_DEFAULT:-api}"
  while read -r p; do
    [[ -z "$p" ]] && continue
    local backend="$backend_default"
    case "$p" in
      openai) backend="${KILROY_BENCH_BACKEND_OPENAI:-$backend}" ;;
      anthropic) backend="${KILROY_BENCH_BACKEND_ANTHROPIC:-$backend}" ;;
      google) backend="${KILROY_BENCH_BACKEND_GOOGLE:-$backend}" ;;
    esac
    cat >> "$cfg" <<YAML
    $p:
      backend: $backend
YAML
  done <<< "$providers"

  local run_id="bench-$name-$STAMP"
  local logs_root="$workdir/logs"
  mkdir -p "$logs_root"

  echo "== RUN $dot =="
  echo "graph=$graph"
  echo "run_id=$run_id"
  echo "logs_root=$logs_root"
  echo "run_out=$workdir/run.out"

  # Optional: overall per-run timeout (defaults to none; CLI runs may take hours).
  local run_timeout="${KILROY_BENCH_RUN_TIMEOUT:-0}"
  set +e
  if [[ "$run_timeout" == "0" ]]; then
    CURRENT_BENCH_DOT="$dot"
    CURRENT_BENCH_WORKDIR="$workdir"
    CURRENT_BENCH_LOGS_ROOT="$logs_root"
    : > "$workdir/run.out"
    ./kilroy attractor run --graph "$graph" --config "$cfg" --run-id "$run_id" --logs-root "$logs_root" >"$workdir/run.out" 2>&1 &
    CURRENT_BENCH_PID=$!
    wait "$CURRENT_BENCH_PID"
    local ec=$?
  else
    CURRENT_BENCH_DOT="$dot"
    CURRENT_BENCH_WORKDIR="$workdir"
    CURRENT_BENCH_LOGS_ROOT="$logs_root"
    : > "$workdir/run.out"
    timeout --preserve-status --signal=SIGTERM "$run_timeout" ./kilroy attractor run --graph "$graph" --config "$cfg" --run-id "$run_id" --logs-root "$logs_root" >"$workdir/run.out" 2>&1 &
    CURRENT_BENCH_PID=$!
    wait "$CURRENT_BENCH_PID"
    local ec=$?
  fi
  CURRENT_BENCH_PID=""
  set -e

  echo "exit_code=$ec" | tee "$workdir/exit_code.txt" >/dev/null
  echo "-- run.out (tail) --"
  tail -n 25 "$workdir/run.out" || true
  echo
  return $ec
}

fail=0
for dot in "${BENCH_DOTS[@]}"; do
  if run_one "$dot"; then
    :
  else
    echo "BENCH FAIL: $dot" >&2
    fail=1
  fi
  # Keep the system responsive.
  sleep 0.5
done

if [[ $fail -ne 0 ]]; then
  echo "One or more benchmarks failed" >&2
  exit 1
fi

echo "All benchmarks finished successfully"
