package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/danshapiro/kilroy/internal/attractor/engine"
	"github.com/danshapiro/kilroy/internal/providerspec"
	"github.com/danshapiro/kilroy/internal/version"
)

const (
	skipCLIHeadlessWarningFlag = "--skip-cli-headless-warning"
)

func signalCancelContext() (context.Context, func()) {
	ctx, cancel := context.WithCancelCause(context.Background())
	sigCh := make(chan os.Signal, 1)
	stopCh := make(chan struct{})
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		for {
			select {
			case sig := <-sigCh:
				cancel(fmt.Errorf("stopped by signal %s", sig.String()))
			case <-stopCh:
				return
			}
		}
	}()
	cleanup := func() {
		signal.Stop(sigCh)
		close(stopCh)
		cancel(nil)
	}
	return ctx, cleanup
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "--version", "-v", "version":
		fmt.Printf("kilroy %s\n", version.Version)
		os.Exit(0)
	case "attractor":
		attractor(os.Args[2:])
	default:
		usage()
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage:")
	fmt.Fprintln(os.Stderr, "  kilroy --version")
	fmt.Fprintln(os.Stderr, "  kilroy attractor run [--detach] [--allow-test-shim] [--confirm-stale-build] [--no-cxdb] [--force-model <provider=model>] --graph <file.dot> --config <run.yaml> [--run-id <id>] [--logs-root <dir>]")
	fmt.Fprintln(os.Stderr, "  kilroy attractor resume --logs-root <dir>")
	fmt.Fprintln(os.Stderr, "  kilroy attractor resume --cxdb <http_base_url> --context-id <id>")
	fmt.Fprintln(os.Stderr, "  kilroy attractor resume --run-branch <attractor/run/...> [--repo <path>]")
	fmt.Fprintln(os.Stderr, "  kilroy attractor status [--logs-root <dir> | --latest] [--json] [--follow|-f] [--cxdb] [--raw] [--watch] [--interval <sec>]")
	fmt.Fprintln(os.Stderr, "  kilroy attractor stop --logs-root <dir> [--grace-ms <ms>] [--force]")
	fmt.Fprintln(os.Stderr, "  kilroy attractor validate --graph <file.dot>")
	fmt.Fprintln(os.Stderr, "  kilroy attractor ingest [--output <file.dot>] [--model <model>] [--skill <skill.md>] [--repo <path>] [--max-turns <n>] <requirements>")
	fmt.Fprintln(os.Stderr, "  kilroy attractor serve [--addr <host:port>]")
}

func attractor(args []string) {
	if len(args) < 1 {
		usage()
		os.Exit(1)
	}
	switch args[0] {
	case "run":
		attractorRun(args[1:])
	case "resume":
		attractorResume(args[1:])
	case "status":
		attractorStatus(args[1:])
	case "stop":
		attractorStop(args[1:])
	case "validate":
		attractorValidate(args[1:])
	case "ingest":
		attractorIngest(args[1:])
	case "serve":
		attractorServe(args[1:])
	default:
		usage()
		os.Exit(1)
	}
}

func attractorRun(args []string) {
	var graphPath string
	var configPath string
	var runID string
	var logsRoot string
	var detach bool
	var allowTestShim bool
	var confirmStaleBuild bool
	var noCXDB bool
	var skipCLIHeadlessWarning bool
	var forceModelSpecs []string

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--detach":
			detach = true
		case "--allow-test-shim":
			allowTestShim = true
		case "--confirm-stale-build":
			confirmStaleBuild = true
		case "--no-cxdb":
			noCXDB = true
		case skipCLIHeadlessWarningFlag:
			skipCLIHeadlessWarning = true
		case "--force-model":
			i++
			if i >= len(args) {
				fmt.Fprintln(os.Stderr, "--force-model requires a value in the form provider=model")
				os.Exit(1)
			}
			forceModelSpecs = append(forceModelSpecs, args[i])
		case "--graph":
			i++
			if i >= len(args) {
				fmt.Fprintln(os.Stderr, "--graph requires a value")
				os.Exit(1)
			}
			graphPath = args[i]
		case "--config":
			i++
			if i >= len(args) {
				fmt.Fprintln(os.Stderr, "--config requires a value")
				os.Exit(1)
			}
			configPath = args[i]
		case "--run-id":
			i++
			if i >= len(args) {
				fmt.Fprintln(os.Stderr, "--run-id requires a value")
				os.Exit(1)
			}
			runID = args[i]
		case "--logs-root":
			i++
			if i >= len(args) {
				fmt.Fprintln(os.Stderr, "--logs-root requires a value")
				os.Exit(1)
			}
			logsRoot = args[i]
		default:
			fmt.Fprintf(os.Stderr, "unknown arg: %s\n", args[i])
			os.Exit(1)
		}
	}

	if graphPath == "" || configPath == "" {
		usage()
		os.Exit(1)
	}
	if err := ensureFreshKilroyBuild(confirmStaleBuild); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	forceModels, canonicalForceSpecs, err := parseForceModelFlags(forceModelSpecs)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	if detach {
		cfg, err := engine.LoadRunConfigFile(configPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if !skipCLIHeadlessWarning && runConfigUsesCLIProviders(cfg) {
			if !confirmCLIHeadlessWarning(os.Stdin, os.Stderr) {
				fmt.Fprintln(os.Stderr, "preflight aborted: declined provider CLI headless-risk warning")
				os.Exit(1)
			}
		}

		if runID == "" {
			id, err := engine.NewRunID()
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			runID = id
		}
		if logsRoot == "" {
			root, err := defaultDetachedLogsRoot(runID)
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			logsRoot = root
		}
		absGraphPath, absConfigPath, absLogsRoot, err := resolveDetachedPaths(graphPath, configPath, logsRoot)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		graphPath = absGraphPath
		configPath = absConfigPath
		logsRoot = absLogsRoot

		childArgs := []string{"attractor", "run", "--graph", graphPath, "--config", configPath}
		if runID != "" {
			childArgs = append(childArgs, "--run-id", runID)
		}
		if logsRoot != "" {
			childArgs = append(childArgs, "--logs-root", logsRoot)
		}
		if allowTestShim {
			childArgs = append(childArgs, "--allow-test-shim")
		}
		if confirmStaleBuild {
			childArgs = append(childArgs, "--confirm-stale-build")
		}
		if noCXDB {
			childArgs = append(childArgs, "--no-cxdb")
		}
		childArgs = append(childArgs, skipCLIHeadlessWarningFlag)
		for _, spec := range canonicalForceSpecs {
			childArgs = append(childArgs, "--force-model", spec)
		}

		if err := launchDetached(childArgs, logsRoot); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Printf("detached=true\nlogs_root=%s\npid_file=%s\n", logsRoot, filepath.Join(logsRoot, "run.pid"))
		os.Exit(0)
	}

	dotSource, err := os.ReadFile(graphPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	cfg, err := engine.LoadRunConfigFile(configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if !skipCLIHeadlessWarning && runConfigUsesCLIProviders(cfg) {
		if !confirmCLIHeadlessWarning(os.Stdin, os.Stderr) {
			fmt.Fprintln(os.Stderr, "preflight aborted: declined provider CLI headless-risk warning")
			os.Exit(1)
		}
	}

	// Default: no deadline. CLI runs (especially with provider CLIs) can take hours.
	ctx, cleanupSignalCtx := signalCancelContext()

	res, err := engine.RunWithConfig(ctx, dotSource, cfg, engine.RunOptions{
		RunID:         runID,
		LogsRoot:      logsRoot,
		AllowTestShim: allowTestShim,
		DisableCXDB:   noCXDB,
		ForceModels:   forceModels,
		OnCXDBStartup: func(info *engine.CXDBStartupInfo) {
			if info == nil {
				return
			}
			if info.UIURL == "" {
				return
			}
			if info.UIStarted {
				fmt.Fprintf(os.Stderr, "CXDB UI starting at %s\n", info.UIURL)
				return
			}
			fmt.Fprintf(os.Stderr, "CXDB UI available at %s\n", info.UIURL)
		},
	})
	cleanupSignalCtx()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("run_id=%s\n", res.RunID)
	fmt.Printf("logs_root=%s\n", res.LogsRoot)
	fmt.Printf("worktree=%s\n", res.WorktreeDir)
	fmt.Printf("run_branch=%s\n", res.RunBranch)
	fmt.Printf("final_commit=%s\n", res.FinalCommitSHA)
	if res.CXDBUIURL != "" {
		fmt.Printf("cxdb_ui=%s\n", res.CXDBUIURL)
	}
	for _, w := range res.Warnings {
		fmt.Fprintf(os.Stderr, "WARNING: %s\n", w)
	}

	if string(res.FinalStatus) == "success" {
		os.Exit(0)
	}
	os.Exit(1)
}

func parseForceModelFlags(specs []string) (map[string]string, []string, error) {
	if len(specs) == 0 {
		return nil, nil, nil
	}
	overrides := map[string]string{}
	for _, raw := range specs {
		spec := strings.TrimSpace(raw)
		parts := strings.SplitN(spec, "=", 2)
		if len(parts) != 2 {
			return nil, nil, fmt.Errorf("--force-model %q is invalid; expected provider=model", raw)
		}
		provider := normalizeRunProviderKey(parts[0])
		modelID := strings.TrimSpace(parts[1])
		if !isSupportedForceModelProvider(provider) {
			return nil, nil, fmt.Errorf("--force-model %q has unsupported provider %q (allowed: %s)", raw, strings.TrimSpace(parts[0]), supportedForceModelProvidersCSV())
		}
		if modelID == "" {
			return nil, nil, fmt.Errorf("--force-model %q has empty model id", raw)
		}
		if prev, exists := overrides[provider]; exists {
			return nil, nil, fmt.Errorf("--force-model provider %q specified multiple times (%q then %q)", provider, prev, modelID)
		}
		overrides[provider] = modelID
	}

	keys := make([]string, 0, len(overrides))
	for provider := range overrides {
		keys = append(keys, provider)
	}
	sort.Strings(keys)
	canonicalSpecs := make([]string, 0, len(keys))
	for _, provider := range keys {
		canonicalSpecs = append(canonicalSpecs, fmt.Sprintf("%s=%s", provider, overrides[provider]))
	}
	return overrides, canonicalSpecs, nil
}

func normalizeRunProviderKey(provider string) string {
	return providerspec.CanonicalProviderKey(provider)
}

func isSupportedForceModelProvider(provider string) bool {
	_, ok := providerspec.Builtin(provider)
	return ok
}

func runConfigUsesCLIProviders(cfg *engine.RunConfigFile) bool {
	if cfg == nil {
		return false
	}
	for _, providerCfg := range cfg.LLM.Providers {
		if providerCfg.Backend == engine.BackendCLI {
			return true
		}
	}
	return false
}

func confirmCLIHeadlessWarning(in io.Reader, out io.Writer) bool {
	if in == nil {
		in = os.Stdin
	}
	if out == nil {
		out = os.Stderr
	}
	_, _ = io.WriteString(out, cliHeadlessWarningPrompt)
	s, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false
	}
	answer := strings.ToLower(strings.TrimSpace(s))
	// Y/n defaults to yes.
	if answer == "" {
		return true
	}
	return answer == "y" || answer == "yes"
}

func supportedForceModelProvidersCSV() string {
	keys := make([]string, 0, len(providerspec.Builtins()))
	for key := range providerspec.Builtins() {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return strings.Join(keys, ", ")
}

func attractorValidate(args []string) {
	var graphPath string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--graph":
			i++
			if i >= len(args) {
				fmt.Fprintln(os.Stderr, "--graph requires a value")
				os.Exit(1)
			}
			graphPath = args[i]
		default:
			fmt.Fprintf(os.Stderr, "unknown arg: %s\n", args[i])
			os.Exit(1)
		}
	}
	if graphPath == "" {
		usage()
		os.Exit(1)
	}
	dotSource, err := os.ReadFile(graphPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	_, diags, err := engine.Prepare(dotSource)
	if err != nil {
		for _, d := range diags {
			fmt.Fprintf(os.Stderr, "%s: %s (%s)\n", d.Severity, d.Message, d.Rule)
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("ok: %s\n", filepath.Base(graphPath))
	for _, d := range diags {
		fmt.Printf("%s: %s (%s)\n", d.Severity, d.Message, d.Rule)
	}
	os.Exit(0)
}

func attractorResume(args []string) {
	var logsRoot string
	var cxdbBaseURL string
	var contextID string
	var runBranch string
	var repoPath string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--logs-root":
			i++
			if i >= len(args) {
				fmt.Fprintln(os.Stderr, "--logs-root requires a value")
				os.Exit(1)
			}
			logsRoot = args[i]
		case "--cxdb":
			i++
			if i >= len(args) {
				fmt.Fprintln(os.Stderr, "--cxdb requires a value")
				os.Exit(1)
			}
			cxdbBaseURL = args[i]
		case "--context-id":
			i++
			if i >= len(args) {
				fmt.Fprintln(os.Stderr, "--context-id requires a value")
				os.Exit(1)
			}
			contextID = args[i]
		case "--run-branch":
			i++
			if i >= len(args) {
				fmt.Fprintln(os.Stderr, "--run-branch requires a value")
				os.Exit(1)
			}
			runBranch = args[i]
		case "--repo":
			i++
			if i >= len(args) {
				fmt.Fprintln(os.Stderr, "--repo requires a value")
				os.Exit(1)
			}
			repoPath = args[i]
		default:
			fmt.Fprintf(os.Stderr, "unknown arg: %s\n", args[i])
			os.Exit(1)
		}
	}
	if logsRoot == "" && (cxdbBaseURL == "" || contextID == "") && runBranch == "" {
		usage()
		os.Exit(1)
	}
	// Default: no deadline. Resume may replay long stages or rehydrate large artifacts.
	ctx, cleanupSignalCtx := signalCancelContext()
	var (
		res *engine.Result
		err error
	)
	switch {
	case logsRoot != "":
		res, err = engine.Resume(ctx, logsRoot)
	case cxdbBaseURL != "" && contextID != "":
		res, err = engine.ResumeFromCXDB(ctx, cxdbBaseURL, contextID)
	case runBranch != "":
		res, err = engine.ResumeFromBranch(ctx, repoPath, runBranch)
	default:
		usage()
		os.Exit(1)
	}
	cleanupSignalCtx()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("run_id=%s\n", res.RunID)
	fmt.Printf("logs_root=%s\n", res.LogsRoot)
	fmt.Printf("worktree=%s\n", res.WorktreeDir)
	fmt.Printf("run_branch=%s\n", res.RunBranch)
	fmt.Printf("final_commit=%s\n", res.FinalCommitSHA)
	if res.CXDBUIURL != "" {
		fmt.Printf("cxdb_ui=%s\n", res.CXDBUIURL)
	}

	if string(res.FinalStatus) == "success" {
		os.Exit(0)
	}
	os.Exit(1)
}
