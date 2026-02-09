package engine

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/strongdm/kilroy/internal/attractor/model"
	"github.com/strongdm/kilroy/internal/attractor/modeldb"
	"github.com/strongdm/kilroy/internal/cxdb"
)

// RunWithConfig executes a run using the metaspec run configuration file schema.
func RunWithConfig(ctx context.Context, dotSource []byte, cfg *RunConfigFile, overrides RunOptions) (*Result, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config is nil")
	}
	applyConfigDefaults(cfg)

	// Prepare graph (parse + transforms + validate).
	g, _, err := Prepare(dotSource)
	if err != nil {
		return nil, err
	}

	// Ensure backend is specified for each provider used by the graph.
	usedProviders := map[string]bool{}
	for _, n := range g.Nodes {
		if n == nil {
			continue
		}
		if n.Shape() != "box" {
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
	for p := range usedProviders {
		rt, ok := runtimes[p]
		if !ok || (rt.Backend != BackendAPI && rt.Backend != BackendCLI) {
			return nil, fmt.Errorf("missing llm.providers.%s.backend (Kilroy forbids implicit backend defaults)", p)
		}
	}

	opts := RunOptions{
		RepoPath:        cfg.Repo.Path,
		RunBranchPrefix: cfg.Git.RunBranchPrefix,
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

	if err := opts.applyDefaults(); err != nil {
		return nil, err
	}
	if err := validateRunCLIProfilePolicy(cfg, opts); err != nil {
		report := &providerPreflightReport{
			GeneratedAt:         time.Now().UTC().Format(time.RFC3339Nano),
			CLIProfile:          normalizedCLIProfile(cfg),
			AllowTestShim:       opts.AllowTestShim,
			StrictCapabilities:  parseBool(strings.TrimSpace(os.Getenv("KILROY_PREFLIGHT_STRICT_CAPABILITIES")), false),
			CapabilityProbeMode: capabilityProbeMode(),
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
	resolved, err := modeldb.ResolveModelCatalog(
		ctx,
		cfg.ModelDB.OpenRouterModelInfoPath,
		opts.LogsRoot,
		modeldb.CatalogUpdatePolicy(strings.ToLower(strings.TrimSpace(cfg.ModelDB.OpenRouterModelInfoUpdatePolicy))),
		cfg.ModelDB.OpenRouterModelInfoURL,
		time.Duration(cfg.ModelDB.OpenRouterModelInfoFetchTimeoutMS)*time.Millisecond,
	)
	if err != nil {
		return nil, err
	}
	catalog, err := loadCatalogForRun(resolved.SnapshotPath)
	if err != nil {
		return nil, err
	}
	if err := validateProviderModelPairs(g, runtimes, catalog, opts); err != nil {
		report := &providerPreflightReport{
			GeneratedAt:         time.Now().UTC().Format(time.RFC3339Nano),
			CLIProfile:          normalizedCLIProfile(cfg),
			AllowTestShim:       opts.AllowTestShim,
			StrictCapabilities:  parseBool(strings.TrimSpace(os.Getenv("KILROY_PREFLIGHT_STRICT_CAPABILITIES")), false),
			CapabilityProbeMode: capabilityProbeMode(),
		}
		report.addCheck(providerPreflightCheck{
			Name:    "provider_model_catalog",
			Status:  preflightStatusFail,
			Message: err.Error(),
		})
		_ = writePreflightReport(opts.LogsRoot, report)
		return nil, err
	}
	if _, err := runProviderCLIPreflight(ctx, g, runtimes, cfg, opts); err != nil {
		return nil, err
	}

	// CXDB is required in v1 and must be reachable.
	cxdbClient, bin, startup, err := ensureCXDBReady(ctx, cfg, opts.LogsRoot, opts.RunID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = bin.Close() }()
	if startup != nil {
		// Defer process shutdown after bin close is deferred so shutdown runs first (LIFO).
		defer func() { _ = startup.shutdownManagedProcesses() }()
	}
	if startup != nil && overrides.OnCXDBStartup != nil {
		overrides.OnCXDBStartup(startup)
	}
	bundleID, bundle, _, err := cxdb.KilroyAttractorRegistryBundle()
	if err != nil {
		return nil, err
	}
	if _, err := cxdbClient.PublishRegistryBundle(ctx, bundleID, bundle); err != nil {
		return nil, err
	}
	ci, err := createContextWithFallback(ctx, cxdbClient, bin)
	if err != nil {
		return nil, err
	}
	sink := NewCXDBSink(cxdbClient, bin, opts.RunID, ci.ContextID, ci.HeadTurnID, bundleID)

	eng := newBaseEngine(g, dotSource, opts)
	eng.RunConfig = cfg
	eng.Context = NewContextWithGraphAttrs(g)
	eng.CodergenBackend = NewCodergenRouterWithRuntimes(cfg, catalog, runtimes)
	eng.CXDB = sink
	eng.ModelCatalogSHA = catalog.SHA256
	eng.ModelCatalogSource = resolved.Source
	eng.ModelCatalogPath = resolved.SnapshotPath
	if strings.TrimSpace(resolved.Warning) != "" {
		eng.Warn(resolved.Warning)
		eng.Context.AppendLog(resolved.Warning)
	}
	if startup != nil {
		for _, w := range startup.Warnings {
			eng.Warn(w)
		}
	}

	res, err := eng.run(ctx)
	if err != nil {
		return nil, err
	}
	if startup != nil {
		res.CXDBUIURL = strings.TrimSpace(startup.UIURL)
	}
	return res, nil
}

func validateProviderModelPairs(g *model.Graph, runtimes map[string]ProviderRuntime, catalog *modeldb.Catalog, opts RunOptions) error {
	if g == nil || catalog == nil {
		return nil
	}
	for _, n := range g.Nodes {
		if n == nil || n.Shape() != "box" {
			continue
		}
		provider := normalizeProviderKey(n.Attr("llm_provider", ""))
		modelID := modelIDForNode(n)
		if provider == "" || modelID == "" {
			continue
		}
		rt, ok := runtimes[provider]
		if !ok {
			return fmt.Errorf("preflight: provider %s missing runtime definition", provider)
		}
		backend := rt.Backend
		if backend != BackendCLI && backend != BackendAPI {
			continue
		}
		if _, forced := forceModelForProvider(opts.ForceModels, provider); forced {
			continue
		}
		if !modeldb.CatalogHasProviderModel(catalog, provider, modelID) {
			return fmt.Errorf("preflight: llm_provider=%s backend=%s model=%s not present in run catalog", provider, backend, modelID)
		}
	}
	return nil
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
