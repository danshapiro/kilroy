package engine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAnthropicCLIContract_InvocationArtifactIncludesStreamJSONAndVerbose(t *testing.T) {
	repo := initTestRepo(t)
	logsRoot := t.TempDir()
	catalog := writeCatalogForPreflight(t, `{
  "data": [
    {"id": "anthropic/claude-sonnet-4-20250514"}
  ]
}`)
	cxdbSrv := newCXDBTestServer(t)

	cli := filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(cli, []byte(`#!/usr/bin/env bash
set -euo pipefail
if [[ "${1:-}" == "--help" ]]; then
cat <<'EOF'
Usage: claude -p --output-format stream-json --verbose --model MODEL
EOF
exit 0
fi
cat > status.json <<'JSON'
{"status":"success","notes":"ok"}
JSON
echo '{"type":"done","text":"ok"}'
`), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := &RunConfigFile{Version: 1}
	cfg.Repo.Path = repo
	cfg.CXDB.BinaryAddr = cxdbSrv.BinaryAddr()
	cfg.CXDB.HTTPBaseURL = cxdbSrv.URL()
	cfg.LLM.CLIProfile = "test_shim"
	cfg.LLM.Providers = map[string]ProviderConfig{
		"anthropic": {Backend: BackendCLI, Executable: cli},
	}
	cfg.ModelDB.LiteLLMCatalogPath = catalog
	cfg.ModelDB.LiteLLMCatalogUpdatePolicy = "pinned"
	cfg.Git.RunBranchPrefix = "attractor/run"

	dot := singleProviderDot("anthropic", "claude-sonnet-4-20250514")

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	res, err := RunWithConfig(ctx, dot, cfg, RunOptions{RunID: "anthropic-contract-ok", LogsRoot: logsRoot, AllowTestShim: true})
	if err != nil {
		t.Fatalf("RunWithConfig: %v", err)
	}

	invPath := filepath.Join(res.LogsRoot, "a", "cli_invocation.json")
	b, err := os.ReadFile(invPath)
	if err != nil {
		t.Fatalf("read %s: %v", invPath, err)
	}
	var inv map[string]any
	if err := json.Unmarshal(b, &inv); err != nil {
		t.Fatalf("decode %s: %v", invPath, err)
	}
	if strings.TrimSpace(anyToString(inv["provider"])) != "anthropic" {
		t.Fatalf("provider: got %q want %q", anyToString(inv["provider"]), "anthropic")
	}
	argvAny, ok := inv["argv"].([]any)
	if !ok {
		t.Fatalf("argv missing/invalid in invocation: %#v", inv["argv"])
	}
	argv := make([]string, 0, len(argvAny))
	for _, v := range argvAny {
		argv = append(argv, strings.TrimSpace(anyToString(v)))
	}
	if !hasArg(argv, "--output-format") || !hasArg(argv, "stream-json") {
		t.Fatalf("expected stream-json contract flags in argv, got %v", argv)
	}
	if !hasArg(argv, "--verbose") {
		t.Fatalf("expected --verbose for anthropic stream-json contract, got %v", argv)
	}
}

func TestAnthropicCLIContract_PreflightFailsWhenVerboseCapabilityMissing(t *testing.T) {
	repo := initTestRepo(t)
	catalog := writeCatalogForPreflight(t, `{
  "data": [
    {"id": "anthropic/claude-sonnet-4-20250514"}
  ]
}`)
	claudeCLI := writeFakeCLI(t, "claude", "Usage: claude -p --output-format stream-json --model MODEL", 0)

	cfg := testPreflightConfigForProviders(repo, catalog, map[string]BackendKind{
		"anthropic": BackendCLI,
	})
	cfg.LLM.Providers["anthropic"] = ProviderConfig{Backend: BackendCLI, Executable: claudeCLI}
	dot := singleProviderDot("anthropic", "claude-sonnet-4-20250514")

	logsRoot := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := RunWithConfig(ctx, dot, cfg, RunOptions{RunID: "anthropic-contract-missing-verbose", LogsRoot: logsRoot, AllowTestShim: true})
	if err == nil {
		t.Fatalf("expected anthropic preflight failure, got nil")
	}
	if !strings.Contains(err.Error(), "preflight: provider anthropic capability probe missing required tokens") {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(err.Error(), "--verbose") {
		t.Fatalf("expected missing --verbose token in error, got %v", err)
	}
}
