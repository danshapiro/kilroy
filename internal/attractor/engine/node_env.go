package engine

import (
	"os"
	"path/filepath"
	"strings"
)

// toolchainEnvKeys are environment variables that locate build toolchains
// (Rust, Go, etc.) relative to HOME. When a handler overrides HOME (e.g.,
// codex isolation), these must be pinned to their original absolute values
// so toolchains remain discoverable.
var toolchainEnvKeys = []string{
	"CARGO_HOME",  // Rust: defaults to $HOME/.cargo
	"RUSTUP_HOME", // Rust: defaults to $HOME/.rustup
	"GOPATH",      // Go: defaults to $HOME/go
	"GOMODCACHE",  // Go: defaults to $GOPATH/pkg/mod
}

// toolchainDefaults maps env keys to their default relative-to-HOME paths.
// If the key is not set in the environment, buildBaseNodeEnv pins it to
// $HOME/<default> so that later HOME overrides don't break toolchain lookup.
// Go defaults: GOPATH=$HOME/go, GOMODCACHE=$GOPATH/pkg/mod.
var toolchainDefaults = map[string]string{
	"CARGO_HOME":  ".cargo",
	"RUSTUP_HOME": ".rustup",
	"GOPATH":      "go",
}

// buildBaseNodeEnv constructs the base environment for any node execution.
// It:
//   - Starts from os.Environ()
//   - Strips CLAUDECODE (nested session protection)
//   - Pins toolchain paths to absolute values (immune to HOME overrides)
//   - Sets CARGO_TARGET_DIR inside worktree to avoid EXDEV errors
//
// Both ToolHandler and CodergenRouter should use this as their starting env,
// then apply handler-specific overrides on top.
func buildBaseNodeEnv(worktreeDir string) []string {
	base := os.Environ()

	// Snapshot HOME before any overrides.
	home := strings.TrimSpace(os.Getenv("HOME"))

	// Pin toolchain paths to absolute values. If not explicitly set,
	// infer from current HOME so a later HOME override doesn't break them.
	toolchainOverrides := map[string]string{}
	for _, key := range toolchainEnvKeys {
		val := strings.TrimSpace(os.Getenv(key))
		if val != "" {
			// Already set — pin the explicit value.
			toolchainOverrides[key] = val
		} else if defaultRel, ok := toolchainDefaults[key]; ok && home != "" {
			// Not set — pin the default (HOME-relative) path.
			toolchainOverrides[key] = filepath.Join(home, defaultRel)
		}
	}

	// GOMODCACHE defaults to $GOPATH/pkg/mod (not directly to HOME).
	// Pin it after the loop so we can use the resolved GOPATH value.
	// GOPATH can be a colon-separated list; Go uses the first entry
	// for GOMODCACHE, so we do the same.
	if strings.TrimSpace(os.Getenv("GOMODCACHE")) == "" {
		gopath := toolchainOverrides["GOPATH"]
		if gopath == "" {
			gopath = strings.TrimSpace(os.Getenv("GOPATH"))
		}
		if gopath != "" {
			// Use first entry of GOPATH list, matching Go's behavior.
			if first, _, ok := strings.Cut(gopath, string(filepath.ListSeparator)); ok {
				gopath = first
			}
			toolchainOverrides["GOMODCACHE"] = filepath.Join(gopath, "pkg", "mod")
		}
	}

	// Set CARGO_TARGET_DIR inside the worktree to avoid EXDEV errors
	// when cargo moves intermediate artifacts across filesystem boundaries.
	// Harmless for non-Rust projects (unused env var).
	if worktreeDir != "" && strings.TrimSpace(os.Getenv("CARGO_TARGET_DIR")) == "" {
		toolchainOverrides["CARGO_TARGET_DIR"] = filepath.Join(worktreeDir, ".cargo-target")
	}

	env := mergeEnvWithOverrides(base, toolchainOverrides)

	// Strip CLAUDECODE — it prevents the Claude CLI from launching
	// (nested session protection). All handler types need this stripped.
	return stripEnvKey(env, "CLAUDECODE")
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

// buildAgentLoopOverrides extracts the subset of base-node environment
// invariants needed by the API agent_loop path and merges contract env vars.
// It bridges buildBaseNodeEnv's []string format to agent.BaseEnv's map format.
func buildAgentLoopOverrides(worktreeDir string, contractEnv map[string]string) map[string]string {
	base := buildBaseNodeEnv(worktreeDir)
	keep := map[string]bool{
		"CARGO_HOME":       true,
		"RUSTUP_HOME":      true,
		"GOPATH":           true,
		"GOMODCACHE":       true,
		"CARGO_TARGET_DIR": true,
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
