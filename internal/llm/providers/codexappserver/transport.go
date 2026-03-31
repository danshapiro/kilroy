package codexappserver

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/danshapiro/kilroy/internal/llm"
)

const (
	providerName          = "codex-app-server"
	defaultCommand        = "codex"
	defaultConnectTimeout = 15 * time.Second
	// No provider-imposed request cap by default; execution deadlines should come
	// from caller context (for example stage/runtime policy timeouts).
	defaultRequestTimeout   = 0
	defaultShutdownTimeout  = 5 * time.Second
	defaultInterruptTimeout = 2 * time.Second
	defaultStderrTailLimit  = 16 * 1024
	maxJSONRPCLineSize      = 16 * 1024 * 1024
)

var defaultCommandArgs = []string{"app-server", "--listen", "stdio://"}

type TransportOptions struct {
	Command          string
	Args             []string
	CWD              string
	Env              map[string]string
	InitializeParams map[string]any
	ConnectTimeout   time.Duration
	RequestTimeout   time.Duration
	ShutdownTimeout  time.Duration
	StderrTailLimit  int
}

type NotificationStream struct {
	Notifications <-chan map[string]any
	Err           <-chan error
	closeFn       func()
}

func (s *NotificationStream) Close() {
	if s == nil || s.closeFn == nil {
		return
	}
	s.closeFn()
}

type pendingRequest struct {
	method string
	respCh chan pendingResult
}

type pendingResult struct {
	result any
	err    error
}

type processLifecycle struct {
	done chan struct{}
	once sync.Once
	mu   sync.Mutex
	err  error
}

type turnWaitOutcome int

const (
	turnWaitCompleted turnWaitOutcome = iota
	turnWaitContextDone
	turnWaitProcessTerminated
)

func newProcessLifecycle() *processLifecycle {
	return &processLifecycle{done: make(chan struct{})}
}

func (l *processLifecycle) finish(err error) {
	if l == nil {
		return
	}
	l.mu.Lock()
	if l.err == nil {
		l.err = err
	}
	l.mu.Unlock()
	l.once.Do(func() {
		close(l.done)
	})
}

func (l *processLifecycle) doneCh() <-chan struct{} {
	if l == nil {
		return nil
	}
	return l.done
}

func (l *processLifecycle) processError() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.err
}

type stdioTransport struct {
	opts TransportOptions

	mu       sync.Mutex
	writeMu  sync.Mutex
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	stdout   io.ReadCloser
	stderr   io.ReadCloser
	procDone chan struct{}
	life     *processLifecycle

	closed       bool
	shuttingDown bool
	initialized  bool
	initWait     chan struct{}
	initErr      error

	nextID int64

	pending   map[string]*pendingRequest
	listeners map[int]func(map[string]any)
	nextLID   int

	stderrTail string
}

func NewTransport(opts TransportOptions) *stdioTransport {
	opts.Command = strings.TrimSpace(opts.Command)
	if opts.Command == "" {
		opts.Command = defaultCommand
	}
	if len(opts.Args) == 0 {
		opts.Args = append([]string{}, defaultCommandArgs...)
	}
	if opts.ConnectTimeout <= 0 {
		opts.ConnectTimeout = defaultConnectTimeout
	}
	if opts.RequestTimeout <= 0 {
		opts.RequestTimeout = defaultRequestTimeout
	}
	if opts.ShutdownTimeout <= 0 {
		opts.ShutdownTimeout = defaultShutdownTimeout
	}
	if opts.StderrTailLimit <= 0 {
		opts.StderrTailLimit = defaultStderrTailLimit
	}
	if opts.InitializeParams == nil {
		opts.InitializeParams = map[string]any{
			"clientInfo": map[string]any{
				"name":    "unified_llm",
				"title":   "Unified LLM",
				"version": "0.1.0",
			},
		}
	}
	return &stdioTransport{
		opts:      opts,
		pending:   map[string]*pendingRequest{},
		listeners: map[int]func(map[string]any){},
	}
}

func (t *stdioTransport) Initialize(ctx context.Context) error {
	return t.ensureInitialized(ctx)
}

func (t *stdioTransport) Complete(ctx context.Context, payload map[string]any) (map[string]any, error) {
	return t.runTurn(ctx, payload)
}

func (t *stdioTransport) Stream(ctx context.Context, payload map[string]any) (*NotificationStream, error) {
	events := make(chan map[string]any, 128)
	errs := make(chan error, 1)
	sctx, cancel := context.WithCancel(ctx)

	go func() {
		defer close(events)
		defer close(errs)

		if err := t.ensureInitialized(sctx); err != nil {
			errs <- err
			return
		}
		life := t.currentProcessLifecycle()
		if life == nil {
			errs <- llm.NewNetworkError(providerName, "Codex app-server process is unavailable")
			return
		}

		turnTemplate, err := parseTurnStartPayload(payload)
		if err != nil {
			errs <- err
			return
		}

		requestCtx, requestCancel := contextWithRequestTimeout(sctx, t.opts.RequestTimeout)
		defer requestCancel()

		threadResp, err := t.startThread(requestCtx, toThreadStartParams(turnTemplate))
		if err != nil {
			errs <- err
			return
		}
		thread := asMap(threadResp["thread"])
		threadID := asString(thread["id"])
		if threadID == "" {
			errs <- llm.ErrorFromHTTPStatus(providerName, 400, "thread/start response missing thread.id", threadResp, nil)
			return
		}

		turnParams := deepCopyMap(turnTemplate)
		turnParams["threadId"] = threadID

		var (
			stateMu   sync.Mutex
			turnID    string
			completed = make(chan struct{}, 1)
		)

		sendNotification := func(notification map[string]any) {
			select {
			case events <- deepCopyMap(notification):
			case <-requestCtx.Done():
			}
		}

		unsubscribe := t.subscribe(func(notification map[string]any) {
			stateMu.Lock()
			currentTurnID := turnID
			stateMu.Unlock()
			if !notificationBelongsToTurn(notification, threadID, currentTurnID) {
				return
			}
			sendNotification(notification)
			notificationTurnID := extractTurnID(notification)
			if notificationTurnID != "" {
				stateMu.Lock()
				if turnID == "" {
					turnID = notificationTurnID
				}
				currentTurnID = turnID
				stateMu.Unlock()
			}
			if asString(notification["method"]) == "turn/completed" {
				if currentTurnID == "" || notificationTurnID == "" || notificationTurnID == currentTurnID {
					select {
					case completed <- struct{}{}:
					default:
					}
				}
			}
		})
		defer unsubscribe()

		turnResp, err := t.startTurn(requestCtx, turnParams)
		if err != nil {
			errs <- err
			return
		}
		turn := asMap(turnResp["turn"])
		if tid := asString(turn["id"]); tid != "" {
			stateMu.Lock()
			turnID = tid
			stateMu.Unlock()
		}
		if isTerminalTurnStatus(asString(turn["status"])) {
			sendNotification(map[string]any{
				"method": "turn/completed",
				"params": map[string]any{
					"threadId": threadID,
					"turn":     turn,
				},
			})
			select {
			case completed <- struct{}{}:
			default:
			}
		}

		outcome, waitErr := t.waitForTurnCompletion(requestCtx, completed, life)
		if waitErr == nil {
			return
		}
		if outcome == turnWaitContextDone {
			stateMu.Lock()
			currentTurnID := turnID
			stateMu.Unlock()
			if currentTurnID != "" {
				go t.interruptTurnBestEffort(threadID, currentTurnID)
			}
		}
		errs <- waitErr
		return
	}()

	return &NotificationStream{Notifications: events, Err: errs, closeFn: cancel}, nil
}

func (t *stdioTransport) ListModels(ctx context.Context, params map[string]any) (modelListResponse, error) {
	if err := t.ensureInitialized(ctx); err != nil {
		return modelListResponse{}, err
	}
	if params == nil {
		params = map[string]any{}
	}
	result, err := t.sendRequest(ctx, "model/list", params, t.opts.RequestTimeout)
	if err != nil {
		return modelListResponse{}, err
	}
	b, err := json.Marshal(result)
	if err != nil {
		return modelListResponse{}, err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var out modelListResponse
	if err := dec.Decode(&out); err != nil {
		return modelListResponse{}, err
	}
	if out.Data == nil {
		out.Data = []modelEntry{}
	}
	return out, nil
}

func (t *stdioTransport) Close() error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil
	}
	t.closed = true
	t.mu.Unlock()

	t.rejectAllPending(llm.NewNetworkError(providerName, "Codex transport closed"))
	return t.shutdownProcess()
}

func (t *stdioTransport) runTurn(ctx context.Context, payload map[string]any) (map[string]any, error) {
	if err := t.ensureInitialized(ctx); err != nil {
		return nil, err
	}
	life := t.currentProcessLifecycle()
	if life == nil {
		return nil, llm.NewNetworkError(providerName, "Codex app-server process is unavailable")
	}

	turnTemplate, err := parseTurnStartPayload(payload)
	if err != nil {
		return nil, err
	}

	requestCtx, requestCancel := contextWithRequestTimeout(ctx, t.opts.RequestTimeout)
	defer requestCancel()

	threadResp, err := t.startThread(requestCtx, toThreadStartParams(turnTemplate))
	if err != nil {
		return nil, err
	}
	thread := asMap(threadResp["thread"])
	threadID := asString(thread["id"])
	if threadID == "" {
		return nil, llm.ErrorFromHTTPStatus(providerName, 400, "thread/start response missing thread.id", threadResp, nil)
	}

	turnParams := deepCopyMap(turnTemplate)
	turnParams["threadId"] = threadID

	var (
		stateMu       sync.Mutex
		notifications []map[string]any
		turnID        string
		completed     = make(chan struct{}, 1)
	)

	unsubscribe := t.subscribe(func(notification map[string]any) {
		stateMu.Lock()
		currentTurnID := turnID
		stateMu.Unlock()
		if !notificationBelongsToTurn(notification, threadID, currentTurnID) {
			return
		}
		stateMu.Lock()
		notifications = append(notifications, deepCopyMap(notification))
		notificationTurnID := extractTurnID(notification)
		if turnID == "" && notificationTurnID != "" {
			turnID = notificationTurnID
		}
		currentTurnID = turnID
		stateMu.Unlock()
		if asString(notification["method"]) == "turn/completed" {
			if currentTurnID == "" || notificationTurnID == "" || notificationTurnID == currentTurnID {
				select {
				case completed <- struct{}{}:
				default:
				}
			}
		}
	})
	defer unsubscribe()

	turnResp, err := t.startTurn(requestCtx, turnParams)
	if err != nil {
		return nil, err
	}
	turn := asMap(turnResp["turn"])
	if tid := asString(turn["id"]); tid != "" {
		stateMu.Lock()
		turnID = tid
		stateMu.Unlock()
	}
	if isTerminalTurnStatus(asString(turn["status"])) {
		select {
		case completed <- struct{}{}:
		default:
		}
	}

	outcome, waitErr := t.waitForTurnCompletion(requestCtx, completed, life)
	if waitErr != nil {
		if outcome == turnWaitContextDone {
			stateMu.Lock()
			currentTurnID := turnID
			stateMu.Unlock()
			if currentTurnID != "" {
				go t.interruptTurnBestEffort(threadID, currentTurnID)
			}
		}
		return nil, waitErr
	}

	stateMu.Lock()
	capturedNotifications := append([]map[string]any{}, notifications...)
	capturedTurnID := turnID
	stateMu.Unlock()

	completedTurn := findCompletedTurn(capturedNotifications, capturedTurnID)
	if completedTurn == nil {
		completedTurn = turn
	}
	result := map[string]any{
		"thread":         thread,
		"turn":           completedTurn,
		"threadId":       threadID,
		"turnId":         firstNonEmpty(capturedTurnID, asString(completedTurn["id"])),
		"notifications":  capturedNotifications,
		"threadResponse": threadResp,
		"turnResponse":   turnResp,
	}
	return result, nil
}

func (t *stdioTransport) startThread(ctx context.Context, params map[string]any) (map[string]any, error) {
	return t.sendRequest(ctx, "thread/start", params, t.opts.RequestTimeout)
}

func (t *stdioTransport) startTurn(ctx context.Context, params map[string]any) (map[string]any, error) {
	return t.sendRequest(ctx, "turn/start", params, t.opts.RequestTimeout)
}

func (t *stdioTransport) interruptTurn(ctx context.Context, params map[string]any) error {
	_, err := t.sendRequest(ctx, "turn/interrupt", params, t.opts.RequestTimeout)
	return err
}

func (t *stdioTransport) interruptTurnBestEffort(threadID, turnID string) {
	if strings.TrimSpace(threadID) == "" || strings.TrimSpace(turnID) == "" {
		return
	}
	timeout := t.interruptTimeout()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	_ = t.interruptTurn(ctx, map[string]any{"threadId": threadID, "turnId": turnID})
}

func (t *stdioTransport) interruptTimeout() time.Duration {
	timeout := t.opts.RequestTimeout
	if timeout <= 0 {
		timeout = defaultInterruptTimeout
	}
	if t.opts.ShutdownTimeout > 0 && t.opts.ShutdownTimeout < timeout {
		timeout = t.opts.ShutdownTimeout
	}
	return timeout
}

func (t *stdioTransport) currentProcessLifecycle() *processLifecycle {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.life
}

func (t *stdioTransport) processTerminationError(life *processLifecycle) error {
	if life != nil {
		if err := life.processError(); err != nil {
			return err
		}
	}
	return llm.NewNetworkError(providerName, "Codex app-server process exited")
}

func (t *stdioTransport) waitForTurnCompletion(ctx context.Context, completed <-chan struct{}, life *processLifecycle) (turnWaitOutcome, error) {
	if life == nil {
		return turnWaitProcessTerminated, llm.NewNetworkError(providerName, "Codex app-server process is unavailable")
	}
	select {
	case <-completed:
		return turnWaitCompleted, nil
	case <-ctx.Done():
		return turnWaitContextDone, llm.WrapContextError(providerName, ctx.Err())
	case <-life.doneCh():
		return turnWaitProcessTerminated, t.processTerminationError(life)
	}
}

func (t *stdioTransport) ensureInitialized(ctx context.Context) error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return llm.NewNetworkError(providerName, "Codex transport is closed")
	}
	if t.initialized {
		t.mu.Unlock()
		return nil
	}
	if t.initWait != nil {
		wait := t.initWait
		t.mu.Unlock()
		select {
		case <-wait:
			t.mu.Lock()
			err := t.initErr
			t.mu.Unlock()
			return err
		case <-ctx.Done():
			return llm.WrapContextError(providerName, ctx.Err())
		}
	}

	wait := make(chan struct{})
	t.initWait = wait
	t.mu.Unlock()

	err := t.startAndInitialize(ctx)

	t.mu.Lock()
	if err == nil {
		t.initialized = true
	}
	t.initErr = err
	close(wait)
	t.initWait = nil
	t.mu.Unlock()
	return err
}

func (t *stdioTransport) startAndInitialize(ctx context.Context) error {
	if err := t.spawnProcess(); err != nil {
		return err
	}
	connCtx, cancel := contextWithRequestTimeout(ctx, t.opts.ConnectTimeout)
	defer cancel()
	if _, err := t.sendRequest(connCtx, "initialize", t.opts.InitializeParams, t.opts.ConnectTimeout); err != nil {
		_ = t.shutdownProcess()
		return err
	}
	if err := t.sendNotification(connCtx, "initialized", nil); err != nil {
		_ = t.shutdownProcess()
		return err
	}
	return nil
}

func (t *stdioTransport) spawnProcess() error {
	t.mu.Lock()
	if t.cmd != nil && processAlive(t.cmd) {
		t.mu.Unlock()
		return nil
	}
	if t.closed {
		t.mu.Unlock()
		return llm.NewNetworkError(providerName, "Codex transport is closed")
	}
	t.mu.Unlock()

	cmd := exec.Command(t.opts.Command, t.opts.Args...)
	if strings.TrimSpace(t.opts.CWD) != "" {
		cmd.Dir = t.opts.CWD
	}
	if len(t.opts.Env) > 0 {
		env := os.Environ()
		for key, value := range t.opts.Env {
			env = append(env, key+"="+value)
		}
		cmd.Env = env
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return llm.NewNetworkError(providerName, fmt.Sprintf("failed to open stdin pipe: %v", err))
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return llm.NewNetworkError(providerName, fmt.Sprintf("failed to open stdout pipe: %v", err))
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return llm.NewNetworkError(providerName, fmt.Sprintf("failed to open stderr pipe: %v", err))
	}

	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		_ = stderr.Close()
		return llm.NewNetworkError(providerName, fmt.Sprintf("failed to spawn codex app-server: %v", err))
	}

	procDone := make(chan struct{})
	life := newProcessLifecycle()
	t.mu.Lock()
	t.cmd = cmd
	t.stdin = stdin
	t.stdout = stdout
	t.stderr = stderr
	t.procDone = procDone
	t.life = life
	t.stderrTail = ""
	t.initialized = false
	t.mu.Unlock()

	go t.readStdout(cmd, stdout, life)
	go t.readStderr(stderr)
	go t.waitForExit(cmd, procDone, life)

	return nil
}

func (t *stdioTransport) readStdout(cmd *exec.Cmd, stdout io.Reader, life *processLifecycle) {
	scanner := bufio.NewScanner(stdout)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, maxJSONRPCLineSize)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		dec := json.NewDecoder(strings.NewReader(line))
		dec.UseNumber()
		var message map[string]any
		if err := dec.Decode(&message); err != nil {
			continue
		}
		t.handleIncomingMessage(message)
	}
	if err := scanner.Err(); err != nil {
		t.handleUnexpectedProcessTermination(life, llm.NewNetworkError(providerName, fmt.Sprintf("Codex stdout read error: %v", err)))
	}
	_ = cmd
}

func (t *stdioTransport) readStderr(stderr io.Reader) {
	buf := make([]byte, 4096)
	for {
		n, err := stderr.Read(buf)
		if n > 0 {
			t.appendStderrTail(string(buf[:n]))
		}
		if err != nil {
			return
		}
	}
}

func (t *stdioTransport) appendStderrTail(chunk string) {
	if chunk == "" {
		return
	}
	t.mu.Lock()
	t.stderrTail += chunk
	if len(t.stderrTail) > t.opts.StderrTailLimit {
		t.stderrTail = t.stderrTail[len(t.stderrTail)-t.opts.StderrTailLimit:]
	}
	t.mu.Unlock()
}

func (t *stdioTransport) waitForExit(cmd *exec.Cmd, done chan struct{}, life *processLifecycle) {
	err := cmd.Wait()
	t.mu.Lock()
	shuttingDown := t.shuttingDown
	closed := t.closed
	stderrTail := strings.TrimSpace(t.stderrTail)
	if t.cmd == cmd {
		t.cmd = nil
		t.stdin = nil
		t.stdout = nil
		t.stderr = nil
		t.procDone = nil
		t.life = nil
		t.initialized = false
	}
	t.shuttingDown = false
	t.mu.Unlock()
	close(done)

	exitMessage := "Codex app-server process exited"
	if err != nil {
		exitMessage = fmt.Sprintf("Codex app-server process exited: %v", err)
	}
	if stderrTail != "" {
		exitMessage = exitMessage + ". stderr: " + stderrTail
	}
	exitErr := llm.NewNetworkError(providerName, exitMessage)
	life.finish(exitErr)

	if shuttingDown || closed {
		return
	}
	message := "Codex app-server exited unexpectedly"
	if err != nil {
		message = fmt.Sprintf("Codex app-server exited unexpectedly: %v", err)
	}
	if stderrTail != "" {
		message = message + ". stderr: " + stderrTail
	}
	t.handleUnexpectedProcessTermination(life, llm.NewNetworkError(providerName, message))
}

func (t *stdioTransport) handleUnexpectedProcessTermination(life *processLifecycle, err error) {
	life.finish(err)
	t.rejectAllPending(err)
}

func (t *stdioTransport) handleIncomingMessage(message map[string]any) {
	id, hasID := message["id"]
	_, hasResult := message["result"]
	errorObj := asMap(message["error"])

	if hasID && hasResult {
		t.resolvePendingRequest(id, pendingResult{result: message["result"]})
		return
	}
	if hasID && errorObj != nil {
		t.resolvePendingRequest(id, pendingResult{err: t.toRPCError(asString(message["method"]), errorObj)})
		return
	}

	method := strings.TrimSpace(asString(message["method"]))
	if method == "" {
		return
	}
	if hasID {
		go t.handleServerRequest(id, method, message["params"])
		return
	}
	notification := map[string]any{"method": method}
	if params := asMap(message["params"]); params != nil {
		notification["params"] = params
	}
	t.emitNotification(notification)
}

func (t *stdioTransport) emitNotification(notification map[string]any) {
	t.mu.Lock()
	listeners := make([]func(map[string]any), 0, len(t.listeners))
	for _, listener := range t.listeners {
		listeners = append(listeners, listener)
	}
	t.mu.Unlock()
	for _, listener := range listeners {
		func(l func(map[string]any)) {
			defer func() { _ = recover() }()
			l(notification)
		}(listener)
	}
}

func (t *stdioTransport) subscribe(listener func(map[string]any)) func() {
	t.mu.Lock()
	id := t.nextLID
	t.nextLID++
	t.listeners[id] = listener
	t.mu.Unlock()
	return func() {
		t.mu.Lock()
		delete(t.listeners, id)
		t.mu.Unlock()
	}
}

func (t *stdioTransport) resolvePendingRequest(id any, result pendingResult) {
	key := rpcIDKey(id)
	t.mu.Lock()
	pending := t.pending[key]
	if pending != nil {
		delete(t.pending, key)
	}
	t.mu.Unlock()
	if pending == nil {
		return
	}
	select {
	case pending.respCh <- result:
	default:
	}
}

func (t *stdioTransport) rejectAllPending(err error) {
	t.mu.Lock()
	pending := t.pending
	t.pending = map[string]*pendingRequest{}
	t.mu.Unlock()
	for _, req := range pending {
		if req == nil {
			continue
		}
		select {
		case req.respCh <- pendingResult{err: err}:
		default:
		}
	}
}

func (t *stdioTransport) sendRequest(ctx context.Context, method string, params any, timeout time.Duration) (map[string]any, error) {
	requestCtx, cancel := contextWithRequestTimeout(ctx, timeout)
	defer cancel()
	if err := requestCtx.Err(); err != nil {
		return nil, llm.WrapContextError(providerName, err)
	}

	id := atomic.AddInt64(&t.nextID, 1)
	idKey := rpcIDKey(id)
	respCh := make(chan pendingResult, 1)

	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil, llm.NewNetworkError(providerName, "Codex transport is closed")
	}
	t.pending[idKey] = &pendingRequest{method: method, respCh: respCh}
	t.mu.Unlock()

	request := map[string]any{"id": id, "method": method}
	if params != nil {
		request["params"] = params
	}
	if err := t.writeJSONLine(request); err != nil {
		t.mu.Lock()
		delete(t.pending, idKey)
		t.mu.Unlock()
		return nil, err
	}

	select {
	case result := <-respCh:
		if result.err != nil {
			return nil, result.err
		}
		if m := asMap(result.result); m != nil {
			return m, nil
		}
		if result.result == nil {
			return map[string]any{}, nil
		}
		b, err := json.Marshal(result.result)
		if err != nil {
			return nil, llm.NewNetworkError(providerName, fmt.Sprintf("invalid RPC result for %s: %v", method, err))
		}
		return decodeJSONToMap(b), nil
	case <-requestCtx.Done():
		t.mu.Lock()
		delete(t.pending, idKey)
		t.mu.Unlock()
		return nil, llm.WrapContextError(providerName, requestCtx.Err())
	}
}

func (t *stdioTransport) sendNotification(ctx context.Context, method string, params any) error {
	if err := ctx.Err(); err != nil {
		return llm.WrapContextError(providerName, err)
	}
	message := map[string]any{"method": method}
	if params != nil {
		message["params"] = params
	}
	return t.writeJSONLine(message)
}

func (t *stdioTransport) writeJSONLine(message map[string]any) error {
	b, err := json.Marshal(message)
	if err != nil {
		return llm.NewNetworkError(providerName, fmt.Sprintf("failed to marshal RPC message: %v", err))
	}
	line := append(b, '\n')

	t.mu.Lock()
	stdin := t.stdin
	cmd := t.cmd
	t.mu.Unlock()
	if stdin == nil || cmd == nil || !processAlive(cmd) {
		return llm.NewNetworkError(providerName, "Codex app-server stdin is not writable")
	}

	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	if _, err := stdin.Write(line); err != nil {
		return llm.NewNetworkError(providerName, fmt.Sprintf("failed to write to codex app-server: %v", err))
	}
	return nil
}

func (t *stdioTransport) toRPCError(method string, errObj map[string]any) error {
	code := asInt(errObj["code"], 0)
	message := firstNonEmpty(asString(errObj["message"]), "RPC error")
	wrapped := fmt.Sprintf("Codex RPC %s failed (%d): %s", method, code, message)
	switch code {
	case -32700, -32600, -32601, -32602:
		return llm.ErrorFromHTTPStatus(providerName, 400, wrapped, errObj["data"], nil)
	default:
		return llm.ErrorFromHTTPStatus(providerName, 500, wrapped, errObj["data"], nil)
	}
}

func (t *stdioTransport) handleServerRequest(id any, method string, params any) {
	sendSuccess := func(result any) {
		_ = t.writeJSONLine(map[string]any{"id": id, "result": result})
	}
	sendError := func(code int, message string, data any) {
		errObj := map[string]any{"code": code, "message": message}
		if data != nil {
			errObj["data"] = data
		}
		_ = t.writeJSONLine(map[string]any{"id": id, "error": errObj})
	}

	switch method {
	case "item/tool/call":
		sendSuccess(map[string]any{"contentItems": []any{}, "success": false})
	case "item/tool/requestUserInput":
		sendSuccess(buildDefaultUserInputResponse(params))
	case "item/commandExecution/requestApproval":
		sendSuccess(map[string]any{"decision": "decline"})
	case "item/fileChange/requestApproval":
		sendSuccess(map[string]any{"decision": "decline"})
	case "applyPatchApproval":
		sendSuccess(map[string]any{"decision": "denied"})
	case "execCommandApproval":
		sendSuccess(map[string]any{"decision": "denied"})
	case "account/chatgptAuthTokens/refresh":
		sendError(-32001, "External ChatGPT auth token refresh is not configured", nil)
	default:
		sendError(-32601, "Method not found: "+method, nil)
	}
}

func buildDefaultUserInputResponse(params any) map[string]any {
	answers := map[string]any{}
	p := asMap(params)
	if p == nil {
		return map[string]any{"answers": answers}
	}
	for _, questionRaw := range asSlice(p["questions"]) {
		question := asMap(questionRaw)
		if question == nil {
			continue
		}
		id := asString(question["id"])
		if id == "" {
			continue
		}
		answers[id] = map[string]any{"answers": []any{}}
	}
	return map[string]any{"answers": answers}
}

func (t *stdioTransport) shutdownProcess() error {
	t.mu.Lock()
	cmd := t.cmd
	stdin := t.stdin
	done := t.procDone
	t.shuttingDown = true
	t.mu.Unlock()

	if cmd == nil {
		return nil
	}
	if stdin != nil {
		_ = stdin.Close()
	}
	if cmd.Process != nil {
		_ = cmd.Process.Signal(os.Interrupt)
	}

	if done != nil {
		select {
		case <-done:
			return nil
		case <-time.After(t.opts.ShutdownTimeout):
		}
	}

	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	if done != nil {
		select {
		case <-done:
		case <-time.After(time.Second):
		}
	}
	return nil
}

func parseTurnStartPayload(payload map[string]any) (map[string]any, error) {
	if payload == nil {
		return nil, llm.ErrorFromHTTPStatus(providerName, 400, "codex-app-server turn payload must be an object", nil, nil)
	}
	input := asSlice(payload["input"])
	if input == nil {
		return nil, llm.ErrorFromHTTPStatus(providerName, 400, "codex-app-server turn payload is missing input array", payload, nil)
	}
	out := deepCopyMap(payload)
	if strings.TrimSpace(asString(out["threadId"])) == "" {
		out["threadId"] = defaultThreadID
	}
	out["input"] = input
	return out, nil
}

func toThreadStartParams(turn map[string]any) map[string]any {
	thread := map[string]any{}
	for _, key := range []string{"model", "cwd", "approvalPolicy", "personality"} {
		if v, ok := turn[key]; ok && v != nil {
			thread[key] = v
		}
	}
	if sandbox := asString(turn["sandbox"]); sandbox == "read-only" || sandbox == "workspace-write" || sandbox == "danger-full-access" {
		thread["sandbox"] = sandbox
	}
	return thread
}

func isTerminalTurnStatus(status string) bool {
	switch strings.TrimSpace(status) {
	case "completed", "failed", "interrupted":
		return true
	default:
		return false
	}
}

func notificationBelongsToTurn(notification map[string]any, threadID, turnID string) bool {
	notificationThreadID := extractThreadID(notification)
	if notificationThreadID != "" && notificationThreadID != threadID {
		return false
	}
	if turnID == "" {
		return true
	}
	notificationTurnID := extractTurnID(notification)
	if notificationTurnID != "" && notificationTurnID != turnID {
		return false
	}
	return true
}

func extractThreadID(notification map[string]any) string {
	params := asMap(notification["params"])
	if params == nil {
		return ""
	}
	if threadID := asString(params["threadId"]); threadID != "" {
		return threadID
	}
	if threadID := asString(params["thread_id"]); threadID != "" {
		return threadID
	}
	return ""
}

func extractTurnID(notification map[string]any) string {
	params := asMap(notification["params"])
	if params == nil {
		return ""
	}
	if turnID := asString(params["turnId"]); turnID != "" {
		return turnID
	}
	if turnID := asString(params["turn_id"]); turnID != "" {
		return turnID
	}
	turn := asMap(params["turn"])
	if turn != nil {
		if turnID := asString(turn["id"]); turnID != "" {
			return turnID
		}
	}
	return ""
}

func findCompletedTurn(notifications []map[string]any, turnID string) map[string]any {
	for idx := len(notifications) - 1; idx >= 0; idx-- {
		notification := notifications[idx]
		if asString(notification["method"]) != "turn/completed" {
			continue
		}
		notificationTurnID := extractTurnID(notification)
		if turnID != "" && notificationTurnID != "" && notificationTurnID != turnID {
			continue
		}
		turn := asMap(asMap(notification["params"])["turn"])
		if turn == nil {
			continue
		}
		if asString(turn["id"]) == "" {
			continue
		}
		if asSlice(turn["items"]) == nil {
			continue
		}
		return turn
	}
	return nil
}

func contextWithRequestTimeout(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		return context.WithCancel(ctx)
	}
	if deadline, ok := ctx.Deadline(); ok {
		if time.Until(deadline) <= timeout {
			return context.WithCancel(ctx)
		}
	}
	return context.WithTimeout(ctx, timeout)
}

func processAlive(cmd *exec.Cmd) bool {
	if cmd == nil {
		return false
	}
	if cmd.ProcessState != nil && cmd.ProcessState.Exited() {
		return false
	}
	if cmd.Process == nil {
		return false
	}
	return true
}

func rpcIDKey(id any) string {
	return strings.TrimSpace(fmt.Sprintf("%v", id))
}
