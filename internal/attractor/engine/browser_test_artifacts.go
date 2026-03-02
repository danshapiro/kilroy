package engine

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

const (
	browserArtifactsDirName        = "browser_artifacts"
	browserArtifactPerFileCapBytes = int64(10 * 1024 * 1024)
	browserArtifactTotalCapBytes   = int64(50 * 1024 * 1024)
)

var browserArtifactPathSegments = []string{
	"playwright-report/",
	"test-results/",
	"cypress/videos/",
	"cypress/screenshots/",
}

var browserArtifactNameGlobs = []string{
	"junit*.xml",
	"*.trace.zip",
}

type artifactFingerprint struct {
	SizeBytes       int64
	ModTimeUnixNano int64
}

type browserArtifact struct {
	AbsPath   string
	RelPath   string
	SizeBytes int64
	ModTime   time.Time
}

type browserArtifactSummary struct {
	DiscoveredFiles      int
	CopiedFiles          int
	CopiedBytes          int64
	SkippedUnchanged     int
	SkippedBeforeStart   int
	SkippedPerFileCap    int
	SkippedTotalCap      int
	SkippedSymlink       int
	SkippedOutsideSource int
	SkippedCopyError     int
	CopiedRelPaths       []string
}

func (s browserArtifactSummary) toProgressFields() map[string]any {
	return map[string]any{
		"discovered_files":       s.DiscoveredFiles,
		"copied_files":           s.CopiedFiles,
		"copied_bytes":           s.CopiedBytes,
		"skipped_unchanged":      s.SkippedUnchanged,
		"skipped_before_start":   s.SkippedBeforeStart,
		"skipped_per_file_cap":   s.SkippedPerFileCap,
		"skipped_total_cap":      s.SkippedTotalCap,
		"skipped_symlink":        s.SkippedSymlink,
		"skipped_outside_source": s.SkippedOutsideSource,
		"skipped_copy_error":     s.SkippedCopyError,
		"copied_paths":           append([]string{}, s.CopiedRelPaths...),
	}
}

func snapshotBrowserArtifacts(worktreeDir string) (map[string]artifactFingerprint, error) {
	artifacts, err := discoverBrowserArtifacts(worktreeDir)
	if err != nil {
		return nil, err
	}
	out := make(map[string]artifactFingerprint, len(artifacts))
	for _, item := range artifacts {
		info, err := os.Lstat(item.AbsPath)
		if err != nil {
			continue
		}
		if !info.Mode().IsRegular() {
			continue
		}
		out[item.RelPath] = artifactFingerprint{
			SizeBytes:       info.Size(),
			ModTimeUnixNano: info.ModTime().UnixNano(),
		}
	}
	return out, nil
}

func discoverBrowserArtifacts(worktreeDir string) ([]browserArtifact, error) {
	root, err := filepath.Abs(strings.TrimSpace(worktreeDir))
	if err != nil {
		return nil, err
	}
	root = filepath.Clean(root)

	items := map[string]browserArtifact{}
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if p == root {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if rel == "." || rel == "" {
			return nil
		}
		if d.IsDir() {
			if shouldSkipBrowserWalkDir(rel) {
				return filepath.SkipDir
			}
			return nil
		}
		if !browserArtifactCandidate(rel) {
			return nil
		}
		if isExcludedBrowserArtifactPath(rel) {
			return nil
		}
		info, err := os.Lstat(p)
		if err != nil {
			return nil
		}
		if !info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 {
			return nil
		}
		items[rel] = browserArtifact{
			AbsPath:   p,
			RelPath:   rel,
			SizeBytes: info.Size(),
			ModTime:   info.ModTime(),
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	keys := make([]string, 0, len(items))
	for k := range items {
		keys = append(keys, k)
	}
	slices.Sort(keys)

	out := make([]browserArtifact, 0, len(keys))
	for _, k := range keys {
		out = append(out, items[k])
	}
	return out, nil
}

func collectBrowserArtifacts(stageDir, worktreeDir string, baseline map[string]artifactFingerprint, startedAt time.Time) (browserArtifactSummary, error) {
	var summary browserArtifactSummary
	if strings.TrimSpace(stageDir) == "" || strings.TrimSpace(worktreeDir) == "" {
		return summary, fmt.Errorf("stageDir and worktreeDir are required")
	}

	destRoot := filepath.Join(stageDir, browserArtifactsDirName)
	if err := os.RemoveAll(destRoot); err != nil {
		return summary, err
	}

	artifacts, err := discoverBrowserArtifacts(worktreeDir)
	if err != nil {
		return summary, err
	}
	summary.DiscoveredFiles = len(artifacts)

	sourceRoot, err := filepath.Abs(worktreeDir)
	if err != nil {
		return summary, err
	}
	sourceRoot = filepath.Clean(sourceRoot)

	for _, item := range artifacts {
		info, err := os.Lstat(item.AbsPath)
		if err != nil {
			summary.SkippedCopyError++
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 {
			summary.SkippedSymlink++
			continue
		}
		if !info.Mode().IsRegular() {
			continue
		}
		if !pathWithin(item.AbsPath, sourceRoot) {
			summary.SkippedOutsideSource++
			continue
		}

		current := artifactFingerprint{
			SizeBytes:       info.Size(),
			ModTimeUnixNano: info.ModTime().UnixNano(),
		}
		if prior, ok := baseline[item.RelPath]; ok {
			if prior == current {
				summary.SkippedUnchanged++
				continue
			}
			if !startedAt.IsZero() && info.ModTime().Before(startedAt) {
				summary.SkippedBeforeStart++
				continue
			}
		} else if baseline == nil && !startedAt.IsZero() && info.ModTime().Before(startedAt) {
			summary.SkippedBeforeStart++
			continue
		}

		size := info.Size()
		if size > browserArtifactPerFileCapBytes {
			summary.SkippedPerFileCap++
			continue
		}
		if summary.CopiedBytes+size > browserArtifactTotalCapBytes {
			summary.SkippedTotalCap++
			continue
		}

		dstPath := filepath.Join(destRoot, filepath.FromSlash(item.RelPath))
		if !pathWithin(dstPath, destRoot) {
			summary.SkippedOutsideSource++
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dstPath), 0o755); err != nil {
			summary.SkippedCopyError++
			continue
		}
		if err := copyBrowserArtifactFile(item.AbsPath, dstPath, info.Mode().Perm()); err != nil {
			summary.SkippedCopyError++
			continue
		}

		summary.CopiedFiles++
		summary.CopiedBytes += size
		summary.CopiedRelPaths = append(summary.CopiedRelPaths, item.RelPath)
	}

	slices.Sort(summary.CopiedRelPaths)
	return summary, nil
}

func copyBrowserArtifactFile(src, dst string, perm os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	defer func() { _ = out.Close() }()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
}

func browserArtifactCandidate(rel string) bool {
	rel = filepath.ToSlash(strings.TrimPrefix(strings.TrimSpace(rel), "./"))
	if rel == "" || rel == "." {
		return false
	}
	for _, sequence := range browserArtifactPathSegments {
		if containsPathSegmentSequence(rel, sequence) {
			return true
		}
	}
	base := path.Base(rel)
	for _, glob := range browserArtifactNameGlobs {
		if ok, _ := path.Match(glob, base); ok {
			return true
		}
	}
	return false
}

func containsPathSegmentSequence(rel, sequence string) bool {
	relParts := splitNonEmptyPathSegments(rel)
	sequenceParts := splitNonEmptyPathSegments(sequence)
	if len(relParts) == 0 || len(sequenceParts) == 0 || len(relParts) < len(sequenceParts) {
		return false
	}
	for start := 0; start <= len(relParts)-len(sequenceParts); start++ {
		if slices.Equal(relParts[start:start+len(sequenceParts)], sequenceParts) {
			return true
		}
	}
	return false
}

func splitNonEmptyPathSegments(in string) []string {
	parts := strings.Split(strings.Trim(filepath.ToSlash(in), "/"), "/")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if strings.TrimSpace(part) == "" || part == "." {
			continue
		}
		out = append(out, part)
	}
	return out
}

func shouldSkipBrowserWalkDir(rel string) bool {
	parts := strings.Split(filepath.ToSlash(rel), "/")
	for i, part := range parts {
		switch part {
		case ".git", "node_modules", ".pnpm-store", ".npm":
			return true
		}
		if part == ".cache" && i+1 < len(parts) && parts[i+1] == "ms-playwright" {
			return true
		}
		if part == "playwright" && i+1 < len(parts) && parts[i+1] == ".cache" {
			return true
		}
		if part == ".yarn" && i+1 < len(parts) && parts[i+1] == "cache" {
			return true
		}
	}
	return false
}

func isExcludedBrowserArtifactPath(rel string) bool {
	parts := strings.Split(filepath.ToSlash(rel), "/")
	for i, part := range parts {
		if part == "playwright" && i+1 < len(parts) && parts[i+1] == ".cache" {
			return true
		}
		if part == ".cache" && i+1 < len(parts) && parts[i+1] == "ms-playwright" {
			return true
		}
		if part == ".yarn" && i+1 < len(parts) && parts[i+1] == "cache" {
			return true
		}
	}
	return false
}
