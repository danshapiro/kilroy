package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/strongdm/kilroy/internal/attractor/gitutil"
	"github.com/strongdm/kilroy/internal/attractor/modeldb"
	"github.com/strongdm/kilroy/internal/attractor/runtime"
	"github.com/strongdm/kilroy/internal/cxdb"
)

type manifest struct {
	RunID         string `json:"run_id"`
	RepoPath      string `json:"repo_path"`
	RunBranch     string `json:"run_branch"`
	RunConfigPath string `json:"run_config_path"`

	ModelDB struct {
		LiteLLMCatalogPath   string `json:"litellm_catalog_path"`
		LiteLLMCatalogSHA256 string `json:"litellm_catalog_sha256"`
		LiteLLMCatalogSource string `json:"litellm_catalog_source"`
	} `json:"modeldb"`

	CXDB struct {
		HTTPBaseURL      string `json:"http_base_url"`
		ContextID        string `json:"context_id"`
		HeadTurnID       string `json:"head_turn_id"`
		RegistryBundleID string `json:"registry_bundle_id"`
	} `json:"cxdb"`
}

type ResumeOverrides struct {
	CXDBHTTPBaseURL string
	CXDBContextID   string
}

// Resume continues an existing run from {logs_root}/checkpoint.json.
//
// v1 resume source of truth:
// - filesystem checkpoint.json (execution state)
// - stage status.json for last completed node (routing outcome)
// - git commit SHA from checkpoint (code state)
func Resume(ctx context.Context, logsRoot string) (*Result, error) {
	return resumeFromLogsRoot(ctx, logsRoot, ResumeOverrides{})
}

func resumeFromLogsRoot(ctx context.Context, logsRoot string, ov ResumeOverrides) (*Result, error) {
	m, err := loadManifest(filepath.Join(logsRoot, "manifest.json"))
	if err != nil {
		return nil, err
	}
	cp, err := runtime.LoadCheckpoint(filepath.Join(logsRoot, "checkpoint.json"))
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(cp.GitCommitSHA) == "" {
		return nil, fmt.Errorf("checkpoint missing git_commit_sha")
	}
	dotSource, err := os.ReadFile(filepath.Join(logsRoot, "graph.dot"))
	if err != nil {
		return nil, err
	}
	g, _, err := Prepare(dotSource)
	if err != nil {
		return nil, err
	}

	// Best-effort: load the snapshotted run config if present.
	cfgPath := strings.TrimSpace(m.RunConfigPath)
	if cfgPath == "" {
		cfgPath = filepath.Join(logsRoot, "run_config.json")
	}
	var cfg *RunConfigFile
	if _, err := os.Stat(cfgPath); err == nil {
		if loaded, err := LoadRunConfigFile(cfgPath); err == nil {
			cfg = loaded
		}
	}

	// If we have a run config, resume with the real codergen router and CXDB sink.
	var backend CodergenBackend = &SimulatedCodergenBackend{}
	var sink *CXDBSink
	var catalog *modeldb.LiteLLMCatalog
	if cfg != nil {
		// Resume MUST use the run's snapshotted catalog (metaspec). Default location is logs_root/modeldb/litellm_catalog.json.
		snapshotPath := strings.TrimSpace(m.ModelDB.LiteLLMCatalogPath)
		if snapshotPath == "" {
			snapshotPath = filepath.Join(logsRoot, "modeldb", "litellm_catalog.json")
		}
		if _, err := os.Stat(snapshotPath); err != nil {
			return nil, fmt.Errorf("resume: missing per-run model catalog snapshot: %s", snapshotPath)
		}
		cat, err := modeldb.LoadLiteLLMCatalog(snapshotPath)
		if err != nil {
			return nil, err
		}
		catalog = cat
		backend = NewCodergenRouter(cfg, catalog)

		// Re-attach to the existing CXDB context head (metaspec required).
		baseURL := strings.TrimSpace(ov.CXDBHTTPBaseURL)
		if baseURL == "" {
			baseURL = strings.TrimSpace(cfg.CXDB.HTTPBaseURL)
		}
		if baseURL == "" {
			baseURL = strings.TrimSpace(m.CXDB.HTTPBaseURL)
		}
		contextID := strings.TrimSpace(ov.CXDBContextID)
		if contextID == "" {
			contextID = strings.TrimSpace(m.CXDB.ContextID)
		}
		if baseURL != "" && contextID != "" {
			cxdbClient := cxdb.New(baseURL)
			if err := cxdbClient.Health(ctx); err != nil {
				return nil, err
			}
			bin, err := cxdb.DialBinary(ctx, cfg.CXDB.BinaryAddr, fmt.Sprintf("kilroy/%s", m.RunID))
			if err != nil {
				return nil, err
			}
			defer func() { _ = bin.Close() }()
			bundleID, bundle, _, err := cxdb.KilroyAttractorRegistryBundle()
			if err != nil {
				return nil, err
			}
			if _, err := cxdbClient.PublishRegistryBundle(ctx, bundleID, bundle); err != nil {
				return nil, err
			}
			ci, err := cxdbClient.GetContext(ctx, contextID)
			if err != nil {
				return nil, err
			}
			sink = NewCXDBSink(cxdbClient, bin, m.RunID, contextID, ci.HeadTurnID, bundleID)
		}
	}

	eng := &Engine{
		Graph: g,
		Options: RunOptions{
			RepoPath: m.RepoPath,
			RunID:    m.RunID,
			LogsRoot: logsRoot,
		},
		DotSource:       dotSource,
		RunConfig:       cfg,
		RunBranch:       m.RunBranch,
		LogsRoot:        logsRoot,
		WorktreeDir:     filepath.Join(logsRoot, "worktree"),
		Context:         runtime.NewContext(),
		Registry:        NewDefaultRegistry(),
		Interviewer:     &AutoApproveInterviewer{},
		CodergenBackend: backend,
		CXDB:            sink,
		ModelCatalogSHA: func() string {
			if catalog == nil {
				return ""
			}
			return catalog.SHA256
		}(),
		ModelCatalogSource: m.ModelDB.LiteLLMCatalogSource,
		ModelCatalogPath: func() string {
			if catalog == nil {
				return ""
			}
			return catalog.Path
		}(),
	}
	eng.Context.ReplaceSnapshot(cp.ContextValues, cp.Logs)
	if cp != nil && cp.Extra != nil {
		// Metaspec/attractor-spec: if the previous hop used `full` fidelity, degrade to
		// summary:high for the first resumed node unless exact session restore is supported.
		// Kilroy v1 does not serialize in-memory sessions, so always degrade.
		if strings.EqualFold(strings.TrimSpace(fmt.Sprint(cp.Extra["last_fidelity"])), "full") {
			eng.forceNextFidelity = "summary:high"
		}
	}

	if !gitutil.IsRepo(m.RepoPath) {
		return nil, fmt.Errorf("not a git repo: %s", m.RepoPath)
	}
	clean, err := gitutil.IsClean(m.RepoPath)
	if err != nil {
		return nil, err
	}
	if !clean {
		return nil, fmt.Errorf("repo has uncommitted changes (resume requires clean repo)")
	}

	// Recreate branch pointer and worktree at the last checkpoint commit.
	// The run branch may currently be checked out by the existing worktree at logs_root/worktree.
	// Remove it first so we can safely force-move the branch pointer.
	_ = gitutil.RemoveWorktree(m.RepoPath, eng.WorktreeDir)
	if err := gitutil.CreateBranchAt(m.RepoPath, eng.RunBranch, cp.GitCommitSHA); err != nil {
		return nil, err
	}
	if err := gitutil.AddWorktree(m.RepoPath, eng.WorktreeDir, eng.RunBranch); err != nil {
		return nil, err
	}
	if err := gitutil.ResetHard(eng.WorktreeDir, cp.GitCommitSHA); err != nil {
		return nil, err
	}

	// Determine next node to execute by re-evaluating routing from the last completed node.
	lastNodeID := strings.TrimSpace(cp.CurrentNode)
	if lastNodeID == "" {
		return nil, fmt.Errorf("checkpoint missing current_node")
	}
	lastStatusPath := filepath.Join(logsRoot, lastNodeID, "status.json")
	b, err := os.ReadFile(lastStatusPath)
	if err != nil {
		return nil, fmt.Errorf("read last status.json: %w", err)
	}
	lastOutcome, err := runtime.DecodeOutcomeJSON(b)
	if err != nil {
		return nil, fmt.Errorf("decode last status.json: %w", err)
	}

	// Reconstruct node outcomes for goal gate enforcement from completed nodes (best-effort).
	nodeOutcomes := map[string]runtime.Outcome{}
	for _, id := range cp.CompletedNodes {
		if id == "" {
			continue
		}
		sb, err := os.ReadFile(filepath.Join(logsRoot, id, "status.json"))
		if err != nil {
			continue
		}
		o, err := runtime.DecodeOutcomeJSON(sb)
		if err != nil {
			continue
		}
		nodeOutcomes[id] = o
	}

	// Kilroy v1: parallel nodes control the next hop via context.
	if lastNode := eng.Graph.Nodes[lastNodeID]; lastNode != nil {
		t := strings.TrimSpace(lastNode.TypeOverride())
		if t == "" {
			t = shapeToType(lastNode.Shape())
		}
		if t == "parallel" {
			join := strings.TrimSpace(eng.Context.GetString("parallel.join_node", ""))
			if join == "" {
				return nil, fmt.Errorf("resume: parallel node missing parallel.join_node in checkpoint context")
			}
			return eng.runLoop(ctx, join, append([]string{}, cp.CompletedNodes...), copyStringIntMap(cp.NodeRetries), nodeOutcomes)
		}
	}

	nextEdge, err := selectNextEdge(eng.Graph, lastNodeID, lastOutcome, eng.Context)
	if err != nil {
		return nil, err
	}
	if nextEdge == nil {
		// Nothing to do; treat as completed.
		return &Result{
			RunID:          eng.Options.RunID,
			LogsRoot:       eng.LogsRoot,
			WorktreeDir:    eng.WorktreeDir,
			RunBranch:      eng.RunBranch,
			FinalStatus:    runtime.FinalSuccess,
			FinalCommitSHA: cp.GitCommitSHA,
		}, nil
	}

	// Continue traversal from next node.
	eng.incomingEdge = nextEdge
	return eng.runLoop(ctx, nextEdge.To, append([]string{}, cp.CompletedNodes...), copyStringIntMap(cp.NodeRetries), nodeOutcomes)
}

func loadManifest(path string) (*manifest, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	if strings.TrimSpace(m.RepoPath) == "" || strings.TrimSpace(m.RunBranch) == "" || strings.TrimSpace(m.RunID) == "" {
		return nil, fmt.Errorf("manifest missing required fields")
	}
	return &m, nil
}
