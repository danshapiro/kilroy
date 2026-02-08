package engine

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/strongdm/kilroy/internal/cxdb"
)

const (
	defaultCXDBAutostartWait = 20 * time.Second
	defaultCXDBAutostartPoll = 250 * time.Millisecond
	cxdbProbeTimeout         = 2 * time.Second
	uiProbeTimeout           = 1200 * time.Millisecond
	uiLogDiscoveryWait       = 10 * time.Second
	uiLogDiscoveryPoll       = 200 * time.Millisecond
)

var uiURLRegex = regexp.MustCompile(`https?://[^\s"'<>]+`)

type CXDBStartupInfo struct {
	UIURL     string
	UIStarted bool
	Warnings  []string
}

type startedProcess struct {
	PID    int
	waitCh <-chan error
}

func ensureCXDBReady(ctx context.Context, cfg *RunConfigFile, logsRoot string, runID string) (*cxdb.Client, *cxdb.BinaryClient, *CXDBStartupInfo, error) {
	if cfg == nil {
		return nil, nil, nil, fmt.Errorf("config is nil")
	}
	client := cxdb.New(cfg.CXDB.HTTPBaseURL)
	info := &CXDBStartupInfo{
		UIURL: resolveUIURL(ctx, cfg.CXDB.Autostart.UI.URL, cfg.CXDB.HTTPBaseURL),
	}

	connect := func() (*cxdb.BinaryClient, error) {
		probeCtx, cancel := context.WithTimeout(ctx, cxdbProbeTimeout)
		defer cancel()
		if err := client.Health(probeCtx); err != nil {
			return nil, fmt.Errorf("cxdb health failed for %s: %w", cfg.CXDB.HTTPBaseURL, err)
		}
		bin, err := cxdb.DialBinary(probeCtx, cfg.CXDB.BinaryAddr, fmt.Sprintf("kilroy/%s", strings.TrimSpace(runID)))
		if err != nil {
			return nil, fmt.Errorf("cxdb binary dial failed for %s: %w", cfg.CXDB.BinaryAddr, err)
		}
		return bin, nil
	}

	bin, err := connect()
	if err == nil {
		startCXDBUI(ctx, cfg, logsRoot, runID, info)
		return client, bin, info, nil
	}
	if !cfg.CXDB.Autostart.Enabled {
		return nil, nil, nil, fmt.Errorf(
			"cxdb is not reachable (http=%s binary=%s): %w; either start CXDB manually or set cxdb.autostart.enabled=true with cxdb.autostart.command",
			cfg.CXDB.HTTPBaseURL,
			cfg.CXDB.BinaryAddr,
			err,
		)
	}

	logPath := filepath.Join(strings.TrimSpace(logsRoot), "cxdb-autostart.log")
	proc, startErr := startBackgroundCommand(
		cfg.CXDB.Autostart.Command,
		logPath,
		[]string{
			fmt.Sprintf("KILROY_RUN_ID=%s", strings.TrimSpace(runID)),
			fmt.Sprintf("KILROY_CXDB_HTTP_BASE_URL=%s", strings.TrimSpace(cfg.CXDB.HTTPBaseURL)),
			fmt.Sprintf("KILROY_CXDB_BINARY_ADDR=%s", strings.TrimSpace(cfg.CXDB.BinaryAddr)),
			fmt.Sprintf("KILROY_LOGS_ROOT=%s", strings.TrimSpace(logsRoot)),
		},
	)
	if startErr != nil {
		return nil, nil, nil, fmt.Errorf("cxdb autostart failed: %w", startErr)
	}
	info.Warnings = append(info.Warnings, fmt.Sprintf("CXDB autostart launched (pid=%d, log=%s)", proc.PID, logPath))

	waitTimeout := time.Duration(cfg.CXDB.Autostart.WaitTimeoutMS) * time.Millisecond
	if waitTimeout <= 0 {
		waitTimeout = defaultCXDBAutostartWait
	}
	poll := time.Duration(cfg.CXDB.Autostart.PollIntervalMS) * time.Millisecond
	if poll <= 0 {
		poll = defaultCXDBAutostartPoll
	}

	deadline := time.Now().Add(waitTimeout)
	lastErr := err
	for time.Now().Before(deadline) {
		select {
		case procErr := <-proc.waitCh:
			if procErr != nil {
				return nil, nil, nil, fmt.Errorf("cxdb autostart process exited before readiness: %w (log=%s)", procErr, logPath)
			}
			return nil, nil, nil, fmt.Errorf("cxdb autostart process exited before readiness (log=%s)", logPath)
		default:
		}

		bin, err = connect()
		if err == nil {
			startCXDBUI(ctx, cfg, logsRoot, runID, info)
			return client, bin, info, nil
		}
		lastErr = err

		timer := time.NewTimer(poll)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, nil, nil, ctx.Err()
		case procErr := <-proc.waitCh:
			timer.Stop()
			if procErr != nil {
				return nil, nil, nil, fmt.Errorf("cxdb autostart process exited before readiness: %w (log=%s)", procErr, logPath)
			}
			return nil, nil, nil, fmt.Errorf("cxdb autostart process exited before readiness (log=%s)", logPath)
		case <-timer.C:
		}
	}
	return nil, nil, nil, fmt.Errorf(
		"cxdb autostart timed out after %s (http=%s binary=%s): %w (log=%s)",
		waitTimeout.String(),
		cfg.CXDB.HTTPBaseURL,
		cfg.CXDB.BinaryAddr,
		lastErr,
		logPath,
	)
}

func startCXDBUI(ctx context.Context, cfg *RunConfigFile, logsRoot string, runID string, info *CXDBStartupInfo) {
	if cfg == nil || info == nil {
		return
	}
	if info.UIURL == "" {
		info.UIURL = resolveUIURL(ctx, cfg.CXDB.Autostart.UI.URL, cfg.CXDB.HTTPBaseURL)
	}

	uiCmd := resolveUICommand(cfg)
	shouldStart := cfg.CXDB.Autostart.UI.Enabled || len(uiCmd) > 0
	if !shouldStart {
		if info.UIURL != "" {
			info.Warnings = append(info.Warnings, fmt.Sprintf("CXDB UI available at %s", info.UIURL))
		}
		return
	}
	if len(uiCmd) == 0 {
		if info.UIURL != "" {
			info.Warnings = append(info.Warnings, fmt.Sprintf("CXDB UI available at %s", info.UIURL))
		} else {
			info.Warnings = append(info.Warnings, "CXDB UI autostart requested but no command configured (set cxdb.autostart.ui.command or KILROY_CXDB_UI_COMMAND)")
		}
		return
	}

	logPath := filepath.Join(strings.TrimSpace(logsRoot), "cxdb-ui-autostart.log")
	proc, err := startBackgroundCommand(
		uiCmd,
		logPath,
		[]string{
			fmt.Sprintf("KILROY_RUN_ID=%s", strings.TrimSpace(runID)),
			fmt.Sprintf("KILROY_CXDB_HTTP_BASE_URL=%s", strings.TrimSpace(cfg.CXDB.HTTPBaseURL)),
			fmt.Sprintf("KILROY_CXDB_BINARY_ADDR=%s", strings.TrimSpace(cfg.CXDB.BinaryAddr)),
			fmt.Sprintf("KILROY_CXDB_UI_URL=%s", strings.TrimSpace(info.UIURL)),
			fmt.Sprintf("KILROY_LOGS_ROOT=%s", strings.TrimSpace(logsRoot)),
		},
	)
	if err != nil {
		info.Warnings = append(info.Warnings, fmt.Sprintf("CXDB UI launch failed: %v", err))
		return
	}
	info.UIStarted = true
	info.Warnings = append(info.Warnings, fmt.Sprintf("CXDB UI launch command started (pid=%d, log=%s)", proc.PID, logPath))

	if info.UIURL == "" {
		info.UIURL = discoverUIURLFromLog(ctx, logPath, uiLogDiscoveryWait)
	}
	if info.UIURL == "" {
		info.Warnings = append(info.Warnings, "CXDB UI URL not detected automatically; set cxdb.autostart.ui.url (or KILROY_CXDB_UI_URL) to print a direct link")
		return
	}
	info.Warnings = append(info.Warnings, fmt.Sprintf("CXDB UI available at %s", info.UIURL))
}

func resolveUICommand(cfg *RunConfigFile) []string {
	if cfg == nil {
		return nil
	}
	if cmd := trimNonEmpty(cfg.CXDB.Autostart.UI.Command); len(cmd) > 0 {
		return cmd
	}
	if shellCmd := strings.TrimSpace(os.Getenv("KILROY_CXDB_UI_COMMAND")); shellCmd != "" {
		return []string{"sh", "-lc", shellCmd}
	}
	return nil
}

func resolveUIURL(ctx context.Context, configuredURL string, cxdbHTTPBaseURL string) string {
	if s := strings.TrimSpace(configuredURL); s != "" {
		return s
	}
	if s := strings.TrimSpace(os.Getenv("KILROY_CXDB_UI_URL")); s != "" {
		return s
	}
	base := strings.TrimSpace(cxdbHTTPBaseURL)
	if base == "" {
		return ""
	}
	if probeUIURL(ctx, base) {
		return base
	}
	return ""
}

func probeUIURL(ctx context.Context, rawURL string) bool {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return false
	}
	reqCtx, cancel := context.WithTimeout(ctx, uiProbeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, rawURL, nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8*1024))
	contentType := strings.ToLower(resp.Header.Get("Content-Type"))
	lowerBody := strings.ToLower(string(body))
	if strings.Contains(contentType, "text/html") {
		return true
	}
	return strings.Contains(lowerBody, "<html") || strings.Contains(lowerBody, "<!doctype html")
}

func discoverUIURLFromLog(ctx context.Context, logPath string, wait time.Duration) string {
	if strings.TrimSpace(logPath) == "" || wait <= 0 {
		return ""
	}
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ""
		default:
		}

		b, err := os.ReadFile(logPath)
		if err == nil && len(b) > 0 {
			matches := uiURLRegex.FindAllString(string(b), -1)
			for i := len(matches) - 1; i >= 0; i-- {
				u := strings.TrimRight(strings.TrimSpace(matches[i]), ".,;:)]}\"'")
				if probeUIURL(ctx, u) {
					return u
				}
			}
		}
		time.Sleep(uiLogDiscoveryPoll)
	}
	return ""
}

func startBackgroundCommand(parts []string, logPath string, extraEnv []string) (*startedProcess, error) {
	parts = trimNonEmpty(parts)
	if len(parts) == 0 {
		return nil, fmt.Errorf("command is empty")
	}
	cmd := exec.Command(parts[0], parts[1:]...)
	cmd.Env = append(os.Environ(), extraEnv...)

	var logFile *os.File
	if strings.TrimSpace(logPath) != "" {
		if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err == nil {
			f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
			if err == nil {
				logFile = f
			}
		}
	}
	if logFile != nil {
		cmd.Stdout = logFile
		cmd.Stderr = logFile
	} else {
		cmd.Stdout = io.Discard
		cmd.Stderr = io.Discard
	}

	if err := cmd.Start(); err != nil {
		if logFile != nil {
			_ = logFile.Close()
		}
		return nil, fmt.Errorf("%s: %w", strings.Join(parts, " "), err)
	}
	if logFile != nil {
		_ = logFile.Close()
	}

	waitCh := make(chan error, 1)
	go func() {
		waitCh <- cmd.Wait()
		close(waitCh)
	}()
	return &startedProcess{
		PID:    cmd.Process.Pid,
		waitCh: waitCh,
	}, nil
}
