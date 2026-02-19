package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zeebo/blake3"
)

type cxdbTestServer struct {
	srv *httptest.Server
	bin net.Listener

	mu sync.Mutex

	nextContextID int
	nextTurnID    int
	nextSessionID atomic.Uint64
	contexts      map[string]*cxdbContextState
	blobs         map[[32]byte][]byte
}

type cxdbContextState struct {
	ContextID  string
	HeadTurnID string
	HeadDepth  int
}

func newCXDBTestServer(t *testing.T) *cxdbTestServer {
	t.Helper()

	s := &cxdbTestServer{
		nextContextID: 1,
		nextTurnID:    1,
		contexts:      map[string]*cxdbContextState{},
		blobs:         map[[32]byte][]byte{},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<!doctype html><html><body>CXDB</body></html>"))
	})
	mux.HandleFunc("/v1/registry/bundles/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.WriteHeader(http.StatusCreated)
	})
	handleContextCreate := func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		b, _ := ioReadAll(r.Body)
		_ = r.Body.Close()
		var req map[string]any
		_ = json.Unmarshal(b, &req)
		baseTurnID := strings.TrimSpace(anyToString(req["base_turn_id"]))
		if baseTurnID == "" {
			baseTurnID = "0"
		}

		s.mu.Lock()
		id := strconv.Itoa(s.nextContextID)
		s.nextContextID++
		s.contexts[id] = &cxdbContextState{ContextID: id, HeadTurnID: baseTurnID, HeadDepth: 0}
		ci := *s.contexts[id]
		s.mu.Unlock()

		_ = json.NewEncoder(w).Encode(map[string]any{
			"context_id":   ci.ContextID,
			"head_turn_id": ci.HeadTurnID,
			"head_depth":   ci.HeadDepth,
		})
	}
	mux.HandleFunc("/v1/contexts/create", handleContextCreate)
	mux.HandleFunc("/v1/contexts/fork", handleContextCreate)
	mux.HandleFunc("/v1/contexts", handleContextCreate) // compat

	mux.HandleFunc("/v1/contexts/", func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, "/v1/contexts/")
		parts := strings.Split(rest, "/")
		if len(parts) < 2 || parts[1] != "append" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		ctxID := strings.TrimSpace(parts[0])
		if ctxID == "" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		s.mu.Lock()
		ci := s.contexts[ctxID]
		if ci == nil {
			s.mu.Unlock()
			w.WriteHeader(http.StatusNotFound)
			return
		}
		turnID := strconv.Itoa(s.nextTurnID)
		s.nextTurnID++
		ci.HeadDepth++
		ci.HeadTurnID = turnID
		depth := ci.HeadDepth
		s.mu.Unlock()

		_ = json.NewEncoder(w).Encode(map[string]any{
			"context_id":   ctxID,
			"turn_id":      turnID,
			"depth":        depth,
			"content_hash": "h" + turnID,
		})
	})

	s.srv = httptest.NewServer(mux)
	t.Cleanup(s.srv.Close)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen binary: %v", err)
	}
	s.bin = ln
	t.Cleanup(func() { _ = ln.Close() })
	go s.serveBinary()

	return s
}

func (s *cxdbTestServer) URL() string { return s.srv.URL }
func (s *cxdbTestServer) BinaryAddr() string {
	if s == nil || s.bin == nil {
		return ""
	}
	return s.bin.Addr().String()
}

func ioReadAll(r io.Reader) ([]byte, error) {
	var buf bytes.Buffer
	_, err := buf.ReadFrom(r)
	return buf.Bytes(), err
}

func anyToString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	default:
		return fmt.Sprint(v)
	}
}

type binFrameHeader struct {
	Len     uint32
	MsgType uint16
	Flags   uint16
	ReqID   uint64
}

func readBinFrame(r io.Reader) (binFrameHeader, []byte, error) {
	var hdrBuf [16]byte
	if _, err := io.ReadFull(r, hdrBuf[:]); err != nil {
		return binFrameHeader{}, nil, err
	}
	h := binFrameHeader{
		Len:     binary.LittleEndian.Uint32(hdrBuf[0:4]),
		MsgType: binary.LittleEndian.Uint16(hdrBuf[4:6]),
		Flags:   binary.LittleEndian.Uint16(hdrBuf[6:8]),
		ReqID:   binary.LittleEndian.Uint64(hdrBuf[8:16]),
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

func (s *cxdbTestServer) serveBinary() {
	for {
		conn, err := s.bin.Accept()
		if err != nil {
			return
		}
		go s.handleBinaryConn(conn)
	}
}

func (s *cxdbTestServer) handleBinaryConn(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	for {
		h, payload, err := readBinFrame(conn)
		if err != nil {
			return
		}
		switch h.MsgType {
		case 1: // HELLO
			// protocol_version(u32) + client_tag_len(u32) + client_tag
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
			_ = payload[8 : 8+tagLen] // ignore tag

			sessionID := s.nextSessionID.Add(1)
			serverTag := []byte("cxdb-test")

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
				_ = writeBinError(conn, h.ReqID, 400, fmt.Sprintf("put_blob: len mismatch: raw_len=%d payload=%d", rawLen, len(payload)))
				continue
			}
			raw := payload[36:]
			gotHash := blake3.Sum256(raw)
			if gotHash != wantHash {
				_ = writeBinError(conn, h.ReqID, 409, "put_blob: hash mismatch")
				continue
			}

			s.mu.Lock()
			_, existed := s.blobs[wantHash]
			if !existed {
				s.blobs[wantHash] = append([]byte{}, raw...)
			}
			s.mu.Unlock()

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

func buildKilroyBinary(t *testing.T) string {
	t.Helper()
	return buildKilroyBinaryWithRevision(t, "")
}

func buildKilroyBinaryWithRevision(t *testing.T, revision string) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	// wd is .../cmd/kilroy
	root := filepath.Dir(filepath.Dir(wd))
	bin := filepath.Join(t.TempDir(), "kilroy")
	args := []string{"build", "-o", bin}
	if strings.TrimSpace(revision) != "" {
		args = append(args, "-ldflags", "-X main.embeddedBuildRevision="+revision)
	}
	args = append(args, "./cmd/kilroy")
	cmd := exec.Command("go", args...)
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go build: %v\n%s", err, string(out))
	}
	return bin
}

func initTestRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = repo
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s failed: %v\n%s", strings.Join(args, " "), err, string(out))
		}
	}
	run("git", "init")
	run("git", "config", "user.name", "tester")
	run("git", "config", "user.email", "tester@example.com")
	_ = os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644)
	run("git", "add", "-A")
	run("git", "commit", "-m", "init")
	return repo
}

func writePinnedCatalog(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "model_prices_and_context_window.json")
	// Minimal OpenRouter model info payload shape.
	_ = os.WriteFile(path, []byte(`{
  "data": [
    {
      "id": "openai/gpt-5.2",
      "context_length": 1000,
      "top_provider": {"max_completion_tokens": 1000}
    }
  ]
}`), 0o644)
	return path
}

func writeRunConfig(t *testing.T, repo string, cxdbURL string, cxdbBinaryAddr string, catalogPath string) string {
	t.Helper()
	return writeRunConfigWithCXDBExtras(t, repo, cxdbURL, cxdbBinaryAddr, catalogPath, "")
}

func writeRunConfigWithCXDBExtras(t *testing.T, repo string, cxdbURL string, cxdbBinaryAddr string, catalogPath string, cxdbExtra string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "run.yaml")
	var sb strings.Builder
	sb.WriteString("version: 1\n")
	sb.WriteString("repo:\n")
	sb.WriteString("  path: " + repo + "\n")
	sb.WriteString("cxdb:\n")
	sb.WriteString("  binary_addr: " + cxdbBinaryAddr + "\n")
	sb.WriteString("  http_base_url: " + cxdbURL + "\n")
	extra := strings.Trim(cxdbExtra, "\n")
	if extra != "" {
		sb.WriteString(extra)
		sb.WriteString("\n")
	}
	sb.WriteString("modeldb:\n")
	sb.WriteString("  openrouter_model_info_path: " + catalogPath + "\n")
	sb.WriteString("  openrouter_model_info_update_policy: pinned\n")
	b := []byte(sb.String())
	_ = os.WriteFile(path, b, 0o644)
	return path
}

func runKilroy(t *testing.T, bin string, args ...string) (exitCode int, stdoutStderr string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	out, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		t.Fatalf("kilroy timed out\n%s", string(out))
	}
	if err == nil {
		return 0, string(out)
	}
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("kilroy failed: %v\n%s", err, string(out))
	}
	return ee.ExitCode(), string(out)
}

func runKilroyWithInput(t *testing.T, bin string, input string, args ...string) (exitCode int, stdoutStderr string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Stdin = strings.NewReader(input)
	out, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		t.Fatalf("kilroy timed out\n%s", string(out))
	}
	if err == nil {
		return 0, string(out)
	}
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("kilroy failed: %v\n%s", err, string(out))
	}
	return ee.ExitCode(), string(out)
}

func runKilroyInDir(t *testing.T, dir string, bin string, args ...string) (exitCode int, stdoutStderr string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		t.Fatalf("kilroy timed out\n%s", string(out))
	}
	if err == nil {
		return 0, string(out)
	}
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("kilroy failed: %v\n%s", err, string(out))
	}
	return ee.ExitCode(), string(out)
}

func TestVersion_PrintsVersionAndExitsZero(t *testing.T) {
	bin := buildKilroyBinary(t)

	for _, flag := range []string{"--version", "-v", "version"} {
		t.Run(flag, func(t *testing.T) {
			code, out := runKilroy(t, bin, flag)
			if code != 0 {
				t.Fatalf("exit code: got %d want 0\n%s", code, out)
			}
			if !strings.Contains(out, "kilroy ") {
				t.Fatalf("expected version output to contain 'kilroy ', got:\n%s", out)
			}
		})
	}
}

func TestUsage_IncludesVersionFlag(t *testing.T) {
	bin := buildKilroyBinary(t)
	code, out := runKilroy(t, bin)
	if code != 1 {
		t.Fatalf("exit code: got %d want 1\n%s", code, out)
	}
	if !strings.Contains(out, "--version") {
		t.Fatalf("usage should include --version; output:\n%s", out)
	}
}

func TestKilroyAttractorExitCodes(t *testing.T) {
	cxdbSrv := newCXDBTestServer(t)
	bin := buildKilroyBinary(t)
	repo := initTestRepo(t)
	catalog := writePinnedCatalog(t)
	cfg := writeRunConfig(t, repo, cxdbSrv.URL(), cxdbSrv.BinaryAddr(), catalog)

	// Success -> exit code 0.
	successGraph := filepath.Join(t.TempDir(), "success.dot")
	_ = os.WriteFile(successGraph, []byte(`
digraph G {
  start [shape=Mdiamond]
  exit [shape=Msquare]
  start -> exit
}
`), 0o644)
	logsRoot1 := filepath.Join(t.TempDir(), "logs-success")
	code, out := runKilroy(t, bin, "attractor", "run", "--graph", successGraph, "--config", cfg, "--run-id", "cli-success", "--logs-root", logsRoot1)
	if code != 0 {
		t.Fatalf("success exit code: got %d want 0\n%s", code, out)
	}

	// Failure -> exit code 1.
	failGraph := filepath.Join(t.TempDir(), "fail.dot")
	_ = os.WriteFile(failGraph, []byte(`
digraph G {
  start [shape=Mdiamond]
  exit [shape=Msquare]
  t [shape=parallelogram, tool_command="exit 1"]
  start -> t
}
`), 0o644)
	logsRoot2 := filepath.Join(t.TempDir(), "logs-fail")
	code, out = runKilroy(t, bin, "attractor", "run", "--graph", failGraph, "--config", cfg, "--run-id", "cli-fail", "--logs-root", logsRoot2)
	if code != 1 {
		t.Fatalf("fail exit code: got %d want 1\n%s", code, out)
	}
}

func TestAttractorRun_AllowsTestShimFlag(t *testing.T) {
	cxdbSrv := newCXDBTestServer(t)
	bin := buildKilroyBinary(t)
	repo := initTestRepo(t)
	catalog := writePinnedCatalog(t)
	cfg := writeRunConfig(t, repo, cxdbSrv.URL(), cxdbSrv.BinaryAddr(), catalog)

	graph := filepath.Join(t.TempDir(), "success.dot")
	_ = os.WriteFile(graph, []byte(`
digraph G {
  start [shape=Mdiamond]
  exit [shape=Msquare]
  start -> exit
}
`), 0o644)

	logsRoot := filepath.Join(t.TempDir(), "logs")
	code, out := runKilroy(t, bin, "attractor", "run", "--graph", graph, "--config", cfg, "--run-id", "allow-test-shim", "--logs-root", logsRoot, "--allow-test-shim")
	if code != 0 {
		t.Fatalf("exit code: got %d want 0\n%s", code, out)
	}
}

func TestUsage_IncludesAllowTestShimFlag(t *testing.T) {
	bin := buildKilroyBinary(t)
	code, out := runKilroy(t, bin)
	if code != 1 {
		t.Fatalf("exit code: got %d want 1\n%s", code, out)
	}
	if !strings.Contains(out, "--allow-test-shim") {
		t.Fatalf("usage should include --allow-test-shim; output:\n%s", out)
	}
	if !strings.Contains(out, "--confirm-stale-build") {
		t.Fatalf("usage should include --confirm-stale-build; output:\n%s", out)
	}
	if !strings.Contains(out, "--force-model") {
		t.Fatalf("usage should include --force-model; output:\n%s", out)
	}
}

func TestAttractorRun_StaleBuildRequiresConfirm(t *testing.T) {
	bin := buildKilroyBinaryWithRevision(t, "deadbeef")
	repo := initTestRepo(t)
	repoBin := filepath.Join(repo, "kilroy")
	b, err := os.ReadFile(bin)
	if err != nil {
		t.Fatalf("read built binary: %v", err)
	}
	if err := os.WriteFile(repoBin, b, 0o755); err != nil {
		t.Fatalf("write repo binary: %v", err)
	}

	missingGraph := filepath.Join(repo, "missing.dot")
	missingConfig := filepath.Join(repo, "missing.yaml")
	code, out := runKilroyInDir(t, repo, repoBin, "attractor", "run", "--graph", missingGraph, "--config", missingConfig)
	if code != 1 {
		t.Fatalf("exit code: got %d want 1\n%s", code, out)
	}
	if !strings.Contains(out, "WARNING: STALE KILROY BUILD DETECTED") {
		t.Fatalf("expected stale build warning, got:\n%s", out)
	}
	if !strings.Contains(out, "--confirm-stale-build") {
		t.Fatalf("expected confirm remediation hint, got:\n%s", out)
	}
	if strings.Contains(out, "missing.dot") {
		t.Fatalf("expected stale-build gate before graph read, got:\n%s", out)
	}
}

func TestAttractorRun_StaleBuildConfirmAllowsProceeding(t *testing.T) {
	bin := buildKilroyBinaryWithRevision(t, "deadbeef")
	repo := initTestRepo(t)
	repoBin := filepath.Join(repo, "kilroy")
	b, err := os.ReadFile(bin)
	if err != nil {
		t.Fatalf("read built binary: %v", err)
	}
	if err := os.WriteFile(repoBin, b, 0o755); err != nil {
		t.Fatalf("write repo binary: %v", err)
	}

	missingGraph := filepath.Join(repo, "missing.dot")
	missingConfig := filepath.Join(repo, "missing.yaml")
	code, out := runKilroyInDir(t, repo, repoBin, "attractor", "run", "--confirm-stale-build", "--graph", missingGraph, "--config", missingConfig)
	if code != 1 {
		t.Fatalf("exit code: got %d want 1\n%s", code, out)
	}
	if !strings.Contains(out, "WARNING: STALE KILROY BUILD DETECTED") {
		t.Fatalf("expected stale build warning, got:\n%s", out)
	}
	if !strings.Contains(out, "proceeding because --confirm-stale-build was provided") {
		t.Fatalf("expected proceed message, got:\n%s", out)
	}
	if !strings.Contains(out, "missing.dot") {
		t.Fatalf("expected command to proceed to graph read, got:\n%s", out)
	}
}

func TestParseForceModelFlags_NormalizesAndCanonicalizes(t *testing.T) {
	got, specs, err := parseForceModelFlags([]string{
		"openai=gpt-5.2-codex",
		"gemini=gemini-3-pro-preview",
	})
	if err != nil {
		t.Fatalf("parseForceModelFlags: %v", err)
	}
	want := map[string]string{
		"openai": "gpt-5.2-codex",
		"google": "gemini-3-pro-preview",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("overrides: got %#v want %#v", got, want)
	}
	wantSpecs := []string{
		"google=gemini-3-pro-preview",
		"openai=gpt-5.2-codex",
	}
	if !reflect.DeepEqual(specs, wantSpecs) {
		t.Fatalf("canonical specs: got %#v want %#v", specs, wantSpecs)
	}
}

func TestParseForceModelFlags_AcceptsKimiAndZaiAliases(t *testing.T) {
	got, specs, err := parseForceModelFlags([]string{
		"moonshot=kimi-k2.5",
		"z-ai=glm-4.7",
	})
	if err != nil {
		t.Fatalf("parseForceModelFlags: %v", err)
	}
	want := map[string]string{
		"kimi": "kimi-k2.5",
		"zai":  "glm-4.7",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("overrides: got %#v want %#v", got, want)
	}
	wantSpecs := []string{
		"kimi=kimi-k2.5",
		"zai=glm-4.7",
	}
	if !reflect.DeepEqual(specs, wantSpecs) {
		t.Fatalf("canonical specs: got %#v want %#v", specs, wantSpecs)
	}
}

func TestParseForceModelFlags_RejectsInvalidShape(t *testing.T) {
	if _, _, err := parseForceModelFlags([]string{"openai"}); err == nil {
		t.Fatalf("expected parse error for missing '='")
	}
}

func TestParseForceModelFlags_RejectsUnsupportedProvider(t *testing.T) {
	if _, _, err := parseForceModelFlags([]string{"foo=model"}); err == nil {
		t.Fatalf("expected parse error for unsupported provider")
	}
}

func TestParseForceModelFlags_RejectsDuplicateProvider(t *testing.T) {
	if _, _, err := parseForceModelFlags([]string{
		"openai=gpt-5.2-codex",
		"openai=gpt-5.3-codex",
	}); err == nil {
		t.Fatalf("expected parse error for duplicate provider")
	}
	if _, _, err := parseForceModelFlags([]string{
		"gemini=gemini-3-pro-preview",
		"google=gemini-3-flash",
	}); err == nil {
		t.Fatalf("expected parse error for duplicate provider alias")
	}
}

func TestAttractorRun_RealProfileRejectsShimOverride(t *testing.T) {
	bin := buildKilroyBinary(t)
	repo := initTestRepo(t)
	catalog := writePinnedCatalog(t)
	t.Setenv("KILROY_CODEX_PATH", "/tmp/fake/codex")

	graph := filepath.Join(t.TempDir(), "openai.dot")
	_ = os.WriteFile(graph, []byte(`
digraph G {
  start [shape=Mdiamond]
  exit [shape=Msquare]
  a [shape=box, llm_provider=openai, llm_model=gpt-5.2, prompt="hi"]
  start -> a -> exit
}
`), 0o644)

	cfg := filepath.Join(t.TempDir(), "run.yaml")
	_ = os.WriteFile(cfg, []byte(fmt.Sprintf(`
version: 1
repo:
  path: %s
cxdb:
  binary_addr: 127.0.0.1:9009
  http_base_url: http://127.0.0.1:9010
llm:
  cli_profile: real
  providers:
    openai:
      backend: cli
modeldb:
  openrouter_model_info_path: %s
  openrouter_model_info_update_policy: pinned
`, repo, catalog)), 0o644)

	logsRoot := filepath.Join(t.TempDir(), "logs")
	code, out := runKilroy(t, bin, "attractor", "run", "--graph", graph, "--config", cfg, "--run-id", "real-reject-shim", "--logs-root", logsRoot)
	if code != 1 {
		t.Fatalf("exit code: got %d want 1\n%s", code, out)
	}
	if !strings.Contains(out, "llm.cli_profile=real forbids provider path overrides") {
		t.Fatalf("expected real profile override rejection, got:\n%s", out)
	}
}

func TestAttractorRun_CLIProviderWarningCanAbortPreflight(t *testing.T) {
	bin := buildKilroyBinary(t)
	repo := initTestRepo(t)
	catalog := writePinnedCatalog(t)

	graph := filepath.Join(t.TempDir(), "openai.dot")
	_ = os.WriteFile(graph, []byte(`
digraph G {
  start [shape=Mdiamond]
  exit [shape=Msquare]
  a [shape=box, llm_provider=openai, llm_model=gpt-5.2, prompt="hi"]
  start -> a -> exit
}
`), 0o644)

	cfg := filepath.Join(t.TempDir(), "run.yaml")
	_ = os.WriteFile(cfg, []byte(fmt.Sprintf(`
version: 1
repo:
  path: %s
cxdb:
  binary_addr: 127.0.0.1:9009
  http_base_url: http://127.0.0.1:9010
llm:
  cli_profile: real
  providers:
    openai:
      backend: cli
modeldb:
  openrouter_model_info_path: %s
  openrouter_model_info_update_policy: pinned
`, repo, catalog)), 0o644)

	logsRoot := filepath.Join(t.TempDir(), "logs")
	code, out := runKilroyWithInput(t, bin, "n\n", "attractor", "run", "--graph", graph, "--config", cfg, "--run-id", "cli-warning-abort", "--logs-root", logsRoot)
	if code != 1 {
		t.Fatalf("exit code: got %d want 1\n%s", code, out)
	}
	if !strings.Contains(out, cliHeadlessWarningPrompt) {
		t.Fatalf("expected cli headless warning prompt, got:\n%s", out)
	}
	if !strings.Contains(out, "preflight aborted: declined provider CLI headless-risk warning") {
		t.Fatalf("expected explicit preflight abort message, got:\n%s", out)
	}
}

func TestAttractorRun_CLIProviderWarningDefaultsToProceedOnEnter(t *testing.T) {
	bin := buildKilroyBinary(t)
	repo := initTestRepo(t)
	catalog := writePinnedCatalog(t)
	t.Setenv("KILROY_CODEX_PATH", "/tmp/fake/codex")

	graph := filepath.Join(t.TempDir(), "openai.dot")
	_ = os.WriteFile(graph, []byte(`
digraph G {
  start [shape=Mdiamond]
  exit [shape=Msquare]
  a [shape=box, llm_provider=openai, llm_model=gpt-5.2, prompt="hi"]
  start -> a -> exit
}
`), 0o644)

	cfg := filepath.Join(t.TempDir(), "run.yaml")
	_ = os.WriteFile(cfg, []byte(fmt.Sprintf(`
version: 1
repo:
  path: %s
cxdb:
  binary_addr: 127.0.0.1:9009
  http_base_url: http://127.0.0.1:9010
llm:
  cli_profile: real
  providers:
    openai:
      backend: cli
modeldb:
  openrouter_model_info_path: %s
  openrouter_model_info_update_policy: pinned
`, repo, catalog)), 0o644)

	logsRoot := filepath.Join(t.TempDir(), "logs")
	code, out := runKilroyWithInput(t, bin, "\n", "attractor", "run", "--graph", graph, "--config", cfg, "--run-id", "cli-warning-proceed", "--logs-root", logsRoot)
	if code != 1 {
		t.Fatalf("exit code: got %d want 1\n%s", code, out)
	}
	if !strings.Contains(out, cliHeadlessWarningPrompt) {
		t.Fatalf("expected cli headless warning prompt, got:\n%s", out)
	}
	if strings.Contains(out, "preflight aborted: declined provider CLI headless-risk warning") {
		t.Fatalf("prompt should default to proceed on Enter, got:\n%s", out)
	}
	if !strings.Contains(out, "llm.cli_profile=real forbids provider path overrides") {
		t.Fatalf("expected run to continue into preflight policy checks, got:\n%s", out)
	}
}

func TestKilroyAttractorRun_PrintsCXDBUILink(t *testing.T) {
	cxdbSrv := newCXDBTestServer(t)
	bin := buildKilroyBinary(t)
	repo := initTestRepo(t)
	catalog := writePinnedCatalog(t)
	cfg := writeRunConfigWithCXDBExtras(
		t,
		repo,
		cxdbSrv.URL(),
		cxdbSrv.BinaryAddr(),
		catalog,
		"  autostart:\n    ui:\n      url: http://127.0.0.1:9020",
	)

	graph := filepath.Join(t.TempDir(), "success.dot")
	_ = os.WriteFile(graph, []byte(`
digraph G {
  start [shape=Mdiamond]
  exit [shape=Msquare]
  start -> exit
}
`), 0o644)
	logsRoot := filepath.Join(t.TempDir(), "logs")
	code, out := runKilroy(t, bin, "attractor", "run", "--graph", graph, "--config", cfg, "--run-id", "cli-ui-link", "--logs-root", logsRoot)
	if code != 0 {
		t.Fatalf("exit code: got %d want 0\n%s", code, out)
	}
	if !strings.Contains(out, "CXDB UI available at http://127.0.0.1:9020") {
		t.Fatalf("missing startup UI line in output:\n%s", out)
	}
	if !strings.Contains(out, "cxdb_ui=http://127.0.0.1:9020") {
		t.Fatalf("missing cxdb_ui link in output:\n%s", out)
	}
}

func TestKilroyAttractorRun_PrintsCXDBUIStartingWhenLaunchCommandConfigured(t *testing.T) {
	cxdbSrv := newCXDBTestServer(t)
	bin := buildKilroyBinary(t)
	repo := initTestRepo(t)
	catalog := writePinnedCatalog(t)
	cfg := writeRunConfigWithCXDBExtras(
		t,
		repo,
		cxdbSrv.URL(),
		cxdbSrv.BinaryAddr(),
		catalog,
		"  autostart:\n    ui:\n      enabled: true\n      command: [\"/bin/sh\", \"-c\", \"true\"]\n      url: http://127.0.0.1:9020",
	)

	graph := filepath.Join(t.TempDir(), "success.dot")
	_ = os.WriteFile(graph, []byte(`
digraph G {
  start [shape=Mdiamond]
  exit [shape=Msquare]
  start -> exit
}
`), 0o644)
	logsRoot := filepath.Join(t.TempDir(), "logs")
	code, out := runKilroy(t, bin, "attractor", "run", "--graph", graph, "--config", cfg, "--run-id", "cli-ui-starting", "--logs-root", logsRoot)
	if code != 0 {
		t.Fatalf("exit code: got %d want 0\n%s", code, out)
	}
	if !strings.Contains(out, "CXDB UI starting at http://127.0.0.1:9020") {
		t.Fatalf("missing startup UI starting line in output:\n%s", out)
	}
}

func TestKilroyAttractorRun_AutoDiscoversCXDBUIFromHTTPBaseURL(t *testing.T) {
	cxdbSrv := newCXDBTestServer(t)
	bin := buildKilroyBinary(t)
	repo := initTestRepo(t)
	catalog := writePinnedCatalog(t)
	cfg := writeRunConfig(t, repo, cxdbSrv.URL(), cxdbSrv.BinaryAddr(), catalog)

	graph := filepath.Join(t.TempDir(), "success.dot")
	_ = os.WriteFile(graph, []byte(`
digraph G {
  start [shape=Mdiamond]
  exit [shape=Msquare]
  start -> exit
}
`), 0o644)
	logsRoot := filepath.Join(t.TempDir(), "logs")
	code, out := runKilroy(t, bin, "attractor", "run", "--graph", graph, "--config", cfg, "--run-id", "cli-ui-autodiscover", "--logs-root", logsRoot)
	if code != 0 {
		t.Fatalf("exit code: got %d want 0\n%s", code, out)
	}
	if !strings.Contains(out, "CXDB UI available at "+cxdbSrv.URL()) {
		t.Fatalf("missing autodiscovered UI startup line in output:\n%s", out)
	}
	if !strings.Contains(out, "cxdb_ui="+cxdbSrv.URL()) {
		t.Fatalf("missing autodiscovered cxdb_ui link in output:\n%s", out)
	}
}
