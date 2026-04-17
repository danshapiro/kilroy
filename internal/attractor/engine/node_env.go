package engine

import (
	"os"
	"path/filepath"
	"strings"
)

const (
	runIDEnvKey          = "KILROY_RUN_ID"
	nodeIDEnvKey         = "KILROY_NODE_ID"
	logsRootEnvKey       = "KILROY_LOGS_ROOT"
	stageLogsDirEnvKey   = "KILROY_STAGE_LOGS_DIR"
	worktreeDirEnvKey    = "KILROY_WORKTREE_DIR"
	inputsManifestEnvKey = "KILROY_INPUTS_MANIFEST_PATH"
	dataDirEnvKey        = "KILROY_DATA_DIR"
)

var baseNodeEnvStripKeys = []string{
	"CLAUDECODE",
	runIDEnvKey,
	nodeIDEnvKey,
	logsRootEnvKey,
	stageLogsDirEnvKey,
	worktreeDirEnvKey,
	inputsManifestEnvKey,
	stageStatusPathEnvKey,
	stageStatusFallbackPathEnvKey,
	dataDirEnvKey,
}

// buildBaseNodeEnv constructs the base environment for any node execution.
// It starts from os.Environ(), strips CLAUDECODE, then applies resolved
// artifact policy environment variables.
func buildBaseNodeEnv(rp ResolvedArtifactPolicy) []string {
	base := os.Environ()
	// Scrub inherited process state that can corrupt stage-local contracts.
	for _, key := range baseNodeEnvStripKeys {
		base = stripEnvKey(base, key)
	}

	overrides := make(map[string]string, len(rp.Env.Vars))
	for k, v := range rp.Env.Vars {
		key := strings.TrimSpace(k)
		if key == "" {
			continue
		}
		overrides[key] = v
	}
	return mergeEnvWithOverrides(base, overrides)
}

// stripEnvKey removes all entries with the given key from an env slice.
func stripEnvKey(env []string, key string) []string {
	prefix := key + "="
	out := make([]string, 0, len(env))
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) || entry == key {
			continue
		}
		out = append(out, entry)
	}
	return out
}

// BuildStageRuntimeEnv returns stable per-stage environment variables that
// help agent/tool nodes find their run-local state (logs, worktree, etc.).
func BuildStageRuntimeEnv(execCtx *Execution, nodeID string) map[string]string {
	out := map[string]string{}
	if execCtx == nil {
		return out
	}
	if execCtx.Engine != nil {
		if runID := strings.TrimSpace(execCtx.Engine.Options.RunID); runID != "" {
			out[runIDEnvKey] = runID
		}
	}
	if id := strings.TrimSpace(nodeID); id != "" {
		out[nodeIDEnvKey] = id
	}
	if logsRoot := strings.TrimSpace(execCtx.LogsRoot); logsRoot != "" {
		out[logsRootEnvKey] = logsRoot
		if id := strings.TrimSpace(nodeID); id != "" {
			out[stageLogsDirEnvKey] = filepath.Join(logsRoot, id)
		}
	}
	if worktree := strings.TrimSpace(execCtx.WorktreeDir); worktree != "" {
		out[worktreeDirEnvKey] = worktree
		out[dataDirEnvKey] = filepath.Join(worktree, kilroyDir)
	}
	// Add structured input env vars (KILROY_INPUT_*).
	if execCtx.Engine != nil {
		for k, v := range InputEnvVars(execCtx.Engine.Options.Inputs) {
			out[k] = v
		}
	}
	if execCtx.Engine != nil && execCtx.Engine.inputMaterializationEnabled() {
		manifestPath := strings.TrimSpace(execCtx.Engine.currentInputManifestPath)
		if manifestPath == "" && strings.TrimSpace(execCtx.LogsRoot) != "" && strings.TrimSpace(nodeID) != "" {
			manifestPath = inputStageManifestPath(execCtx.LogsRoot, nodeID)
		}
		if strings.TrimSpace(manifestPath) != "" {
			if _, err := os.Stat(manifestPath); err == nil {
				out[inputsManifestEnvKey] = manifestPath
			}
		}
	}
	return out
}

func buildStageRuntimePreamble(execCtx *Execution, nodeID string) string {
	if execCtx == nil {
		return ""
	}
	runID := ""
	if execCtx.Engine != nil {
		runID = strings.TrimSpace(execCtx.Engine.Options.RunID)
	}
	logsRoot := strings.TrimSpace(execCtx.LogsRoot)
	worktree := strings.TrimSpace(execCtx.WorktreeDir)
	stageDir := ""
	if logsRoot != "" && strings.TrimSpace(nodeID) != "" {
		stageDir = filepath.Join(logsRoot, strings.TrimSpace(nodeID))
	}
	if runID == "" && logsRoot == "" && stageDir == "" && worktree == "" && strings.TrimSpace(nodeID) == "" {
		return ""
	}
	dataDir := ""
	if worktree != "" {
		dataDir = filepath.Join(worktree, kilroyDir)
	}
	return strings.TrimSpace(
		"Execution context:\n" +
			"- $" + runIDEnvKey + "=" + runID + "\n" +
			"- $" + logsRootEnvKey + "=" + logsRoot + "\n" +
			"- $" + stageLogsDirEnvKey + "=" + stageDir + "\n" +
			"- $" + worktreeDirEnvKey + "=" + worktree + "\n" +
			"- $" + nodeIDEnvKey + "=" + strings.TrimSpace(nodeID) + "\n" +
			"- $" + dataDirEnvKey + "=" + dataDir + "\n",
	)
}

// buildAgentLoopOverrides extracts the subset of base-node environment
// invariants needed by the API agent_loop path and merges contract env vars.
// It bridges buildBaseNodeEnv's []string format to agent.BaseEnv's map format.
func buildAgentLoopOverrides(rp ResolvedArtifactPolicy, contractEnv map[string]string) map[string]string {
	base := buildBaseNodeEnv(rp)
	keep := make(map[string]bool, len(rp.Env.Vars))
	for k := range rp.Env.Vars {
		key := strings.TrimSpace(k)
		if key == "" {
			continue
		}
		keep[key] = true
	}
	out := make(map[string]string, len(contractEnv)+len(keep))
	for _, kv := range base {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		if keep[k] {
			out[k] = v
		}
	}
	for k, v := range contractEnv {
		out[k] = v
	}
	return out
}

func artifactPolicyFromExecution(execCtx *Execution) ResolvedArtifactPolicy {
	if execCtx == nil || execCtx.Engine == nil {
		return ResolvedArtifactPolicy{}
	}
	return execCtx.Engine.ArtifactPolicy
}
