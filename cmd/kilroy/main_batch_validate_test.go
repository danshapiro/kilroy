package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestAttractorValidateBatch_AllClean verifies exit code 0 and "ok" status when
// all files in a batch have no errors or warnings.
func TestAttractorValidateBatch_AllClean(t *testing.T) {
	bin := buildKilroyBinary(t)
	clean := testdataBatchFile(t, "clean.dot")

	code, out := runKilroy(t, bin, "attractor", "validate", "--batch", clean)
	if code != 0 {
		t.Fatalf("expected exit code 0 (clean), got %d\n%s", code, out)
	}
	if !strings.Contains(out, "ok") {
		t.Fatalf("expected 'ok' status in output, got:\n%s", out)
	}
}

// TestAttractorValidateBatch_StatusContractInPromptWarns verifies that a
// agent node with a prompt that does not reference KILROY_STAGE_STATUS_PATH
// triggers the status_contract_in_prompt warning (exit code 2).
func TestAttractorValidateBatch_StatusContractInPromptWarns(t *testing.T) {
	bin := buildKilroyBinary(t)
	f := testdataBatchFile(t, "no_status_contract.dot")

	code, out := runKilroy(t, bin, "attractor", "validate", "--batch", f)
	if code != 2 {
		t.Fatalf("expected exit code 2 (warnings-only), got %d\n%s", code, out)
	}
	if !strings.Contains(out, "status_contract_in_prompt") {
		t.Fatalf("expected status_contract_in_prompt warning in output, got:\n%s", out)
	}
}

// TestAttractorValidateBatch_WarningsOnly verifies exit code 2 when all files
// have warnings but no errors.
func TestAttractorValidateBatch_WarningsOnly(t *testing.T) {
	bin := buildKilroyBinary(t)
	warnFile := testdataBatchFile(t, "warnings_only.dot")

	code, out := runKilroy(t, bin, "attractor", "validate", "--batch", warnFile)
	if code != 2 {
		t.Fatalf("expected exit code 2 (warnings-only), got %d\n%s", code, out)
	}
	if !strings.Contains(out, "warn") {
		t.Fatalf("expected 'warn' status in output, got:\n%s", out)
	}
}

// TestAttractorValidateBatch_WithErrors verifies exit code 1 when any file has
// validation errors.
func TestAttractorValidateBatch_WithErrors(t *testing.T) {
	bin := buildKilroyBinary(t)
	errFile := testdataBatchFile(t, "has_errors.dot")

	code, out := runKilroy(t, bin, "attractor", "validate", "--batch", errFile)
	if code != 1 {
		t.Fatalf("expected exit code 1 (errors), got %d\n%s", code, out)
	}
	if !strings.Contains(out, "FAIL") {
		t.Fatalf("expected 'FAIL' status in output, got:\n%s", out)
	}
}

// TestAttractorValidateBatch_MixedFiles verifies exit code 1 when a batch
// contains both clean and errored files.
func TestAttractorValidateBatch_MixedFiles(t *testing.T) {
	bin := buildKilroyBinary(t)
	clean := testdataBatchFile(t, "clean.dot")
	errFile := testdataBatchFile(t, "has_errors.dot")

	code, out := runKilroy(t, bin, "attractor", "validate", "--batch", clean, errFile)
	if code != 1 {
		t.Fatalf("expected exit code 1 (any error wins), got %d\n%s", code, out)
	}
	// Both files should appear in summary.
	if !strings.Contains(out, filepath.Base(clean)) {
		t.Fatalf("expected clean file in output, got:\n%s", out)
	}
	if !strings.Contains(out, filepath.Base(errFile)) {
		t.Fatalf("expected error file in output, got:\n%s", out)
	}
}

// TestAttractorValidateBatch_JSONOutput verifies that --json emits a valid JSON
// array with per-file objects containing errors and warnings arrays.
func TestAttractorValidateBatch_JSONOutput(t *testing.T) {
	bin := buildKilroyBinary(t)
	clean := testdataBatchFile(t, "clean.dot")
	errFile := testdataBatchFile(t, "has_errors.dot")

	_, out := runKilroy(t, bin, "attractor", "validate", "--batch", clean, errFile, "--json")

	var results []struct {
		File     string `json:"file"`
		Errors   []any  `json:"errors"`
		Warnings []any  `json:"warnings"`
	}
	if err := json.Unmarshal([]byte(out), &results); err != nil {
		t.Fatalf("JSON parse failed: %v\nOutput:\n%s", err, out)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results in JSON, got %d", len(results))
	}
	// First file (clean.dot) should have no errors.
	if len(results[0].Errors) != 0 {
		t.Fatalf("clean.dot: expected 0 errors in JSON, got %d", len(results[0].Errors))
	}
	// Second file (has_errors.dot) should have at least one error.
	if len(results[1].Errors) == 0 {
		t.Fatalf("has_errors.dot: expected >0 errors in JSON, got 0")
	}
}

// TestAttractorValidateBatch_MissingFile verifies that a missing file is
// reported as an error and exit code 1 is returned.
func TestAttractorValidateBatch_MissingFile(t *testing.T) {
	bin := buildKilroyBinary(t)
	missing := filepath.Join(t.TempDir(), "nonexistent.dot")

	code, out := runKilroy(t, bin, "attractor", "validate", "--batch", missing)
	if code != 1 {
		t.Fatalf("expected exit code 1 for missing file, got %d\n%s", code, out)
	}
	_ = out // parse error is reported in the summary table
}

// TestAttractorValidateBatch_NoFilesExitsWithUsage verifies that --batch with
// no file arguments prints usage and exits non-zero.
func TestAttractorValidateBatch_NoFilesExitsWithUsage(t *testing.T) {
	bin := buildKilroyBinary(t)

	code, out := runKilroy(t, bin, "attractor", "validate", "--batch")
	if code == 0 {
		t.Fatalf("expected non-zero exit when no files given, got 0\n%s", out)
	}
	_ = out
}

// TestAttractorValidateBatch_ThreeFiles validates that three files are all
// processed and reported independently in the summary table.
func TestAttractorValidateBatch_ThreeFiles(t *testing.T) {
	bin := buildKilroyBinary(t)
	clean := testdataBatchFile(t, "clean.dot")
	warnFile := testdataBatchFile(t, "warnings_only.dot")
	errFile := testdataBatchFile(t, "has_errors.dot")

	_, out := runKilroy(t, bin, "attractor", "validate", "--batch", clean, warnFile, errFile)

	if !strings.Contains(out, "Total files: 3") {
		t.Fatalf("expected 'Total files: 3' in output, got:\n%s", out)
	}
}

// testdataBatchFile returns the absolute path to a file under
// cmd/kilroy/testdata/batch, failing the test if the file does not exist.
func testdataBatchFile(t *testing.T, name string) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	p := filepath.Join(wd, "testdata", "batch", name)
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("testdata file missing: %s: %v", p, err)
	}
	return p
}
