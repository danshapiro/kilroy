package codexappserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/danshapiro/kilroy/internal/llm"
)

type recordingWriteCloser struct {
	mu    sync.Mutex
	buf   bytes.Buffer
	err   error
	close bool
}

func (w *recordingWriteCloser) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.err != nil {
		return 0, w.err
	}
	return w.buf.Write(p)
}

func (w *recordingWriteCloser) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.close = true
	return nil
}

func (w *recordingWriteCloser) lines() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	raw := strings.TrimSpace(w.buf.String())
	if raw == "" {
		return nil
	}
	return strings.Split(raw, "\n")
}

func aliveCmd(t *testing.T) *exec.Cmd {
	t.Helper()
	proc, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatalf("FindProcess: %v", err)
	}
	return &exec.Cmd{Process: proc}
}

func TestTransport_ParseTurnStartPayload_ValidatesAndDefaults(t *testing.T) {
	if _, err := parseTurnStartPayload(nil); err == nil {
		t.Fatalf("expected error for nil payload")
	}

	if _, err := parseTurnStartPayload(map[string]any{"threadId": "thread_1"}); err == nil {
		t.Fatalf("expected error for missing input array")
	}

	in := map[string]any{
		"input": []any{
			map[string]any{"type": "message", "role": "user"},
		},
	}
	out, err := parseTurnStartPayload(in)
	if err != nil {
		t.Fatalf("parseTurnStartPayload: %v", err)
	}
	if got := asString(out["threadId"]); got != defaultThreadID {
		t.Fatalf("threadId default: got %q want %q", got, defaultThreadID)
	}
	if asSlice(out["input"]) == nil {
		t.Fatalf("input array was not preserved: %#v", out["input"])
	}
	if _, ok := in["threadId"]; ok {
		t.Fatalf("expected input map to remain unmodified; got %#v", in)
	}
}

func TestTransport_ToThreadStartParams_FiltersFields(t *testing.T) {
	turn := map[string]any{
		"model":          "codex-mini",
		"cwd":            "/tmp/repo",
		"approvalPolicy": "never",
		"personality":    "strict",
		"sandbox":        "danger-full-access",
		"ignored":        true,
	}
	got := toThreadStartParams(turn)
	if got["model"] != "codex-mini" || got["cwd"] != "/tmp/repo" || got["approvalPolicy"] != "never" || got["personality"] != "strict" {
		t.Fatalf("unexpected mapped thread params: %#v", got)
	}
	if got["sandbox"] != "danger-full-access" {
		t.Fatalf("sandbox: got %#v", got["sandbox"])
	}
	if _, ok := got["ignored"]; ok {
		t.Fatalf("did not expect ignored key in thread params: %#v", got)
	}

	turn["sandbox"] = "unsupported"
	got = toThreadStartParams(turn)
	if _, ok := got["sandbox"]; ok {
		t.Fatalf("unexpected sandbox for unsupported mode: %#v", got["sandbox"])
	}
}

func TestTransport_TurnStatusAndNotificationMatching(t *testing.T) {
	if !isTerminalTurnStatus("completed") || !isTerminalTurnStatus("failed") || !isTerminalTurnStatus("interrupted") {
		t.Fatalf("expected terminal statuses to match")
	}
	if isTerminalTurnStatus("running") {
		t.Fatalf("did not expect running to be terminal")
	}

	n1 := map[string]any{"params": map[string]any{"threadId": "thread_1", "turnId": "turn_1"}}
	if got := extractThreadID(n1); got != "thread_1" {
		t.Fatalf("extractThreadID: got %q", got)
	}
	if got := extractTurnID(n1); got != "turn_1" {
		t.Fatalf("extractTurnID: got %q", got)
	}
	if !notificationBelongsToTurn(n1, "thread_1", "turn_1") {
		t.Fatalf("expected notification to belong to matching thread/turn")
	}
	if notificationBelongsToTurn(n1, "thread_2", "turn_1") {
		t.Fatalf("expected thread mismatch to reject notification")
	}
	if notificationBelongsToTurn(n1, "thread_1", "turn_2") {
		t.Fatalf("expected turn mismatch to reject notification")
	}

	n2 := map[string]any{"params": map[string]any{"thread_id": "thread_1", "turn": map[string]any{"id": "turn_1"}}}
	if got := extractThreadID(n2); got != "thread_1" {
		t.Fatalf("extractThreadID snake_case: got %q", got)
	}
	if got := extractTurnID(n2); got != "turn_1" {
		t.Fatalf("extractTurnID nested turn.id: got %q", got)
	}
	if !notificationBelongsToTurn(n2, "thread_1", "") {
		t.Fatalf("expected turn-less matching to pass when thread matches")
	}
}

func TestTransport_FindCompletedTurn_ReturnsLatestMatching(t *testing.T) {
	notifications := []map[string]any{
		{"method": "turn/progress", "params": map[string]any{"threadId": "thread_1"}},
		{"method": "turn/completed", "params": map[string]any{"turnId": "turn_old", "turn": map[string]any{"id": "turn_old", "items": []any{"a"}}}},
		{"method": "turn/completed", "params": map[string]any{"turnId": "turn_new", "turn": map[string]any{"id": "turn_new"}}}, // missing items
		{"method": "turn/completed", "params": map[string]any{"turnId": "turn_new", "turn": map[string]any{"id": "turn_new", "items": []any{"x"}}}},
	}

	got := findCompletedTurn(notifications, "turn_new")
	if got == nil {
		t.Fatalf("expected completed turn")
	}
	if asString(got["id"]) != "turn_new" {
		t.Fatalf("completed turn id: got %#v", got["id"])
	}
	if len(asSlice(got["items"])) != 1 {
		t.Fatalf("completed turn items: %#v", got["items"])
	}

	if miss := findCompletedTurn(notifications, "turn_missing"); miss != nil {
		t.Fatalf("expected nil for missing turn id; got %#v", miss)
	}
}

func TestTransport_ProcessAliveAndRPCIDKey(t *testing.T) {
	if processAlive(nil) {
		t.Fatalf("nil command should not be alive")
	}

	cmd := aliveCmd(t)
	if !processAlive(cmd) {
		t.Fatalf("expected command with process to be alive")
	}

	finished := exec.Command(os.Args[0], "-test.run=TestTransport_HelperProcess")
	finished.Env = append(os.Environ(),
		"GO_WANT_TRANSPORT_HELPER=1",
		"GO_TRANSPORT_HELPER_MODE=exit",
	)
	if err := finished.Run(); err != nil {
		t.Fatalf("run finished command: %v", err)
	}
	if processAlive(finished) {
		t.Fatalf("expected exited command to be not alive")
	}

	if got := rpcIDKey("  x  "); got != "x" {
		t.Fatalf("rpcIDKey string trim: got %q", got)
	}
	if got := rpcIDKey(12); got != "12" {
		t.Fatalf("rpcIDKey int conversion: got %q", got)
	}
}

func TestTransport_ResolveAndRejectPendingRequests(t *testing.T) {
	tp := &stdioTransport{pending: map[string]*pendingRequest{}}

	respCh := make(chan pendingResult, 1)
	tp.pending["1"] = &pendingRequest{method: "turn/start", respCh: respCh}
	tp.resolvePendingRequest(1, pendingResult{result: map[string]any{"ok": true}})

	select {
	case got := <-respCh:
		if asMap(got.result)["ok"] != true {
			t.Fatalf("unexpected pending result: %#v", got.result)
		}
	default:
		t.Fatalf("expected resolved pending result")
	}
	if _, ok := tp.pending["1"]; ok {
		t.Fatalf("expected pending request to be removed after resolution")
	}

	errCh := make(chan pendingResult, 1)
	tp.pending["2"] = &pendingRequest{method: "turn/start", respCh: errCh}
	wantErr := errors.New("transport failed")
	tp.rejectAllPending(wantErr)

	select {
	case got := <-errCh:
		if !errors.Is(got.err, wantErr) {
			t.Fatalf("rejectAllPending err: got %v want %v", got.err, wantErr)
		}
	default:
		t.Fatalf("expected rejected pending error")
	}
	if len(tp.pending) != 0 {
		t.Fatalf("expected pending map to be cleared; got %#v", tp.pending)
	}
}

func TestTransport_EmitNotificationAndSubscribeLifecycle(t *testing.T) {
	tp := &stdioTransport{listeners: map[int]func(map[string]any){}}

	got := make(chan string, 2)
	unsubscribe := tp.subscribe(func(notification map[string]any) {
		got <- asString(notification["method"])
	})
	_ = tp.subscribe(func(map[string]any) {
		panic("listener panic should be recovered")
	})

	tp.emitNotification(map[string]any{"method": "turn/progress"})
	select {
	case method := <-got:
		if method != "turn/progress" {
			t.Fatalf("unexpected method: %q", method)
		}
	default:
		t.Fatalf("expected listener notification")
	}

	unsubscribe()
	tp.emitNotification(map[string]any{"method": "turn/completed"})
	select {
	case method := <-got:
		t.Fatalf("did not expect method after unsubscribe: %q", method)
	default:
	}
}

func TestTransport_ToRPCError_MapsJSONRPCCodes(t *testing.T) {
	tp := &stdioTransport{}
	badReq := tp.toRPCError("turn/start", map[string]any{
		"code":    -32601,
		"message": "method not found",
		"data":    map[string]any{"hint": "check schema"},
	})
	var llmErr llm.Error
	if !errors.As(badReq, &llmErr) {
		t.Fatalf("expected llm.Error, got %T", badReq)
	}
	if llmErr.StatusCode() != 400 {
		t.Fatalf("status code: got %d want 400", llmErr.StatusCode())
	}

	serverErr := tp.toRPCError("turn/start", map[string]any{
		"code":    -32000,
		"message": "internal",
	})
	if !errors.As(serverErr, &llmErr) {
		t.Fatalf("expected llm.Error, got %T", serverErr)
	}
	if llmErr.StatusCode() != 500 {
		t.Fatalf("status code: got %d want 500", llmErr.StatusCode())
	}
}

func TestTransport_WriteJSONLine_ValidationAndSuccessPaths(t *testing.T) {
	tp := &stdioTransport{}
	if err := tp.writeJSONLine(map[string]any{"bad": func() {}}); err == nil {
		t.Fatalf("expected marshal error")
	}

	if err := tp.writeJSONLine(map[string]any{"method": "x"}); err == nil {
		t.Fatalf("expected non-writable stdin error without process")
	}

	writer := &recordingWriteCloser{}
	tp = &stdioTransport{cmd: aliveCmd(t), stdin: writer}
	if err := tp.writeJSONLine(map[string]any{"method": "turn/start"}); err != nil {
		t.Fatalf("writeJSONLine success: %v", err)
	}
	lines := writer.lines()
	if len(lines) != 1 || !strings.Contains(lines[0], `"method":"turn/start"`) {
		t.Fatalf("unexpected written line: %#v", lines)
	}

	failingWriter := &recordingWriteCloser{err: errors.New("boom")}
	tp = &stdioTransport{cmd: aliveCmd(t), stdin: failingWriter}
	if err := tp.writeJSONLine(map[string]any{"method": "turn/start"}); err == nil {
		t.Fatalf("expected write failure")
	}
}

func TestTransport_SendRequest_CoversClosedWriteSuccessAndTimeout(t *testing.T) {
	tp := &stdioTransport{
		closed:  true,
		pending: map[string]*pendingRequest{},
	}
	if _, err := tp.sendRequest(context.Background(), "turn/start", nil, 50*time.Millisecond); err == nil {
		t.Fatalf("expected closed transport error")
	}

	tp = &stdioTransport{
		pending: map[string]*pendingRequest{},
	}
	if _, err := tp.sendRequest(context.Background(), "turn/start", nil, 50*time.Millisecond); err == nil {
		t.Fatalf("expected write error when process is unavailable")
	}
	if len(tp.pending) != 0 {
		t.Fatalf("expected pending to be cleaned after write failure; got %#v", tp.pending)
	}

	writer := &recordingWriteCloser{}
	tp = &stdioTransport{
		pending: map[string]*pendingRequest{},
		cmd:     aliveCmd(t),
		stdin:   writer,
	}
	resultCh := make(chan struct {
		value map[string]any
		err   error
	}, 1)
	go func() {
		value, err := tp.sendRequest(context.Background(), "turn/start", map[string]any{"input": []any{}}, 250*time.Millisecond)
		resultCh <- struct {
			value map[string]any
			err   error
		}{value: value, err: err}
	}()

	deadline := time.Now().Add(150 * time.Millisecond)
	for {
		tp.mu.Lock()
		_, ok := tp.pending["1"]
		tp.mu.Unlock()
		if ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for pending request registration")
		}
		time.Sleep(2 * time.Millisecond)
	}
	tp.resolvePendingRequest(1, pendingResult{result: map[string]any{"ok": true}})
	got := <-resultCh
	if got.err != nil {
		t.Fatalf("sendRequest success path returned error: %v", got.err)
	}
	if asMap(got.value)["ok"] != true {
		t.Fatalf("sendRequest success payload: %#v", got.value)
	}

	timeoutTransport := &stdioTransport{
		pending: map[string]*pendingRequest{},
		cmd:     aliveCmd(t),
		stdin:   &recordingWriteCloser{},
	}
	_, err := timeoutTransport.sendRequest(context.Background(), "turn/start", nil, 25*time.Millisecond)
	if err == nil {
		t.Fatalf("expected timeout error")
	}
	tpErr := &llm.RequestTimeoutError{}
	if !errors.As(err, &tpErr) {
		t.Fatalf("expected RequestTimeoutError, got %T (%v)", err, err)
	}
	timeoutTransport.mu.Lock()
	pendingLen := len(timeoutTransport.pending)
	timeoutTransport.mu.Unlock()
	if pendingLen != 0 {
		t.Fatalf("expected timeout path to clear pending map, got len=%d", pendingLen)
	}
}

func TestTransport_HandleIncomingMessage_ResolvesPendingAndForwardsNotifications(t *testing.T) {
	tp := &stdioTransport{
		pending:   map[string]*pendingRequest{},
		listeners: map[int]func(map[string]any){},
	}

	okCh := make(chan pendingResult, 1)
	tp.pending["1"] = &pendingRequest{method: "turn/start", respCh: okCh}
	tp.handleIncomingMessage(map[string]any{"id": 1, "result": map[string]any{"threadId": "thread_1"}})
	select {
	case got := <-okCh:
		if asMap(got.result)["threadId"] != "thread_1" {
			t.Fatalf("unexpected result payload: %#v", got.result)
		}
	default:
		t.Fatalf("expected pending result resolution")
	}

	errCh := make(chan pendingResult, 1)
	tp.pending["2"] = &pendingRequest{method: "turn/start", respCh: errCh}
	tp.handleIncomingMessage(map[string]any{
		"id":     2,
		"method": "turn/start",
		"error":  map[string]any{"code": -32601, "message": "method not found"},
	})
	select {
	case got := <-errCh:
		if got.err == nil {
			t.Fatalf("expected rpc error result")
		}
	default:
		t.Fatalf("expected pending error resolution")
	}

	notifications := make(chan string, 1)
	unsubscribe := tp.subscribe(func(notification map[string]any) {
		notifications <- asString(notification["method"])
	})
	tp.handleIncomingMessage(map[string]any{
		"method": "turn/progress",
		"params": map[string]any{"threadId": "thread_1"},
	})
	select {
	case method := <-notifications:
		if method != "turn/progress" {
			t.Fatalf("unexpected notification method: %q", method)
		}
	default:
		t.Fatalf("expected notification fan-out")
	}
	unsubscribe()
}

func TestTransport_HandleServerRequest_SupportsKnownMethods(t *testing.T) {
	writer := &recordingWriteCloser{}
	tp := &stdioTransport{
		cmd:   aliveCmd(t),
		stdin: writer,
	}

	cases := []struct {
		id        int
		method    string
		params    any
		wantField string
		wantValue string
		wantErr   int
	}{
		{id: 1, method: "item/tool/call", wantField: "success", wantValue: "false"},
		{id: 2, method: "item/tool/requestUserInput", params: map[string]any{"questions": []any{map[string]any{"id": "q1"}}}, wantField: "answers", wantValue: "map"},
		{id: 3, method: "item/commandExecution/requestApproval", wantField: "decision", wantValue: "decline"},
		{id: 4, method: "item/fileChange/requestApproval", wantField: "decision", wantValue: "decline"},
		{id: 5, method: "applyPatchApproval", wantField: "decision", wantValue: "denied"},
		{id: 6, method: "execCommandApproval", wantField: "decision", wantValue: "denied"},
		{id: 7, method: "account/chatgptAuthTokens/refresh", wantErr: -32001},
		{id: 8, method: "unknown/method", wantErr: -32601},
	}

	for _, tc := range cases {
		tp.handleServerRequest(tc.id, tc.method, tc.params)
	}

	lines := writer.lines()
	if len(lines) != len(cases) {
		t.Fatalf("written lines: got %d want %d (%#v)", len(lines), len(cases), lines)
	}
	for i, tc := range cases {
		var msg map[string]any
		if err := json.Unmarshal([]byte(lines[i]), &msg); err != nil {
			t.Fatalf("unmarshal response line %d: %v", i, err)
		}
		if got := asInt(msg["id"], 0); got != tc.id {
			t.Fatalf("line %d id: got %d want %d", i, got, tc.id)
		}
		if tc.wantErr != 0 {
			errObj := asMap(msg["error"])
			if got := asInt(errObj["code"], 0); got != tc.wantErr {
				t.Fatalf("line %d error code: got %d want %d", i, got, tc.wantErr)
			}
			continue
		}
		result := asMap(msg["result"])
		switch tc.wantValue {
		case "false":
			if got := fmt.Sprint(result[tc.wantField]); got != "false" {
				t.Fatalf("line %d result[%q]: got %q want false", i, tc.wantField, got)
			}
		case "map":
			if asMap(result[tc.wantField]) == nil {
				t.Fatalf("line %d expected map field %q in result: %#v", i, tc.wantField, result)
			}
		default:
			if got := asString(result[tc.wantField]); got != tc.wantValue {
				t.Fatalf("line %d result[%q]: got %q want %q", i, tc.wantField, got, tc.wantValue)
			}
		}
	}
}

func TestTransport_ShutdownProcess_WithRunningProcess(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=TestTransport_HelperProcess")
	cmd.Env = append(os.Environ(),
		"GO_WANT_TRANSPORT_HELPER=1",
		"GO_TRANSPORT_HELPER_MODE=stdin",
	)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("StdinPipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()

	tp := &stdioTransport{
		cmd:      cmd,
		stdin:    stdin,
		procDone: done,
		opts: TransportOptions{
			ShutdownTimeout: 250 * time.Millisecond,
		},
	}
	if err := tp.shutdownProcess(); err != nil {
		t.Fatalf("shutdownProcess: %v", err)
	}

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("helper process did not exit after shutdown")
	}
}

func TestTransport_HelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_TRANSPORT_HELPER") != "1" {
		return
	}
	switch os.Getenv("GO_TRANSPORT_HELPER_MODE") {
	case "stdin":
		_, _ = io.Copy(io.Discard, os.Stdin)
	case "exit":
		return
	default:
		return
	}
}
