package engine

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/danshapiro/kilroy/internal/attractor/model"
	"github.com/danshapiro/kilroy/internal/attractor/modeldb"
	"github.com/danshapiro/kilroy/internal/cxdb"
)

type runBootstrap struct {
	Graph                   *model.Graph
	Dot                     []byte
	Config                  *RunConfigFile
	Options                 RunOptions
	Registry                *HandlerRegistry
	ResolvedArtifactPolicy  ResolvedArtifactPolicy
	Catalog                 *modeldb.Catalog
	ModelCatalogSource      string
	ModelCatalogPath        string
	Runtimes                map[string]ProviderRuntime
	InputInferer            InputReferenceInferer
	ResolvedWarning         string
	InputInfererInitWarning string
	StartupWarnings         []string
	Warnings                []string
	CXDBClient              *cxdb.Client
	CXDBBin                 *cxdb.BinaryClient
	Startup                 *CXDBStartupInfo
}

// RunWithConfig executes a run using the metaspec run configuration file schema.
func RunWithConfig(ctx context.Context, dotSource []byte, cfg *RunConfigFile, overrides RunOptions) (*Result, error) {
	boot, err := bootstrapRunWithConfig(ctx, dotSource, cfg, overrides)
	if err != nil {
		return nil, err
	}
	defer closeRunBootstrapResources(boot)

	var sink *CXDBSink
	if boot.CXDBClient != nil {
		bundleID, bundle, _, err := cxdb.KilroyAttractorRegistryBundle()
		if err != nil {
			return nil, err
		}
		if _, err := boot.CXDBClient.PublishRegistryBundle(ctx, bundleID, bundle); err != nil {
			return nil, err
		}
		ci, err := createContextWithFallback(ctx, boot.CXDBClient, boot.CXDBBin)
		if err != nil {
			return nil, err
		}
		sink = NewCXDBSink(boot.CXDBClient, boot.CXDBBin, boot.Options.RunID, ci.ContextID, ci.HeadTurnID, bundleID)
	}

	eng := newBaseEngine(boot.Graph, dotSource, boot.Options)
	eng.Registry = boot.Registry // reuse the registry from validation (avoids creating a duplicate)
	eng.RunConfig = boot.Config
	eng.ArtifactPolicy = boot.ResolvedArtifactPolicy
	eng.Context = NewContextWithGraphAttrs(boot.Graph)
	eng.AgentBackend = NewAgentRouterWithRuntimes(boot.Config, boot.Catalog, boot.Runtimes)
	eng.CXDB = sink
	eng.ModelCatalogSHA = boot.Catalog.SHA256
	eng.ModelCatalogSource = boot.ModelCatalogSource
	eng.ModelCatalogPath = boot.ModelCatalogPath
	eng.InputMaterializationPolicy = inputMaterializationPolicyFromConfig(boot.Config)
	eng.InputReferenceInferer = boot.InputInferer
	eng.InputInferenceCache = map[string][]InferredReference{}
	eng.InputSourceTargetMap = map[string]string{}
	if boot.ResolvedWarning != "" {
		eng.Warn(boot.ResolvedWarning)
		eng.Context.AppendLog(boot.ResolvedWarning)
	}
	if boot.InputInfererInitWarning != "" {
		eng.Warn(boot.InputInfererInitWarning)
		eng.Context.AppendLog(boot.InputInfererInitWarning)
	}
	for _, w := range boot.StartupWarnings {
		eng.Warn(w)
	}

	if boot.Options.OnEngineReady != nil {
		boot.Options.OnEngineReady(eng)
	}

	res, err := eng.run(ctx)
	if err != nil {
		return nil, err
	}
	if boot.Startup != nil {
		res.CXDBUIURL = strings.TrimSpace(boot.Startup.UIURL)
	}
	return res, nil
}

func closeRunBootstrapResources(boot *runBootstrap) {
	if boot == nil {
		return
	}
	if boot.Startup != nil {
		_ = boot.Startup.shutdownManagedProcesses()
	}
	if boot.CXDBBin != nil {
		_ = boot.CXDBBin.Close()
	}
}

func bootstrapRunWithConfig(ctx context.Context, dotSource []byte, cfg *RunConfigFile, overrides RunOptions) (*runBootstrap, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config is nil")
	}
	applyConfigDefaults(cfg)

	// Use the registry from options if provided (layered composition from cmd/kilroy/),
	// otherwise fall back to the full default registry.
	reg := overrides.Registry
	if reg == nil {
		reg = NewDefaultRegistry()
	}

	// Load catalog early (best-effort) so that model ID lint rules fire during
	// PrepareWithOptions. The full ResolveModelCatalog snapshot still runs later
	// for execution repeatability; this early load uses the pinned file directly.
	var earlyCatalog *modeldb.Catalog
	if pinnedPath := strings.TrimSpace(cfg.ModelDB.OpenRouterModelInfoPath); pinnedPath != "" {
		if cat, catErr := modeldb.LoadCatalogFromOpenRouterJSON(pinnedPath); catErr == nil {
			earlyCatalog = cat
		}
		// On error, earlyCatalog remains nil — model ID checks are skipped,
		// all other lint rules still run (degraded mode).
	} else {
		// No pinned path configured — fall back to the embedded catalog so
		// model ID lint rules fire even without an explicit modeldb config.
		if cat, catErr := modeldb.LoadEmbeddedCatalog(); catErr == nil {
			earlyCatalog = cat
		}
	}

	// Prepare graph (parse + transforms + validate).
	g, _, err := PrepareWithOptions(dotSource, PrepareOptions{
		RepoPath:   cfg.Repo.Path,
		GraphDir:   overrides.GraphDir,
		KnownTypes: reg.KnownTypes(),
		Catalog:    earlyCatalog,
	})
	if err != nil {
		return nil, err
	}

	// Ensure backend is specified for each provider used by the graph.
	// Use the handler registry to identify nodes that require an LLM provider
	// instead of hardcoding shape checks.
	usedProviders := map[string]bool{}
	for _, n := range g.Nodes {
		if n == nil {
			continue
		}
		if pr, ok := reg.Resolve(n).(ProviderRequiringHandler); !ok || !pr.RequiresProvider() {
			continue
		}
		p := strings.TrimSpace(n.Attr("llm_provider", ""))
		if p == "" {
			continue // validation already fails, but keep defensive
		}
		usedProviders[normalizeProviderKey(p)] = true
	}
	runtimes, err := resolveProviderRuntimes(cfg)
	if err != nil {
		return nil, err
	}
	var (
		inputInferer            InputReferenceInferer
		inputInfererInitWarning string
	)
	if cfg.Inputs.Materialize.InferWithLLM != nil && *cfg.Inputs.Materialize.InferWithLLM {
		inferer, infererErr := newInputReferenceInfererFromRuntimes(runtimes)
		if infererErr != nil {
			inputInfererInitWarning = fmt.Sprintf("input reference inferer init failed (scanner-only fallback): %v", infererErr)
		} else {
			inputInferer = inferer
		}
	}
	for p := range usedProviders {
		rt, ok := runtimes[p]
		if !ok || (rt.Backend != BackendAPI && rt.Backend != BackendCLI) {
			return nil, fmt.Errorf("missing llm.providers.%s.backend (Kilroy forbids implicit backend defaults)\n  hint: add llm.providers.%s.backend: cli (or api) to your run config, or remove --config to use auto-detection", p, p)
		}
	}
	runUsesCLIProviders := false
	for p := range usedProviders {
		if rt, ok := runtimes[p]; ok && rt.Backend == BackendCLI {
			runUsesCLIProviders = true
			break
		}
	}

	opts := RunOptions{
		RepoPath:        cfg.Repo.Path,
		RunBranchPrefix: cfg.Git.RunBranchPrefix,
		StageTimeout:    durationFromOptionalMSOrDisabled(cfg.RuntimePolicy.StageTimeoutMS),
		StallTimeout:    durationFromOptionalMSOrDisabled(cfg.RuntimePolicy.StallTimeoutMS),
		StallCheckInterval: durationFromOptionalMSOrDisabled(
			cfg.RuntimePolicy.StallCheckIntervalMS,
		),
		MaxLLMRetries: copyOptionalInt(cfg.RuntimePolicy.MaxLLMRetries),
	}
	// Allow select overrides.
	if overrides.RunID != "" {
		opts.RunID = overrides.RunID
	}
	if overrides.LogsRoot != "" {
		opts.LogsRoot = overrides.LogsRoot
	}
	if overrides.WorktreeDir != "" {
		opts.WorktreeDir = overrides.WorktreeDir
	}
	if overrides.RunBranchPrefix != "" {
		opts.RunBranchPrefix = overrides.RunBranchPrefix
	}
	opts.AllowTestShim = overrides.AllowTestShim
	opts.ForceModels = normalizeForceModels(overrides.ForceModels)
	opts.ProgressSink = overrides.ProgressSink
	opts.Interviewer = overrides.Interviewer
	opts.OnEngineReady = overrides.OnEngineReady
	opts.RunDB = overrides.RunDB
	opts.Registry = overrides.Registry
	opts.Labels = overrides.Labels
	opts.Inputs = overrides.Inputs
	opts.GraphDir = overrides.GraphDir
	opts.GitOps = overrides.GitOps
	if overrides.Workspace != "" {
		opts.Workspace = overrides.Workspace
		if opts.RepoPath == "" {
			opts.RepoPath = overrides.Workspace
		}
	}

	if err := opts.applyDefaults(); err != nil {
		return nil, err
	}
	// Wire require_clean from config (applyDefaults sets the safe default;
	// the config can explicitly relax it to false).
	if cfg.Git.RequireClean != nil {
		opts.RequireClean = *cfg.Git.RequireClean
	}
	resolvedArtifactPolicy, err := ResolveArtifactPolicy(cfg, ResolveArtifactPolicyInput{
		LogsRoot: opts.LogsRoot,
	})
	if err != nil {
		return nil, err
	}

	// Auto-detect git mode when GitOps is not explicitly set.
	if opts.GitOps == nil && AutoDetectGitOps != nil && opts.RepoPath != "" {
		if detected := AutoDetectGitOps(opts.RepoPath); detected != nil {
			opts.GitOps = detected
		}
	}

	// Repo validation: cheap local checks that must pass before any expensive
	// preflight work (provider probes, model catalog fetch, CXDB startup).
	if opts.GitOps != nil {
		if opts.RepoPath == "" {
			return nil, fmt.Errorf("repo.path is required")
		}
		if err := opts.GitOps.ValidateRepo(opts.RepoPath, opts.RequireClean); err != nil {
			return nil, err
		}
		// Verify the repo has at least one commit (HeadSHA fails on empty repos).
		// eng.run() needs this later for branch creation; catching it here avoids
		// wasting minutes on provider probes and CXDB startup first.
		if _, err := opts.GitOps.HeadSHA(opts.RepoPath); err != nil {
			return nil, fmt.Errorf("repo has no commits or HEAD is unresolvable: %w", err)
		}
	}
	// Ensure the logs directory is writable before expensive preflight work.
	// Several preflight steps write into LogsRoot, but an outright unwritable
	// path would surface as a confusing mid-preflight error instead of a clear
	// early one.
	if err := os.MkdirAll(opts.LogsRoot, 0o755); err != nil {
		return nil, fmt.Errorf("cannot create logs directory %s: %w", opts.LogsRoot, err)
	}

	if err := validateRunCLIProfilePolicy(cfg, opts, runUsesCLIProviders); err != nil {
		report := &providerPreflightReport{
			GeneratedAt:         time.Now().UTC().Format(time.RFC3339Nano),
			CLIProfile:          normalizedCLIProfile(cfg),
			AllowTestShim:       opts.AllowTestShim,
			StrictCapabilities:  parseBool(strings.TrimSpace(os.Getenv("KILROY_PREFLIGHT_STRICT_CAPABILITIES")), false),
			CapabilityProbeMode: capabilityProbeMode(),
			PromptProbeMode:     promptProbeMode(cfg),
		}
		report.addCheck(providerPreflightCheck{
			Name:    "provider_executable_policy",
			Status:  preflightStatusFail,
			Message: err.Error(),
		})
		_ = writePreflightReport(opts.LogsRoot, report)
		return nil, err
	}

	// Resolve + snapshot the model catalog for this run (repeatability).
	// When no catalog path is configured, fall back to the embedded catalog.
	var (
		catalog            *modeldb.Catalog
		modelCatalogSource string
		modelCatalogPath   string
		resolvedWarning    string
	)
	if strings.TrimSpace(cfg.ModelDB.OpenRouterModelInfoPath) != "" {
		resolved, resolveErr := modeldb.ResolveModelCatalog(
			ctx,
			cfg.ModelDB.OpenRouterModelInfoPath,
			opts.LogsRoot,
			modeldb.CatalogUpdatePolicy(strings.ToLower(strings.TrimSpace(cfg.ModelDB.OpenRouterModelInfoUpdatePolicy))),
			cfg.ModelDB.OpenRouterModelInfoURL,
			time.Duration(cfg.ModelDB.OpenRouterModelInfoFetchTimeoutMS)*time.Millisecond,
		)
		if resolveErr != nil {
			return nil, resolveErr
		}
		cat, loadErr := loadCatalogForRun(resolved.SnapshotPath)
		if loadErr != nil {
			return nil, loadErr
		}
		catalog = cat
		modelCatalogSource = resolved.Source
		modelCatalogPath = resolved.SnapshotPath
		resolvedWarning = strings.TrimSpace(resolved.Warning)
	} else {
		cat, loadErr := modeldb.LoadEmbeddedCatalog()
		if loadErr != nil {
			return nil, fmt.Errorf("no model catalog configured and embedded catalog unavailable: %w", loadErr)
		}
		catalog = cat
		modelCatalogSource = "embedded"
		modelCatalogPath = ""
	}
	catalogChecks, catalogErr := validateProviderModelPairs(g, runtimes, catalog, opts)
	if catalogErr != nil {
		report := &providerPreflightReport{
			GeneratedAt:         time.Now().UTC().Format(time.RFC3339Nano),
			CLIProfile:          normalizedCLIProfile(cfg),
			AllowTestShim:       opts.AllowTestShim,
			StrictCapabilities:  parseBool(strings.TrimSpace(os.Getenv("KILROY_PREFLIGHT_STRICT_CAPABILITIES")), false),
			CapabilityProbeMode: capabilityProbeMode(),
			PromptProbeMode:     promptProbeMode(cfg),
		}
		for _, c := range catalogChecks {
			report.addCheck(c)
		}
		_ = writePreflightReport(opts.LogsRoot, report)
		return nil, catalogErr
	}
	if _, err := runProviderCLIPreflight(ctx, g, runtimes, cfg, opts, catalog, catalogChecks); err != nil {
		return nil, err
	}

	var (
		cxdbClient *cxdb.Client
		bin        *cxdb.BinaryClient
		startup    *CXDBStartupInfo
	)
	cxdbConfigured := strings.TrimSpace(cfg.CXDB.BinaryAddr) != "" && strings.TrimSpace(cfg.CXDB.HTTPBaseURL) != ""
	if !overrides.DisableCXDB && cxdbConfigured {
		var cxdbStartup *CXDBStartupInfo
		cxdbClient, bin, cxdbStartup, err = ensureCXDBReady(ctx, cfg, opts.LogsRoot, opts.RunID)
		if err != nil {
			return nil, err
		}
		startup = cxdbStartup
		if startup != nil && overrides.OnCXDBStartup != nil {
			overrides.OnCXDBStartup(startup)
		}
	}

	inputInfererInitWarning = strings.TrimSpace(inputInfererInitWarning)
	combinedWarnings := []string{}
	if resolvedWarning != "" {
		combinedWarnings = append(combinedWarnings, resolvedWarning)
	}
	if inputInfererInitWarning != "" {
		combinedWarnings = append(combinedWarnings, inputInfererInitWarning)
	}
	startupWarnings := []string{}
	if startup != nil {
		for _, w := range startup.Warnings {
			w = strings.TrimSpace(w)
			if w == "" {
				continue
			}
			startupWarnings = append(startupWarnings, w)
			combinedWarnings = append(combinedWarnings, w)
		}
	}

	return &runBootstrap{
		Graph:                   g,
		Dot:                     dotSource,
		Config:                  cfg,
		Options:                 opts,
		Registry:                reg,
		ResolvedArtifactPolicy:  resolvedArtifactPolicy,
		Catalog:                 catalog,
		ModelCatalogSource:      modelCatalogSource,
		ModelCatalogPath:        modelCatalogPath,
		Runtimes:                runtimes,
		InputInferer:            inputInferer,
		ResolvedWarning:         resolvedWarning,
		InputInfererInitWarning: inputInfererInitWarning,
		StartupWarnings:         startupWarnings,
		Warnings:                combinedWarnings,
		CXDBClient:              cxdbClient,
		CXDBBin:                 bin,
		Startup:                 startup,
	}, nil
}

func validateProviderModelPairs(g *model.Graph, runtimes map[string]ProviderRuntime, catalog *modeldb.Catalog, opts RunOptions) ([]providerPreflightCheck, error) {
	if g == nil || catalog == nil {
		return nil, nil
	}
	reg := opts.Registry
	if reg == nil {
		reg = NewDefaultRegistry()
	}
	var checks []providerPreflightCheck
	warnedUncovered := map[string]bool{}
	for _, n := range g.Nodes {
		if n == nil {
			continue
		}
		if pr, ok := reg.Resolve(n).(ProviderRequiringHandler); !ok || !pr.RequiresProvider() {
			continue
		}
		provider := normalizeProviderKey(n.Attr("llm_provider", ""))
		modelID := modelIDForNode(n)
		if provider == "" || modelID == "" {
			continue
		}
		rt, ok := runtimes[provider]
		if !ok {
			return checks, fmt.Errorf("preflight: provider %s missing runtime definition", provider)
		}
		backend := rt.Backend
		if backend != BackendCLI && backend != BackendAPI {
			continue
		}
		if _, forced := forceModelForProvider(opts.ForceModels, provider); forced {
			continue
		}
		if !modeldb.CatalogCoversProvider(catalog, provider) {
			if !warnedUncovered[provider] {
				warnedUncovered[provider] = true
				checks = append(checks, providerPreflightCheck{
					Name:     "provider_model_catalog",
					Provider: provider,
					Status:   preflightStatusWarn,
					Message:  fmt.Sprintf("model validation skipped: provider %s not in catalog (prompt probe will validate)", provider),
					Details: map[string]any{
						"model":   modelID,
						"backend": string(backend),
					},
				})
			}
			continue
		}
		if !modeldb.CatalogHasProviderModel(catalog, provider, modelID) {
			checks = append(checks, providerPreflightCheck{
				Name:     "provider_model_catalog",
				Provider: provider,
				Status:   preflightStatusWarn,
				Message:  fmt.Sprintf("llm_provider=%s backend=%s model=%s not present in run catalog (catalog may be stale; prompt probe will validate)", provider, backend, modelID),
				Details: map[string]any{
					"model":   modelID,
					"backend": string(backend),
				},
			})
		}
	}
	return checks, nil
}

func loadCatalogForRun(path string) (*modeldb.Catalog, error) {
	return modeldb.LoadCatalogFromOpenRouterJSON(path)
}

func modelIDForNode(n *model.Node) string {
	if n == nil {
		return ""
	}
	modelID := strings.TrimSpace(n.Attr("llm_model", ""))
	if modelID == "" {
		// Best-effort compatibility with stylesheet examples that use "model".
		modelID = strings.TrimSpace(n.Attr("model", ""))
	}
	return modelID
}

func durationFromOptionalMSOrDisabled(ms *int) time.Duration {
	if ms == nil {
		return 0
	}
	if *ms <= 0 {
		return 0
	}
	return time.Duration(*ms) * time.Millisecond
}

func copyOptionalInt(v *int) *int {
	if v == nil {
		return nil
	}
	out := *v
	return &out
}

func createContextWithFallback(ctx context.Context, client *cxdb.Client, bin *cxdb.BinaryClient) (cxdb.ContextInfo, error) {
	if bin != nil {
		ci, err := bin.CreateContext(ctx, 0)
		if err == nil {
			return cxdb.ContextInfo{
				ContextID:  strconv.FormatUint(ci.ContextID, 10),
				HeadTurnID: strconv.FormatUint(ci.HeadTurnID, 10),
				HeadDepth:  int(ci.HeadDepth),
			}, nil
		}
	}
	return client.CreateContext(ctx, "0")
}
