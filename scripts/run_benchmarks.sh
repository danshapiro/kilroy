#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."

# Full agent capability benchmarks (current: refactor trio).
# Runs with real LLM providers (as specified by the DOT graph) and a local fake CXDB.
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

# Default pinned LiteLLM catalog shipped in-repo.
PINNED_CATALOG_DEFAULT="$PWD/internal/attractor/modeldb/pinned/model_prices_and_context_window.json"
PINNED_CATALOG="${KILROY_BENCH_LITELLM_CATALOG_PATH:-$PINNED_CATALOG_DEFAULT}"
if [[ ! -f "$PINNED_CATALOG" ]]; then
  echo "missing LiteLLM pinned catalog: $PINNED_CATALOG" >&2
  exit 1
fi

# Start a fake CXDB server (Go) in the background.
CXDB_BIN="$OUT_DIR/cxdb_fake"
cat > "$OUT_DIR/cxdb_fake.go" <<'GO'
package main

import (
  "encoding/binary"
  "encoding/json"
  "fmt"
  "io"
  "log"
  "net"
  "net/http"
  "strconv"
  "strings"
  "sync"
  "sync/atomic"

  "github.com/zeebo/blake3"
)

type srvState struct {
  mu sync.Mutex
  nextContextID int
  nextTurnID int
  nextSessionID atomic.Uint64
  contexts map[string]*ctxState
  blobs map[[32]byte][]byte
}

type ctxState struct {
  ContextID string
  HeadTurnID string
  HeadDepth int
}

type binFrameHeader struct {
  Len uint32
  MsgType uint16
  Flags uint16
  ReqID uint64
}

func readBinFrame(r io.Reader) (binFrameHeader, []byte, error) {
  var hdrBuf [16]byte
  if _, err := io.ReadFull(r, hdrBuf[:]); err != nil {
    return binFrameHeader{}, nil, err
  }
  h := binFrameHeader{
    Len: binary.LittleEndian.Uint32(hdrBuf[0:4]),
    MsgType: binary.LittleEndian.Uint16(hdrBuf[4:6]),
    Flags: binary.LittleEndian.Uint16(hdrBuf[6:8]),
    ReqID: binary.LittleEndian.Uint64(hdrBuf[8:16]),
  }
  payload := make([]byte, int(h.Len))
  if _, err := io.ReadFull(r, payload); err != nil {
    return binFrameHeader{}, nil, err
  }
  return h, payload, nil
}

func writeBinFrame(w io.Writer, msgType uint16, flags uint16, reqID uint64, payload []byte) error {
  var hdrBuf [16]byte
  binary.LittleEndian.PutUint32(hdrBuf[0:4], uint32(len(payload)))
  binary.LittleEndian.PutUint16(hdrBuf[4:6], msgType)
  binary.LittleEndian.PutUint16(hdrBuf[6:8], flags)
  binary.LittleEndian.PutUint64(hdrBuf[8:16], reqID)
  if _, err := w.Write(hdrBuf[:]); err != nil {
    return err
  }
  if len(payload) > 0 {
    _, err := w.Write(payload)
    return err
  }
  return nil
}

func writeBinError(w io.Writer, reqID uint64, code uint32, detail string) error {
  detailBytes := []byte(detail)
  payload := make([]byte, 8+len(detailBytes))
  binary.LittleEndian.PutUint32(payload[0:4], code)
  binary.LittleEndian.PutUint32(payload[4:8], uint32(len(detailBytes)))
  copy(payload[8:], detailBytes)
  return writeBinFrame(w, 255, 0, reqID, payload)
}

func serveBinary(ln net.Listener, st *srvState) {
  for {
    conn, err := ln.Accept()
    if err != nil {
      return
    }
    go handleBinaryConn(conn, st)
  }
}

func handleBinaryConn(conn net.Conn, st *srvState) {
  defer func() { _ = conn.Close() }()
  for {
    h, payload, err := readBinFrame(conn)
    if err != nil {
      return
    }
    switch h.MsgType {
    case 1: // HELLO
      if len(payload) < 8 {
        _ = writeBinError(conn, h.ReqID, 400, "hello: short payload")
        continue
      }
      ver := binary.LittleEndian.Uint32(payload[0:4])
      tagLen := binary.LittleEndian.Uint32(payload[4:8])
      if ver != 1 {
        _ = writeBinError(conn, h.ReqID, 422, fmt.Sprintf("hello: unsupported protocol_version=%d", ver))
        continue
      }
      if int(8+tagLen) > len(payload) {
        _ = writeBinError(conn, h.ReqID, 400, "hello: client_tag_len out of range")
        continue
      }
      _ = payload[8 : 8+tagLen] // ignore client tag

      sessionID := st.nextSessionID.Add(1)
      serverTag := []byte("cxdb-fake")

      resp := make([]byte, 4+8+4+len(serverTag))
      binary.LittleEndian.PutUint32(resp[0:4], 1)
      binary.LittleEndian.PutUint64(resp[4:12], sessionID)
      binary.LittleEndian.PutUint32(resp[12:16], uint32(len(serverTag)))
      copy(resp[16:], serverTag)
      _ = writeBinFrame(conn, 1, 0, h.ReqID, resp)

    case 11: // PUT_BLOB
      if len(payload) < 36 {
        _ = writeBinError(conn, h.ReqID, 400, "put_blob: short payload")
        continue
      }
      var wantHash [32]byte
      copy(wantHash[:], payload[0:32])
      rawLen := binary.LittleEndian.Uint32(payload[32:36])
      if int(36+rawLen) != len(payload) {
        _ = writeBinError(conn, h.ReqID, 400, "put_blob: len mismatch")
        continue
      }
      raw := payload[36:]
      gotHash := blake3.Sum256(raw)
      if gotHash != wantHash {
        _ = writeBinError(conn, h.ReqID, 409, "put_blob: hash mismatch")
        continue
      }

      st.mu.Lock()
      _, existed := st.blobs[wantHash]
      if !existed {
        st.blobs[wantHash] = append([]byte{}, raw...)
      }
      st.mu.Unlock()

      resp := make([]byte, 33)
      copy(resp[0:32], wantHash[:])
      if existed {
        resp[32] = 0
      } else {
        resp[32] = 1
      }
      _ = writeBinFrame(conn, 11, 0, h.ReqID, resp)

    default:
      _ = writeBinError(conn, h.ReqID, 400, fmt.Sprintf("unsupported msg_type=%d", h.MsgType))
    }
  }
}

func main() {
  st := &srvState{
    nextContextID: 1,
    nextTurnID: 1,
    contexts: map[string]*ctxState{},
    blobs: map[[32]byte][]byte{},
  }

  mux := http.NewServeMux()
  mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
  mux.HandleFunc("/v1/registry/bundles/", func(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodPut { w.WriteHeader(http.StatusMethodNotAllowed); return }
    w.WriteHeader(http.StatusCreated)
  })

  handleContextCreate := func(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodPost { w.WriteHeader(http.StatusMethodNotAllowed); return }
    baseTurnID := "0"
    b, _ := io.ReadAll(r.Body)
    _ = r.Body.Close()
    if strings.TrimSpace(string(b)) != "" {
      var req map[string]any
      _ = json.Unmarshal(b, &req)
      if v, ok := req["base_turn_id"]; ok {
        if s, ok := v.(string); ok {
          s = strings.TrimSpace(s)
          if s != "" { baseTurnID = s }
        }
      }
    }

    st.mu.Lock()
    id := strconv.Itoa(st.nextContextID)
    st.nextContextID++
    st.contexts[id] = &ctxState{ContextID: id, HeadTurnID: baseTurnID, HeadDepth: 0}
    ci := *st.contexts[id]
    st.mu.Unlock()

    _ = json.NewEncoder(w).Encode(map[string]any{
      "context_id": ci.ContextID,
      "head_turn_id": ci.HeadTurnID,
      "head_depth": ci.HeadDepth,
    })
  }
  mux.HandleFunc("/v1/contexts/create", handleContextCreate)
  mux.HandleFunc("/v1/contexts/fork", handleContextCreate)
  mux.HandleFunc("/v1/contexts", func(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodPost { w.WriteHeader(http.StatusMethodNotAllowed); return }
    handleContextCreate(w, r)
  })
  mux.HandleFunc("/v1/contexts/", func(w http.ResponseWriter, r *http.Request) {
    rest := strings.TrimPrefix(r.URL.Path, "/v1/contexts/")
    parts := strings.Split(rest, "/")
    if len(parts) < 2 { w.WriteHeader(http.StatusNotFound); return }
    ctxID := strings.TrimSpace(parts[0])
    if ctxID == "" { w.WriteHeader(http.StatusNotFound); return }
    if r.Method != http.MethodPost { w.WriteHeader(http.StatusMethodNotAllowed); return }
    if parts[1] != "append" && parts[1] != "turns" { w.WriteHeader(http.StatusNotFound); return }

    st.mu.Lock()
    ci := st.contexts[ctxID]
    if ci == nil {
      st.mu.Unlock(); w.WriteHeader(http.StatusNotFound); return
    }
    turnID := strconv.Itoa(st.nextTurnID)
    st.nextTurnID++
    ci.HeadDepth++
    ci.HeadTurnID = turnID
    depth := ci.HeadDepth
    st.mu.Unlock()

    _ = json.NewEncoder(w).Encode(map[string]any{
      "context_id": ctxID,
      "turn_id": turnID,
      "depth": depth,
      "payload_hash": "h" + turnID,
      "content_hash": "h" + turnID,
    })
  })

  httpLn, err := net.Listen("tcp", "127.0.0.1:0")
  if err != nil { log.Fatal(err) }
  binLn, err := net.Listen("tcp", "127.0.0.1:0")
  if err != nil { log.Fatal(err) }

  // Print base URLs for shell parsing.
  fmt.Println("http://" + httpLn.Addr().String())
  fmt.Println(binLn.Addr().String())

  go func() {
    if err := http.Serve(httpLn, mux); err != nil {
      log.Fatal(err)
    }
  }()
  go serveBinary(binLn, st)

  select {}
}
GO

go build -o "$CXDB_BIN" "$OUT_DIR/cxdb_fake.go"

CXDB_URL_FILE="$OUT_DIR/cxdb_url.txt"
"$CXDB_BIN" >"$CXDB_URL_FILE" 2>"$OUT_DIR/cxdb.log" &
CXDB_PID=$!

cleanup() {
  kill "$CXDB_PID" >/dev/null 2>&1 || true
}
trap cleanup EXIT

# Wait for URL to appear.
CXDB_URL=""
CXDB_BIN_ADDR=""
for _ in {1..200}; do
  if [[ -s "$CXDB_URL_FILE" ]]; then
    CXDB_URL="$(sed -n '1p' "$CXDB_URL_FILE" | tr -d '\r' | tr -d '\n' | xargs || true)"
    CXDB_BIN_ADDR="$(sed -n '2p' "$CXDB_URL_FILE" | tr -d '\r' | tr -d '\n' | xargs || true)"
    if [[ -n "$CXDB_URL" && -n "$CXDB_BIN_ADDR" ]]; then
      break
    fi
  fi
  sleep 0.05
done

if [[ -z "$CXDB_URL" || -z "$CXDB_BIN_ADDR" ]]; then
  echo "failed to start fake cxdb (missing endpoints)" >&2
  echo "--- cxdb output ---" >&2
  sed -n '1,5p' "$CXDB_URL_FILE" >&2 || true
  echo "--- cxdb log ---" >&2
  sed -n '1,50p' "$OUT_DIR/cxdb.log" >&2 || true
  exit 1
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
  litellm_catalog_path: $PINNED_CATALOG
  litellm_catalog_update_policy: ${KILROY_BENCH_LITELLM_UPDATE_POLICY:-pinned}
git:
  run_branch_prefix: attractor/run
  commit_per_node: true
llm:
  providers:
YAML
  # Configure all supported providers so engine-level failover can work when API keys are present.
  # This does not change the DOT contract: the graph still selects the primary provider/model.
  echo "NOTE: benchmark run config includes failover-capable providers: openai, anthropic, google"
  local backend_default="${KILROY_BENCH_BACKEND_DEFAULT:-api}"
  for p in openai anthropic google; do
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
  done

  local run_id="bench-$name-$STAMP"
  local logs_root="$workdir/logs"
  mkdir -p "$logs_root"

  echo "== RUN $dot =="
  echo "graph=$graph"
  echo "run_id=$run_id"
  echo "logs_root=$logs_root"

  # Guard against hangs: per-node timeout is enforced by the engine, and we also apply an overall per-run timeout.
  local run_timeout="${KILROY_BENCH_RUN_TIMEOUT:-2h}"
  set +e
  if [[ "$run_timeout" == "0" ]]; then
    ./kilroy attractor run --graph "$graph" --config "$cfg" --run-id "$run_id" --logs-root "$logs_root" | tee "$workdir/run.out"
    local ec=${PIPESTATUS[0]}
  else
    timeout --preserve-status --signal=SIGTERM "$run_timeout" ./kilroy attractor run --graph "$graph" --config "$cfg" --run-id "$run_id" --logs-root "$logs_root" | tee "$workdir/run.out"
    local ec=${PIPESTATUS[0]}
  fi
  set -e

  echo "exit_code=$ec" | tee "$workdir/exit_code.txt"
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
