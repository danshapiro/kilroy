package main

import (
	"bufio"
	"context"
	"encoding/json"
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
	"github.com/danshapiro/kilroy/internal/attractor/modeldb"
	"github.com/danshapiro/kilroy/internal/attractor/validate"
	"github.com/danshapiro/kilroy/internal/dotenv"
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
	// Auto-load .env from CWD; silently ignore if absent.
	if err := dotenv.Load(".env"); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not load .env: %v\n", err)
	}

	args := os.Args[1:]

	// Pre-scan for --env-file before the subcommand switch so it works with
	// any subcommand: kilroy --env-file .env.prod attractor run ...
	args = loadEnvFile(args)

	if len(args) < 1 {
		usage()
		os.Exit(1)
	}

	switch args[0] {
	case "--version", "-v", "version":
		fmt.Printf("kilroy %s\n", version.Version)
		os.Exit(0)
	case "attractor":
		attractor(args[1:])
	default:
		usage()
		os.Exit(1)
	}
}

// loadEnvFile scans args for --env-file <path>, loads the file, and returns
// args with the flag and its value removed. If the flag is absent, args is
// returned unchanged. Exits on error (explicit file not found is an error).
func loadEnvFile(args []string) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		if args[i] == "--env-file" {
			i++
			if i >= len(args) {
				fmt.Fprintln(os.Stderr, "--env-file requires a path argument")
				os.Exit(1)
			}
			path := args[i]
			// Unlike auto-load, an explicit --env-file must exist.
			if _, err := os.Stat(path); os.IsNotExist(err) {
				fmt.Fprintf(os.Stderr, "error: --env-file %q does not exist\n", path)
				os.Exit(1)
			}
			if err := dotenv.Load(path); err != nil {
				fmt.Fprintf(os.Stderr, "error: could not load --env-file %q: %v\n", path, err)
				os.Exit(1)
			}
			continue
		}
		out = append(out, args[i])
	}
	return out
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage:")
	fmt.Fprintln(os.Stderr, "  kilroy --version")
	fmt.Fprintln(os.Stderr, "  kilroy [--env-file <path>] attractor run [--detach] [--validate|--preflight|--test-run] [--allow-test-shim] [--confirm-stale-build] [--no-cxdb] [--force-model <provider=model>] --graph <file.dot> [--config <run.yaml>] [--run-id <id>] [--logs-root <dir>]")
	fmt.Fprintln(os.Stderr, "  kilroy attractor resume --logs-root <dir>")
	fmt.Fprintln(os.Stderr, "  kilroy attractor resume --cxdb <http_base_url> --context-id <id>")
	fmt.Fprintln(os.Stderr, "  kilroy attractor resume --run-branch <attractor/run/...> [--repo <path>]")
	fmt.Fprintln(os.Stderr, "  kilroy attractor status [--logs-root <dir> | --latest] [--json] [-v|--verbose] [--follow|-f] [--cxdb] [--raw] [--watch] [--interval <sec>]")
	fmt.Fprintln(os.Stderr, "  kilroy attractor stop --logs-root <dir> [--grace-ms <ms>] [--force]")
	fmt.Fprintln(os.Stderr, "  kilroy attractor validate --graph <file.dot>")
	fmt.Fprintln(os.Stderr, "  kilroy attractor validate --batch <file.dot> [<file.dot> ...] [--json]")
	fmt.Fprintln(os.Stderr, "  kilroy attractor ingest [--output <file.dot>] [--model <model>] [--skill <skill.md>] [--repo <path>] [--max-turns <n>] <requirements>")
	fmt.Fprintln(os.Stderr, "  kilroy attractor serve [--addr <host:port>]")
	fmt.Fprintln(os.Stderr, "  kilroy attractor modeldb suggest [--refresh] [--ttl <duration>] [--provider <name>]")
	fmt.Fprintln(os.Stderr, "  kilroy attractor review --graph <file.dot> [--output <file>] [--json] [--max-turns <n>]")
	fmt.Fprintln(os.Stderr, "  kilroy attractor runs list [--json]")
	fmt.Fprintln(os.Stderr, "  kilroy attractor runs prune [--before YYYY-MM-DD] [--graph PATTERN] [--label KEY=VALUE] [--orphans] [--dry-run | --yes]")
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
	case "modeldb":
		attractorModelDB(args[1:])
	case "review":
		attractorReview(args[1:])
	case "runs":
		attractorRuns(args[1:])
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
	var preflightOnly bool
	var allowTestShim bool
	var confirmStaleBuild bool
	var noCXDB bool
	var skipCLIHeadlessWarning bool
	var forceModelSpecs []string

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--detach":
			detach = true
		case "--preflight", "--validate":
			preflightOnly = true
		case "--test-run":
			preflightOnly = true
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

	if graphPath == "" {
		usage()
		os.Exit(1)
	}
	if preflightOnly && detach {
		fmt.Fprintln(os.Stderr, "--validate/--preflight/--test-run cannot be combined with --detach")
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
		cfg, err := loadOrBuildConfig(configPath)
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

		childArgs := []string{"attractor", "run", "--graph", graphPath}
		if configPath != "" {
			childArgs = append(childArgs, "--config", configPath)
		}
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
	cfg, err := loadOrBuildConfig(configPath)
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
	if preflightOnly {
		ctx, cleanupSignalCtx := signalCancelContext()
		pf, err := engine.PreflightWithConfig(ctx, dotSource, cfg, engine.RunOptions{
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
		fmt.Printf("preflight=true\n")
		fmt.Printf("run_id=%s\n", pf.RunID)
		fmt.Printf("logs_root=%s\n", pf.LogsRoot)
		fmt.Printf("preflight_report=%s\n", pf.PreflightReportPath)
		if pf.CXDBUIURL != "" {
			fmt.Printf("cxdb_ui=%s\n", pf.CXDBUIURL)
		}
		for _, w := range pf.Warnings {
			fmt.Fprintf(os.Stderr, "WARNING: %s\n", w)
		}
		os.Exit(0)
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

// loadOrBuildConfig loads a config from file, or builds a zero-config default
// when configPath is empty. In both cases, providers are auto-detected from
// the environment to fill gaps. Config-file values always take precedence.
func loadOrBuildConfig(configPath string) (*engine.RunConfigFile, error) {
	var cfg *engine.RunConfigFile
	if configPath != "" {
		loaded, err := engine.LoadRunConfigFile(configPath)
		if err != nil {
			return nil, err
		}
		cfg = loaded
	} else {
		built, err := engine.DefaultRunConfig()
		if err != nil {
			return nil, err
		}
		cfg = built
	}
	detected := engine.DetectProviders()
	engine.ApplyDetectedProviders(cfg, detected)
	if len(detected) > 0 {
		for _, dp := range detected {
			fmt.Fprintf(os.Stderr, "auto-detected provider %s (backend=%s)\n", dp.Key, dp.Backend)
		}
	} else {
		fmt.Fprintln(os.Stderr, "no providers auto-detected from environment (set API key env vars for your LLM providers)")
	}
	return cfg, nil
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
	var batchFiles []string
	var batchMode bool
	var jsonOutput bool

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--graph":
			i++
			if i >= len(args) {
				fmt.Fprintln(os.Stderr, "--graph requires a value")
				os.Exit(1)
			}
			graphPath = args[i]
		case "--batch":
			batchMode = true
			// Consume all remaining non-flag arguments as file paths.
			for i+1 < len(args) && !strings.HasPrefix(args[i+1], "--") {
				i++
				batchFiles = append(batchFiles, args[i])
			}
		case "--json":
			jsonOutput = true
		default:
			// Allow positional file arguments when in batch mode context
			// (e.g. when --batch was not yet seen but a .dot was given).
			if strings.HasSuffix(args[i], ".dot") && batchMode {
				batchFiles = append(batchFiles, args[i])
			} else {
				fmt.Fprintf(os.Stderr, "unknown arg: %s\n", args[i])
				os.Exit(1)
			}
		}
	}

	if batchMode {
		attractorValidateBatch(batchFiles, jsonOutput)
		return
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
	// Load the embedded model catalog so that stylesheet model ID lint rules
	// fire. On failure, fall back to nil catalog (degraded mode: model ID
	// checks are skipped; all other rules still run).
	cat, catErr := modeldb.LoadEmbeddedCatalog()
	if catErr != nil {
		fmt.Fprintf(os.Stderr, "WARNING: model catalog unavailable, model ID checks skipped: %v\n", catErr)
		cat = nil
	}
	_, diags, err := engine.PrepareWithOptions(dotSource, engine.PrepareOptions{Catalog: cat})
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

// batchFileResult holds per-file validate results for batch mode.
type batchFileResult struct {
	File     string                `json:"file"`
	Errors   []validate.Diagnostic `json:"errors"`
	Warnings []validate.Diagnostic `json:"warnings"`
	ParseErr string                `json:"parse_error,omitempty"`
}

// attractorValidateBatch runs validate against each file in files and emits a
// summary.  Exit codes: 0 = all clean, 1 = any errors, 2 = warnings-only.
func attractorValidateBatch(files []string, jsonOutput bool) {
	if len(files) == 0 {
		fmt.Fprintln(os.Stderr, "--batch requires at least one file path")
		usage()
		os.Exit(1)
	}

	results := make([]batchFileResult, 0, len(files))
	anyErrors := false
	anyWarnings := false

	for _, f := range files {
		res := batchFileResult{
			File:     f,
			Errors:   []validate.Diagnostic{},
			Warnings: []validate.Diagnostic{},
		}
		dotSource, err := os.ReadFile(f)
		if err != nil {
			res.ParseErr = err.Error()
			anyErrors = true
			results = append(results, res)
			continue
		}
		_, diags, prepErr := engine.Prepare(dotSource)
		// Collect diagnostics even when Prepare returns an error.
		for _, d := range diags {
			switch d.Severity {
			case validate.SeverityError:
				res.Errors = append(res.Errors, d)
			case validate.SeverityWarning:
				res.Warnings = append(res.Warnings, d)
			}
		}
		if prepErr != nil && len(res.Errors) == 0 {
			// Prepare returned a fatal error that produced no diagnostic; surface it.
			res.ParseErr = prepErr.Error()
		}
		if len(res.Errors) > 0 || res.ParseErr != "" {
			anyErrors = true
		}
		if len(res.Warnings) > 0 {
			anyWarnings = true
		}
		results = append(results, res)
	}

	if jsonOutput {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(results); err != nil {
			fmt.Fprintln(os.Stderr, "json encode:", err)
			os.Exit(1)
		}
	} else {
		// Human-readable per-file summary table.
		fmt.Printf("%-50s  %6s  %8s\n", "FILE", "ERRORS", "WARNINGS")
		fmt.Println(strings.Repeat("-", 70))
		for _, r := range results {
			errCount := len(r.Errors)
			if r.ParseErr != "" {
				errCount++
			}
			warnCount := len(r.Warnings)
			status := "ok"
			if errCount > 0 {
				status = "FAIL"
			} else if warnCount > 0 {
				status = "warn"
			}
			fmt.Printf("%-50s  %6d  %8d  [%s]\n", filepath.Base(r.File), errCount, warnCount, status)
			if r.ParseErr != "" {
				fmt.Printf("  parse error: %s\n", r.ParseErr)
			}
			for _, d := range r.Errors {
				fmt.Printf("  ERROR   %-30s %s\n", "("+d.Rule+")", d.Message)
			}
			for _, d := range r.Warnings {
				fmt.Printf("  WARNING %-30s %s\n", "("+d.Rule+")", d.Message)
			}
		}
		fmt.Println(strings.Repeat("-", 70))
		fmt.Printf("Total files: %d\n", len(results))
	}

	switch {
	case anyErrors:
		os.Exit(1)
	case anyWarnings:
		os.Exit(2)
	default:
		os.Exit(0)
	}
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
