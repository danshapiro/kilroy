#!/usr/bin/env bash
set -euo pipefail

HTTP_BASE_URL="${KILROY_CXDB_HTTP_BASE_URL:-${CXDB_HTTP_BASE_URL:-http://127.0.0.1:9010}}"
BINARY_ADDR="${KILROY_CXDB_BINARY_ADDR:-${CXDB_BINARY_ADDR:-127.0.0.1:9009}}"
UI_ADDR="${KILROY_CXDB_UI_ADDR:-127.0.0.1:9020}"
CXDB_IMAGE="${KILROY_CXDB_IMAGE:-cxdb/cxdb:local}"
CONTAINER_NAME="${KILROY_CXDB_CONTAINER_NAME:-kilroy-cxdb}"
DATA_DIR="${KILROY_CXDB_DATA_DIR:-$HOME/.local/state/kilroy/cxdb}"
START_TIMEOUT_MS="${KILROY_CXDB_START_TIMEOUT_MS:-20000}"
POLL_INTERVAL_MS="${KILROY_CXDB_POLL_INTERVAL_MS:-250}"
ALLOW_EXTERNAL="${KILROY_CXDB_ALLOW_EXTERNAL:-0}"

split_host_port() {
  local raw="$1"
  local host=""
  local port=""
  if [[ "$raw" != *:* ]]; then
    echo "expected host:port, got: $raw" >&2
    return 1
  fi
  host="${raw%:*}"
  port="${raw##*:}"
  if [[ -z "$host" || -z "$port" ]]; then
    echo "expected host:port, got: $raw" >&2
    return 1
  fi
  if [[ "$host" == "localhost" ]]; then
    host="127.0.0.1"
  fi
  if ! [[ "$port" =~ ^[0-9]+$ ]]; then
    echo "invalid port in endpoint: $raw" >&2
    return 1
  fi
  printf '%s %s\n' "$host" "$port"
}

split_http_base() {
  local url="$1"
  local without_scheme="${url#http://}"
  without_scheme="${without_scheme#https://}"
  without_scheme="${without_scheme%%/*}"
  split_host_port "$without_scheme"
}

health_ok() {
  if curl -fsS -m 2 "$HTTP_BASE_URL/health" >/dev/null 2>&1; then
    return 0
  fi
  curl -fsS -m 2 "$HTTP_BASE_URL/healthz" >/dev/null 2>&1
}

container_running() {
  if ! command -v docker >/dev/null 2>&1; then
    return 1
  fi
  local state
  state="$(docker inspect -f '{{.State.Status}}' "$CONTAINER_NAME" 2>/dev/null || true)"
  [[ "$state" == "running" ]]
}

if ! [[ "$START_TIMEOUT_MS" =~ ^[0-9]+$ ]]; then
  echo "KILROY_CXDB_START_TIMEOUT_MS must be a non-negative integer" >&2
  exit 1
fi
if ! [[ "$POLL_INTERVAL_MS" =~ ^[0-9]+$ ]]; then
  echo "KILROY_CXDB_POLL_INTERVAL_MS must be a non-negative integer" >&2
  exit 1
fi
if [[ "$ALLOW_EXTERNAL" != "0" && "$ALLOW_EXTERNAL" != "1" ]]; then
  echo "KILROY_CXDB_ALLOW_EXTERNAL must be 0 or 1" >&2
  exit 1
fi

read -r BIN_HOST BIN_PORT < <(split_host_port "$BINARY_ADDR")
read -r HTTP_HOST HTTP_PORT < <(split_http_base "$HTTP_BASE_URL")
read -r UI_HOST UI_PORT < <(split_host_port "$UI_ADDR")

if health_ok; then
  if container_running; then
    echo "cxdb already healthy at $HTTP_BASE_URL (container=$CONTAINER_NAME)"
    exit 0
  fi
  if [[ "$ALLOW_EXTERNAL" == "1" ]]; then
    echo "cxdb already healthy at $HTTP_BASE_URL (external instance accepted)"
    exit 0
  fi
  cat >&2 <<EOF
cxdb endpoint $HTTP_BASE_URL is healthy, but docker container $CONTAINER_NAME is not running.
refusing to use an unmanaged endpoint (often a test daemon).
set KILROY_CXDB_ALLOW_EXTERNAL=1 to accept an external instance.
EOF
  exit 1
fi

if ! command -v docker >/dev/null 2>&1; then
  echo "docker is required to launch CXDB (missing docker executable)" >&2
  exit 1
fi
if ! command -v curl >/dev/null 2>&1; then
  echo "curl is required to probe CXDB health (missing curl executable)" >&2
  exit 1
fi

mkdir -p "$DATA_DIR"

container_state="$(docker inspect -f '{{.State.Status}}' "$CONTAINER_NAME" 2>/dev/null || true)"
case "$container_state" in
  running)
    echo "container $CONTAINER_NAME is already running; waiting for health..."
    ;;
  exited|created)
    docker start "$CONTAINER_NAME" >/dev/null
    ;;
  "")
    docker run -d \
      --name "$CONTAINER_NAME" \
      --restart unless-stopped \
      -p "${BIN_HOST}:${BIN_PORT}:${BIN_PORT}" \
      -p "${HTTP_HOST}:${HTTP_PORT}:${HTTP_PORT}" \
      -p "${UI_HOST}:${UI_PORT}:80" \
      -e "CXDB_BIND=0.0.0.0:${BIN_PORT}" \
      -e "CXDB_HTTP_BIND=0.0.0.0:${HTTP_PORT}" \
      -e "CXDB_DATA_DIR=/data" \
      -v "${DATA_DIR}:/data" \
      "$CXDB_IMAGE" >/dev/null
    ;;
  *)
    echo "unexpected docker state for $CONTAINER_NAME: $container_state" >&2
    exit 1
    ;;
esac

poll_seconds="0.250"
if (( POLL_INTERVAL_MS > 0 )); then
  poll_seconds="$(awk -v ms="$POLL_INTERVAL_MS" 'BEGIN { printf "%.3f", ms/1000 }')"
fi

# macOS BSD date does not support %N; fall back to python3.
now_ms() {
  local ms
  ms="$(date +%s%3N 2>/dev/null)"
  if [[ "$ms" =~ ^[0-9]+$ ]]; then
    echo "$ms"
  else
    python3 -c 'import time; print(int(time.time()*1000))'
  fi
}

deadline_ms=$(( $(now_ms) + START_TIMEOUT_MS ))
while (( $(now_ms) < deadline_ms )); do
  if health_ok; then
    echo "cxdb ready: http=$HTTP_BASE_URL binary=$BINARY_ADDR ui=http://$UI_ADDR container=$CONTAINER_NAME image=$CXDB_IMAGE"
    # When launched via attractor autostart, keep the process around until Kilroy exits.
    if [[ -n "${KILROY_RUN_ID:-}" ]]; then
      trap 'exit 0' TERM INT
      parent_pid="$PPID"
      while kill -0 "$parent_pid" >/dev/null 2>&1; do
        sleep 1
      done
    fi
    exit 0
  fi
  sleep "$poll_seconds"
done

echo "cxdb did not become healthy within ${START_TIMEOUT_MS}ms (container=$CONTAINER_NAME image=$CXDB_IMAGE)" >&2
docker logs --tail 80 "$CONTAINER_NAME" >&2 || true
exit 1
