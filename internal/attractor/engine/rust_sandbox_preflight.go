package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/danshapiro/kilroy/internal/attractor/model"
	"github.com/danshapiro/kilroy/internal/attractor/runtime"
)

var rustSandboxPathHintRE = regexp.MustCompile("`([^`]+)`")

func maybeRunRustSandboxPreflight(ctx context.Context, node *model.Node, worktreeDir, stageDir string, env []string) (map[string]any, *runtime.Outcome) {
	if node == nil || strings.TrimSpace(worktreeDir) == "" || !looksLikeRustStage(node) {
		return nil, nil
	}

	meta := map[string]any{
		"enabled": true,
	}
	manifests := resolveRustSandboxPreflightManifests(node, worktreeDir)
	if len(manifests) == 0 {
		meta["status"] = "skipped"
		meta["reason"] = "no_manifest"
		return meta, nil
	}
	meta["manifests"] = manifests

	for _, manifestPath := range manifests {
		checkMeta, out := runRustSandboxPreflightForManifest(ctx, manifestPath, worktreeDir, env)
		checkMeta["manifest"] = manifestPath
		if out != nil {
			meta["status"] = "fail"
			meta["failure"] = checkMeta
			_ = writeJSON(filepath.Join(stageDir, "rust_sandbox_preflight.json"), meta)
			return meta, out
		}
	}

	meta["status"] = "pass"
	_ = writeJSON(filepath.Join(stageDir, "rust_sandbox_preflight.json"), meta)
	return meta, nil
}

func looksLikeRustStage(node *model.Node) bool {
	if node == nil {
		return false
	}
	text := strings.ToLower(strings.Join([]string{
		node.ID,
		node.Attr("prompt", ""),
		node.Attr("tool_command", ""),
	}, "\n"))
	for _, hint := range []string{"cargo", "rust", "wasm", "wasm32", "cargo.toml"} {
		if strings.Contains(text, hint) {
			return true
		}
	}
	return false
}

func resolveRustSandboxPreflightManifests(node *model.Node, worktreeDir string) []string {
	seen := map[string]struct{}{}
	addManifest := func(path string) {
		abs, ok := normalizeCandidateWithinWorktree(path, worktreeDir)
		if !ok {
			return
		}
		info, err := os.Stat(abs)
		if err == nil && !info.IsDir() && strings.EqualFold(filepath.Base(abs), "Cargo.toml") {
			seen[abs] = struct{}{}
			return
		}
		if err == nil && info.IsDir() {
			manifestPath := filepath.Join(abs, "Cargo.toml")
			if stat, statErr := os.Stat(manifestPath); statErr == nil && !stat.IsDir() {
				seen[manifestPath] = struct{}{}
			}
		}
	}

	for _, text := range []string{node.Attr("prompt", ""), node.Attr("tool_command", "")} {
		matches := rustSandboxPathHintRE.FindAllStringSubmatch(text, -1)
		for _, match := range matches {
			if len(match) < 2 {
				continue
			}
			addManifest(strings.TrimSpace(match[1]))
		}
	}

	manifests := sortedStringSetKeys(seen)
	if len(manifests) > 0 {
		return manifests
	}

	// Fallback: if exactly one crate exists in the worktree, use it.
	fallback := discoverCargoManifests(worktreeDir, 6, 64)
	if len(fallback) == 1 {
		return fallback
	}
	return nil
}

func normalizeCandidateWithinWorktree(candidate, worktreeDir string) (string, bool) {
	candidate = strings.TrimSpace(candidate)
	if candidate == "" {
		return "", false
	}
	absWorktree, err := filepath.Abs(worktreeDir)
	if err != nil {
		return "", false
	}
	var absCandidate string
	if filepath.IsAbs(candidate) {
		absCandidate = filepath.Clean(candidate)
	} else {
		absCandidate = filepath.Join(absWorktree, filepath.Clean(candidate))
	}
	rel, err := filepath.Rel(absWorktree, absCandidate)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return absCandidate, true
}

func discoverCargoManifests(worktreeDir string, maxDepth int, maxResults int) []string {
	if maxDepth < 1 {
		maxDepth = 1
	}
	if maxResults < 1 {
		maxResults = 1
	}
	root, err := filepath.Abs(worktreeDir)
	if err != nil {
		return nil
	}
	var manifests []string
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if d.IsDir() {
			name := d.Name()
			if name == ".git" || name == "node_modules" || name == "target" || name == ".cargo-target" {
				return filepath.SkipDir
			}
			if rel, err := filepath.Rel(root, path); err == nil && rel != "." {
				depth := strings.Count(rel, string(filepath.Separator)) + 1
				if depth > maxDepth {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if !strings.EqualFold(d.Name(), "Cargo.toml") {
			return nil
		}
		manifests = append(manifests, path)
		if len(manifests) >= maxResults {
			return errors.New("stop")
		}
		return nil
	})
	sort.Strings(manifests)
	return manifests
}

func runRustSandboxPreflightForManifest(ctx context.Context, manifestPath, worktreeDir string, env []string) (map[string]any, *runtime.Outcome) {
	checkMeta := map[string]any{
		"check": "cargo_metadata",
	}
	cmd := exec.CommandContext(ctx, "cargo", "metadata", "--format-version", "1", "--manifest-path", manifestPath, "--no-deps")
	cmd.Dir = worktreeDir
	cmd.Env = env
	outBytes, err := cmd.CombinedOutput()
	output := strings.TrimSpace(string(outBytes))
	if err != nil {
		if class, sig, reason, ok := classifyRustSandboxPreflightInfraFailure(err, output); ok {
			checkMeta["error"] = Truncate(output, 500)
			return checkMeta, rustSandboxPreflightRetryOutcome(reason, class, sig)
		}
		checkMeta["status"] = "skipped_non_infra_failure"
		checkMeta["error"] = Truncate(output, 500)
		return checkMeta, nil
	}

	targetDir := strings.TrimSpace(envListLookup(env, "CARGO_TARGET_DIR"))
	if targetDir == "" {
		targetDir = filepath.Join(filepath.Dir(manifestPath), "target")
	}
	if !filepath.IsAbs(targetDir) {
		targetDir = filepath.Join(worktreeDir, targetDir)
	}
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		checkMeta["check"] = "rename_probe"
		checkMeta["error"] = err.Error()
		return checkMeta, rustSandboxPreflightRetryOutcome(
			"Rust sandbox preflight failed: unable to prepare target dir for rename probe",
			failureClassTransientInfra,
			"rust_preflight|rename_probe|target_dir_unavailable",
		)
	}

	// Create the probe temp file under targetDir so the probe validates
	// destination filesystem writability without introducing a cross-FS move.
	tmpFile, err := os.CreateTemp(targetDir, ".kilroy-rust-preflight-src-*")
	if err != nil {
		checkMeta["check"] = "rename_probe"
		checkMeta["error"] = err.Error()
		return checkMeta, rustSandboxPreflightRetryOutcome(
			"Rust sandbox preflight failed: unable to create temp probe file",
			failureClassTransientInfra,
			"rust_preflight|rename_probe|tmp_create_failed",
		)
	}
	tmpName := tmpFile.Name()
	_, _ = tmpFile.WriteString("kilroy-rust-preflight")
	_ = tmpFile.Close()
	defer func() { _ = os.Remove(tmpName) }()

	dst := filepath.Join(targetDir, fmt.Sprintf(".kilroy-rust-preflight-%d", time.Now().UnixNano()))
	if err := os.Rename(tmpName, dst); err != nil {
		lower := strings.ToLower(err.Error())
		checkMeta["check"] = "rename_probe"
		checkMeta["error"] = err.Error()
		if errors.Is(err, syscall.EXDEV) || strings.Contains(lower, "cross-device link") || strings.Contains(lower, "os error 18") {
			return checkMeta, rustSandboxPreflightRetryOutcome(
				"WASM build failed with Invalid cross-device link (os error 18) while writing crate metadata artifacts.",
				failureClassTransientInfra,
				"rust_preflight|rename_probe|cross_device_link",
			)
		}
		return checkMeta, rustSandboxPreflightRetryOutcome(
			"Rust sandbox preflight failed: filesystem rename probe could not complete",
			failureClassTransientInfra,
			"rust_preflight|rename_probe|filesystem_io",
		)
	}
	_ = os.Remove(dst)
	checkMeta["status"] = "pass"
	return checkMeta, nil
}

func classifyRustSandboxPreflightInfraFailure(runErr error, output string) (failureClass string, failureSignature string, failureReason string, ok bool) {
	combined := strings.ToLower(strings.TrimSpace(fmt.Sprintf("%v\n%s", runErr, output)))
	switch {
	case strings.Contains(combined, "could not resolve host"),
		strings.Contains(combined, "could not resolve hostname"),
		strings.Contains(combined, "temporary failure in name resolution"),
		strings.Contains(combined, "index.crates.io"),
		strings.Contains(combined, "download of config.json failed"),
		strings.Contains(combined, "failed to download from `https://index.crates.io"),
		strings.Contains(combined, "failed to download from https://index.crates.io"),
		strings.Contains(combined, "network is unreachable"),
		strings.Contains(combined, "connection timed out"):
		return failureClassTransientInfra, "rust_preflight|cargo_metadata|registry_unavailable", "toolchain_or_dependency_registry_unavailable", true
	case strings.Contains(combined, "invalid cross-device link"),
		strings.Contains(combined, "cross-device link"),
		strings.Contains(combined, "os error 18"):
		return failureClassTransientInfra, "rust_preflight|cargo_metadata|cross_device_link", "WASM build failed with Invalid cross-device link (os error 18) while writing crate metadata artifacts.", true
	default:
		return "", "", "", false
	}
}

func rustSandboxPreflightRetryOutcome(reason, failureClass, failureSignature string) *runtime.Outcome {
	return &runtime.Outcome{
		Status:        runtime.StatusRetry,
		FailureReason: strings.TrimSpace(reason),
		Notes:         "rust sandbox preflight failed before stage execution",
		Meta: map[string]any{
			"failure_class":     failureClass,
			"failure_signature": failureSignature,
		},
		ContextUpdates: map[string]any{
			"failure_class": failureClass,
		},
	}
}

func sortedStringSetKeys(set map[string]struct{}) []string {
	if len(set) == 0 {
		return nil
	}
	out := make([]string, 0, len(set))
	for key := range set {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

func envListLookup(env []string, key string) string {
	prefix := key + "="
	val := ""
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			val = strings.TrimPrefix(entry, prefix)
		}
	}
	return val
}
