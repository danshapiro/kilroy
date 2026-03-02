package engine

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestDiscoverBrowserArtifacts_PlaywrightAndCypress(t *testing.T) {
	worktree := t.TempDir()

	mustWriteFile(t, filepath.Join(worktree, "playwright-report", "index.html"), "ok")
	mustWriteFile(t, filepath.Join(worktree, "test-results", "junit.xml"), "ok")
	mustWriteFile(t, filepath.Join(worktree, "cypress", "videos", "spec.mp4"), "ok")
	mustWriteFile(t, filepath.Join(worktree, "cypress", "screenshots", "spec.png"), "ok")
	mustWriteFile(t, filepath.Join(worktree, "junit-report.xml"), "ok")

	artifacts, err := discoverBrowserArtifacts(worktree)
	if err != nil {
		t.Fatalf("discoverBrowserArtifacts: %v", err)
	}

	got := artifactRelPaths(artifacts)
	want := []string{
		"cypress/screenshots/spec.png",
		"cypress/videos/spec.mp4",
		"junit-report.xml",
		"playwright-report/index.html",
		"test-results/junit.xml",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("discover rel paths mismatch:\n got=%v\nwant=%v", got, want)
	}
}

func TestDiscoverBrowserArtifacts_FindsNestedSegmentMatches(t *testing.T) {
	worktree := t.TempDir()

	mustWriteFile(t, filepath.Join(worktree, "frontend", "playwright-report", "index.html"), "ok")
	mustWriteFile(t, filepath.Join(worktree, "apps", "web", "test-results", "junit.xml"), "ok")
	mustWriteFile(t, filepath.Join(worktree, "packages", "ui", "cypress", "videos", "spec.mp4"), "ok")
	mustWriteFile(t, filepath.Join(worktree, "packages", "ui", "cypress", "screenshots", "spec.png"), "ok")

	artifacts, err := discoverBrowserArtifacts(worktree)
	if err != nil {
		t.Fatalf("discoverBrowserArtifacts: %v", err)
	}

	got := artifactRelPaths(artifacts)
	want := []string{
		"apps/web/test-results/junit.xml",
		"frontend/playwright-report/index.html",
		"packages/ui/cypress/screenshots/spec.png",
		"packages/ui/cypress/videos/spec.mp4",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("discover rel paths mismatch:\n got=%v\nwant=%v", got, want)
	}
}

func TestBrowserArtifactCandidate_DoesNotMatchPartialSegmentNames(t *testing.T) {
	cases := []string{
		"frontend/playwright-reporter/index.html",
		"apps/web/test-results-old/result.xml",
		"packages/ui/cypress/videos2/spec.mp4",
		"packages/ui/cypress-screenshots/spec.png",
	}
	for _, rel := range cases {
		if browserArtifactCandidate(rel) {
			t.Fatalf("expected false for partial segment match: %q", rel)
		}
	}
}

func TestCollectBrowserArtifacts_CopiesIntoStageBrowserArtifactsDir(t *testing.T) {
	worktree := t.TempDir()
	stageDir := t.TempDir()

	mustWriteFile(t, filepath.Join(worktree, "playwright-report", "index.html"), "index")
	mustWriteFile(t, filepath.Join(worktree, "test-results", "result.txt"), "result")

	summary, err := collectBrowserArtifacts(stageDir, worktree, nil, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("collectBrowserArtifacts: %v", err)
	}
	if summary.CopiedFiles != 2 {
		t.Fatalf("copied files: got %d want 2", summary.CopiedFiles)
	}

	dstRoot := filepath.Join(stageDir, browserArtifactsDirName)
	assertExists(t, filepath.Join(dstRoot, "playwright-report", "index.html"))
	assertExists(t, filepath.Join(dstRoot, "test-results", "result.txt"))

	content, err := os.ReadFile(filepath.Join(dstRoot, "test-results", "result.txt"))
	if err != nil {
		t.Fatalf("read copied file: %v", err)
	}
	if string(content) != "result" {
		t.Fatalf("copied content mismatch: got %q want %q", string(content), "result")
	}
}

func TestCollectBrowserArtifacts_NoMatches_NoError(t *testing.T) {
	worktree := t.TempDir()
	stageDir := t.TempDir()

	summary, err := collectBrowserArtifacts(stageDir, worktree, nil, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("collectBrowserArtifacts: %v", err)
	}
	if summary.CopiedFiles != 0 {
		t.Fatalf("copied files: got %d want 0", summary.CopiedFiles)
	}
}

func TestDiscoverBrowserArtifacts_ExcludesPlaywrightCache(t *testing.T) {
	worktree := t.TempDir()

	mustWriteFile(t, filepath.Join(worktree, "playwright", ".cache", "cache.bin"), "cache")
	mustWriteFile(t, filepath.Join(worktree, "playwright-report", "index.html"), "index")

	artifacts, err := discoverBrowserArtifacts(worktree)
	if err != nil {
		t.Fatalf("discoverBrowserArtifacts: %v", err)
	}
	got := artifactRelPaths(artifacts)
	for _, rel := range got {
		if strings.Contains(rel, "playwright/.cache") {
			t.Fatalf("unexpected cache artifact in result: %q", rel)
		}
	}
	if !slices.Contains(got, "playwright-report/index.html") {
		t.Fatalf("expected playwright-report artifact to be discovered")
	}
}

func TestCollectBrowserArtifacts_OnlyCollectsNewOrModifiedVsPreRunSnapshot(t *testing.T) {
	worktree := t.TempDir()
	stageDir := t.TempDir()

	unchanged := filepath.Join(worktree, "playwright-report", "unchanged.html")
	modified := filepath.Join(worktree, "test-results", "modified.txt")
	mustWriteFile(t, unchanged, "unchanged")
	mustWriteFile(t, modified, "old")

	baseline, err := snapshotBrowserArtifacts(worktree)
	if err != nil {
		t.Fatalf("snapshotBrowserArtifacts: %v", err)
	}

	startedAt := time.Now()
	time.Sleep(20 * time.Millisecond)
	mustWriteFile(t, modified, "new")
	mustWriteFile(t, filepath.Join(worktree, "test-results", "new.txt"), "new")

	summary, err := collectBrowserArtifacts(stageDir, worktree, baseline, startedAt)
	if err != nil {
		t.Fatalf("collectBrowserArtifacts: %v", err)
	}
	if summary.CopiedFiles != 2 {
		t.Fatalf("copied files: got %d want 2", summary.CopiedFiles)
	}

	dstRoot := filepath.Join(stageDir, browserArtifactsDirName)
	assertExists(t, filepath.Join(dstRoot, "test-results", "modified.txt"))
	assertExists(t, filepath.Join(dstRoot, "test-results", "new.txt"))
	if _, err := os.Stat(filepath.Join(dstRoot, "playwright-report", "unchanged.html")); err == nil {
		t.Fatalf("unchanged artifact should not be copied")
	}
}

func TestCollectBrowserArtifacts_RespectsPerFileAndTotalSizeCaps(t *testing.T) {
	worktree := t.TempDir()
	stageDir := t.TempDir()

	mustCreateSizedFile(t, filepath.Join(worktree, "playwright-report", "too-big.bin"), browserArtifactPerFileCapBytes+1)
	for i := 0; i < 6; i++ {
		mustCreateSizedFile(
			t,
			filepath.Join(worktree, "playwright-report", "chunk-"+string(rune('0'+i))+".bin"),
			9*1024*1024,
		)
	}

	summary, err := collectBrowserArtifacts(stageDir, worktree, nil, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("collectBrowserArtifacts: %v", err)
	}
	if summary.SkippedPerFileCap == 0 {
		t.Fatalf("expected per-file cap skip")
	}
	if summary.SkippedTotalCap == 0 {
		t.Fatalf("expected total-cap skip")
	}
}

func TestCollectBrowserArtifacts_SkipsSymlinksAndOutOfTreeTargets(t *testing.T) {
	worktree := t.TempDir()
	stageDir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.txt")

	mustWriteFile(t, outside, "outside")
	mustWriteFile(t, filepath.Join(worktree, "test-results", "good.txt"), "good")
	if err := os.MkdirAll(filepath.Join(worktree, "test-results"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(worktree, "test-results", "outside-link.txt")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	summary, err := collectBrowserArtifacts(stageDir, worktree, nil, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("collectBrowserArtifacts: %v", err)
	}
	if summary.SkippedSymlink == 0 {
		t.Fatalf("expected symlink skip")
	}

	dstRoot := filepath.Join(stageDir, browserArtifactsDirName)
	assertExists(t, filepath.Join(dstRoot, "test-results", "good.txt"))
	if _, err := os.Stat(filepath.Join(dstRoot, "test-results", "outside-link.txt")); err == nil {
		t.Fatalf("symlink target should not be copied")
	}
}

func TestCollectBrowserArtifacts_ClearsPreviousAttemptDestination(t *testing.T) {
	worktree := t.TempDir()
	stageDir := t.TempDir()
	dstRoot := filepath.Join(stageDir, browserArtifactsDirName)

	mustWriteFile(t, filepath.Join(dstRoot, "stale.txt"), "stale")
	mustWriteFile(t, filepath.Join(worktree, "playwright-report", "fresh.txt"), "fresh")

	if _, err := collectBrowserArtifacts(stageDir, worktree, nil, time.Now().Add(-time.Hour)); err != nil {
		t.Fatalf("collectBrowserArtifacts: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dstRoot, "stale.txt")); err == nil {
		t.Fatalf("stale destination file should be removed before copy")
	}
	assertExists(t, filepath.Join(dstRoot, "playwright-report", "fresh.txt"))
}

func artifactRelPaths(in []browserArtifact) []string {
	out := make([]string, 0, len(in))
	for _, item := range in {
		out = append(out, item.RelPath)
	}
	slices.Sort(out)
	return out
}

func mustWriteFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func mustCreateSizedFile(t *testing.T, path string, size int64) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	defer func() { _ = f.Close() }()
	if err := f.Truncate(size); err != nil {
		t.Fatalf("truncate %s: %v", path, err)
	}
}
