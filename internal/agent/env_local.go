package agent

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/bmatcuk/doublestar/v4"
)

type LocalExecutionEnvironment struct {
	RootDir      string
	BaseEnv      map[string]string
	StripEnvKeys []string
}

func NewLocalExecutionEnvironmentWithPolicy(rootDir string, baseEnv map[string]string, stripKeys []string) *LocalExecutionEnvironment {
	baseCopy := map[string]string{}
	for k, v := range baseEnv {
		baseCopy[k] = v
	}
	stripCopy := make([]string, 0, len(stripKeys))
	for _, k := range stripKeys {
		if strings.TrimSpace(k) == "" {
			continue
		}
		stripCopy = append(stripCopy, k)
	}
	return &LocalExecutionEnvironment{
		RootDir:      rootDir,
		BaseEnv:      baseCopy,
		StripEnvKeys: stripCopy,
	}
}

func NewLocalExecutionEnvironmentWithBaseEnv(rootDir string, baseEnv map[string]string) *LocalExecutionEnvironment {
	return NewLocalExecutionEnvironmentWithPolicy(rootDir, baseEnv, nil)
}

func NewLocalExecutionEnvironment(rootDir string) *LocalExecutionEnvironment {
	return NewLocalExecutionEnvironmentWithBaseEnv(rootDir, nil)
}

func (e *LocalExecutionEnvironment) WorkingDirectory() string { return e.RootDir }

func (e *LocalExecutionEnvironment) Platform() string {
	switch runtime.GOOS {
	case "darwin":
		return "darwin"
	case "windows":
		return "windows"
	default:
		return "linux"
	}
}

func (e *LocalExecutionEnvironment) OSVersion() string { return runtime.GOOS + "/" + runtime.GOARCH }

func (e *LocalExecutionEnvironment) ReadFile(path string, offsetLine *int, limitLines *int) (string, error) {
	abs := e.resolve(path)
	b, err := os.ReadFile(abs)
	if err != nil {
		return "", err
	}
	// Basic binary detection.
	if bytes.IndexByte(b, 0) >= 0 {
		return "", fmt.Errorf("binary file (NUL byte): %s", path)
	}
	s := strings.ReplaceAll(string(b), "\r\n", "\n")
	lines := strings.Split(s, "\n")

	start := 1
	if offsetLine != nil && *offsetLine > 0 {
		start = *offsetLine
	}
	limit := 2000
	if limitLines != nil && *limitLines > 0 {
		limit = *limitLines
	}
	if start > len(lines) {
		return "", nil
	}
	end := start - 1 + limit
	if end > len(lines) {
		end = len(lines)
	}
	var out strings.Builder
	for i := start; i <= end; i++ {
		out.WriteString(fmt.Sprintf("%4d | %s\n", i, lines[i-1]))
	}
	return out.String(), nil
}

func (e *LocalExecutionEnvironment) WriteFile(path string, content string) (string, error) {
	abs := e.resolve(path)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		return "", err
	}
	return fmt.Sprintf("wrote %d bytes to %s", len(content), path), nil
}

func (e *LocalExecutionEnvironment) EditFile(path string, oldString string, newString string, replaceAll bool) (string, error) {
	abs := e.resolve(path)
	b, err := os.ReadFile(abs)
	if err != nil {
		return "", err
	}
	s := string(b)
	if !strings.Contains(s, oldString) {
		return "", fmt.Errorf("old_string not found in %s", path)
	}
	if !replaceAll && strings.Count(s, oldString) != 1 {
		return "", fmt.Errorf("old_string not unique in %s; use replace_all=true or provide a more specific old_string", path)
	}
	n := strings.Count(s, oldString)
	if replaceAll {
		s = strings.ReplaceAll(s, oldString, newString)
	} else {
		s = strings.Replace(s, oldString, newString, 1)
		n = 1
	}
	if err := os.WriteFile(abs, []byte(s), 0o644); err != nil {
		return "", err
	}
	return fmt.Sprintf("edited %s: %d replacement(s)", path, n), nil
}

func (e *LocalExecutionEnvironment) FileExists(path string) bool {
	_, err := os.Stat(e.resolve(path))
	return err == nil
}

func (e *LocalExecutionEnvironment) ListDirectory(path string, depth int) ([]DirEntry, error) {
	if depth <= 0 {
		depth = 1
	}
	root := e.resolve(path)

	var out []DirEntry
	var walk func(absDir string, relPrefix string, d int) error
	walk = func(absDir string, relPrefix string, d int) error {
		ents, err := os.ReadDir(absDir)
		if err != nil {
			return err
		}
		sort.SliceStable(ents, func(i, j int) bool { return ents[i].Name() < ents[j].Name() })
		for _, ent := range ents {
			name := ent.Name()
			relName := name
			if relPrefix != "" {
				relName = filepath.Join(relPrefix, name)
			}
			de := DirEntry{Name: relName, IsDir: ent.IsDir()}
			if !ent.IsDir() {
				if info, err := ent.Info(); err == nil {
					de.Size = info.Size()
				}
			}
			out = append(out, de)
			if ent.IsDir() && d > 1 {
				if err := walk(filepath.Join(absDir, name), relName, d-1); err != nil {
					return err
				}
			}
		}
		return nil
	}

	if err := walk(root, "", depth); err != nil {
		return nil, err
	}
	return out, nil
}

func (e *LocalExecutionEnvironment) Glob(pattern string, basePath string) ([]string, error) {
	base := strings.TrimSpace(basePath)
	if base == "" {
		base = e.RootDir
	}
	if !filepath.IsAbs(base) {
		base = filepath.Join(e.RootDir, base)
	}
	matches, err := doublestar.Glob(os.DirFS(base), pattern)
	if err != nil {
		return nil, err
	}
	abs := make([]string, 0, len(matches))
	for _, m := range matches {
		abs = append(abs, filepath.Join(base, m))
	}
	sort.SliceStable(abs, func(i, j int) bool {
		fi, _ := os.Stat(abs[i])
		fj, _ := os.Stat(abs[j])
		if fi == nil || fj == nil {
			return abs[i] < abs[j]
		}
		if fi.ModTime() != fj.ModTime() {
			return fi.ModTime().After(fj.ModTime())
		}
		return abs[i] < abs[j]
	})
	return abs, nil
}

func (e *LocalExecutionEnvironment) Grep(pattern string, path string, globFilter string, caseInsensitive bool, maxResults int) (string, error) {
	rg, err := exec.LookPath("rg")
	if err != nil {
		return "", fmt.Errorf("rg not found in PATH")
	}
	dir := strings.TrimSpace(path)
	if dir == "" {
		dir = e.RootDir
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(e.RootDir, dir)
	}

	args := []string{"--no-heading", "--line-number", "--color", "never"}
	if caseInsensitive {
		args = append(args, "-i")
	}
	if strings.TrimSpace(globFilter) != "" {
		args = append(args, "-g", globFilter)
	}
	args = append(args, pattern, dir)

	ctx := context.Background()
	if maxResults <= 0 {
		maxResults = 100
	}
	res, err := e.ExecCommand(ctx, rg+" "+shellEscapeArgs(args...), 10_000, e.RootDir, nil)
	if err == nil {
		// Best-effort cap: keep first maxResults lines.
		lines := strings.Split(res.Stdout, "\n")
		if len(lines) > maxResults {
			lines = lines[:maxResults]
		}
		return strings.Join(lines, "\n"), nil
	}
	// Exit code 1 means "no matches" for rg.
	if res.ExitCode == 1 {
		return "", nil
	}
	return res.Stdout + res.Stderr, err
}

func (e *LocalExecutionEnvironment) ExecCommand(ctx context.Context, command string, timeoutMS int, workingDir string, envVars map[string]string) (ExecResult, error) {
	if timeoutMS <= 0 {
		timeoutMS = 10_000
	}
	dir := strings.TrimSpace(workingDir)
	if dir == "" {
		dir = e.RootDir
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(e.RootDir, dir)
	}

	start := time.Now()
	cmd := exec.Command("bash", "-lc", command)
	cmd.Dir = dir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	mergedEnv := map[string]string{}
	for k, v := range e.BaseEnv {
		mergedEnv[k] = v
	}
	for k, v := range envVars {
		mergedEnv[k] = v
	}
	cmd.Env = filteredEnv(mergedEnv, e.StripEnvKeys)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return ExecResult{ExitCode: 127}, err
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	timedOut := false
	var waitErr error
	select {
	case <-ctx.Done():
		timedOut = true
		waitErr = ctx.Err()
	case err := <-done:
		waitErr = err
	case <-time.After(time.Duration(timeoutMS) * time.Millisecond):
		timedOut = true
		waitErr = context.DeadlineExceeded
	}

	if timedOut {
		terminateProcessGroup(cmd.Process.Pid)
		select {
		case <-done:
			// exited on SIGTERM
		case <-time.After(2 * time.Second):
			killProcessGroup(cmd.Process.Pid)
			// Best-effort: wait a bit for Wait() to return so we don't leak the goroutine.
			select {
			case <-done:
			case <-time.After(2 * time.Second):
			}
		}
	}

	exitCode := 0
	if waitErr != nil {
		if ee, ok := waitErr.(*exec.ExitError); ok {
			exitCode = ee.ExitCode()
		} else if timedOut {
			exitCode = 124
		} else {
			exitCode = 1
		}
	}

	return ExecResult{
		Stdout:     stdout.String(),
		Stderr:     stderr.String(),
		ExitCode:   exitCode,
		TimedOut:   timedOut,
		DurationMS: time.Since(start).Milliseconds(),
	}, waitErr
}

func (e *LocalExecutionEnvironment) resolve(path string) string {
	p := strings.TrimSpace(path)
	if p == "" {
		return e.RootDir
	}
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(e.RootDir, p)
}

func terminateProcessGroup(pid int) {
	if pid <= 0 {
		return
	}
	_ = syscall.Kill(-pid, syscall.SIGTERM)
}

func killProcessGroup(pid int) {
	if pid <= 0 {
		return
	}
	_ = syscall.Kill(-pid, syscall.SIGKILL)
}

func filteredEnv(extra map[string]string, stripKeys []string) []string {
	stripped := map[string]bool{}
	for _, k := range stripKeys {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		stripped[k] = true
		stripped[strings.ToUpper(k)] = true
	}
	isStripped := func(k string) bool {
		if stripped[k] {
			return true
		}
		return stripped[strings.ToUpper(k)]
	}
	deny := func(k string) bool {
		uk := strings.ToUpper(k)
		if strings.Contains(uk, "API_KEY") || strings.Contains(uk, "SECRET") || strings.Contains(uk, "TOKEN") || strings.Contains(uk, "PASSWORD") || strings.Contains(uk, "CREDENTIAL") {
			return true
		}
		return false
	}
	allow := map[string]bool{
		"PATH":       true,
		"HOME":       true,
		"USER":       true,
		"SHELL":      true,
		"LANG":       true,
		"TERM":       true,
		"TMPDIR":     true,
		"GOPATH":     true,
		"GOMODCACHE": true,
	}
	out := []string{}
	for _, kv := range os.Environ() {
		k, _, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		if isStripped(k) {
			continue
		}
		if allow[k] && !deny(k) {
			out = append(out, kv)
			continue
		}
		if deny(k) {
			continue
		}
		// Keep non-sensitive env vars by default.
		out = append(out, kv)
	}
	for k, v := range extra {
		if isStripped(k) {
			continue
		}
		if deny(k) {
			continue
		}
		out = append(out, k+"="+v)
	}
	return out
}

func shellEscapeArgs(args ...string) string {
	var b strings.Builder
	for i, a := range args {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(shellEscape(a))
	}
	return b.String()
}

func shellEscape(s string) string {
	if s == "" {
		return "''"
	}
	if strings.IndexFunc(s, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '\n' || r == '"' || r == '\'' || r == '\\' || r == '$' || r == '`' || r == '!' || r == '(' || r == ')' || r == ';' || r == '|' || r == '&' || r == '<' || r == '>' || r == '*'
	}) == -1 {
		return s
	}
	// Single-quote escape strategy for bash.
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}
