package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/danshapiro/kilroy/internal/attractor/runtime"
)

type InputSourceTargetMapEntry struct {
	Source string `json:"source"`
	Target string `json:"target"`
}

type InputManifest struct {
	Sources                      []string                    `json:"sources"`
	ResolvedFiles                []string                    `json:"resolved_files"`
	SourceTargetMap              []InputSourceTargetMapEntry `json:"source_target_map"`
	RunBaseRevision              string                      `json:"run_base_revision,omitempty"`
	BranchRevision               string                      `json:"branch_revision,omitempty"`
	BaseRunRevision              string                      `json:"base_run_revision,omitempty"`
	BranchHeadRevision           string                      `json:"branch_head_revision,omitempty"`
	DiscoveredReferences         []DiscoveredInputReference  `json:"discovered_references"`
	UnresolvedInferredReferences []InferredReference         `json:"unresolved_inferred_references,omitempty"`
	Warnings                     []string                    `json:"warnings,omitempty"`
	GeneratedAt                  string                      `json:"generated_at"`
}

type InputMaterializationOptions struct {
	SourceRoots             []string
	Include                 []string
	DefaultInclude          []string
	FollowReferences        bool
	TargetRoot              string
	SnapshotRoot            string
	ExistingSourceTargetMap map[string]string
	Scanner                 InputReferenceScanner
	InferWithLLM            bool
	Inferer                 InputReferenceInferer
	InferProvider           string
	InferModel              string
	InferenceCache          map[string][]InferredReference
}

type InputMaterializationPolicy struct {
	Enabled          bool
	Include          []string
	DefaultInclude   []string
	FollowReferences bool
	InferWithLLM     bool
	InferProvider    string
	InferModel       string
}

const (
	inputManifestFileName       = "inputs_manifest.json"
	inputSnapshotDirName        = "input_snapshot"
	inputSnapshotFilesSubdir    = "files"
	inputInferenceCacheFileName = "inference_cache.json"
)

func inputMaterializationPolicyFromConfig(cfg *RunConfigFile) InputMaterializationPolicy {
	if cfg == nil {
		return InputMaterializationPolicy{}
	}
	m := cfg.Inputs.Materialize
	policy := InputMaterializationPolicy{
		Include:        append([]string{}, m.Include...),
		DefaultInclude: append([]string{}, m.DefaultInclude...),
		InferProvider:  strings.TrimSpace(m.LLMProvider),
		InferModel:     strings.TrimSpace(m.LLMModel),
	}
	if m.Enabled != nil {
		policy.Enabled = *m.Enabled
	}
	if m.FollowReferences != nil {
		policy.FollowReferences = *m.FollowReferences
	}
	if m.InferWithLLM != nil {
		policy.InferWithLLM = *m.InferWithLLM
	}
	return policy
}

type inputIncludeMissingError struct {
	Patterns []string
}

type inputReferenceQueueReason int

const (
	inputReferenceQueueReasonDefault inputReferenceQueueReason = iota
	inputReferenceQueueReasonDiscovered
	inputReferenceQueueReasonExplicit
)

func (e *inputIncludeMissingError) Error() string {
	if e == nil || len(e.Patterns) == 0 {
		return "input_include_missing"
	}
	return fmt.Sprintf("input_include_missing: unmatched include patterns: %s", strings.Join(e.Patterns, ", "))
}

func materializeInputClosure(ctx context.Context, opts InputMaterializationOptions) (*InputManifest, error) {
	roots, err := normalizeInputRoots(opts.SourceRoots)
	if err != nil {
		return nil, err
	}
	if len(roots) == 0 {
		return nil, fmt.Errorf("input materialization requires at least one source root")
	}
	if opts.Scanner == nil {
		opts.Scanner = deterministicInputReferenceScanner{}
	}

	requiredSeeds, missingIncludes, err := expandInputSeedPatterns(opts.Include, roots)
	if err != nil {
		return nil, err
	}
	defaultSeeds, _, err := expandInputSeedPatterns(opts.DefaultInclude, roots)
	if err != nil {
		return nil, err
	}
	if len(missingIncludes) > 0 {
		return nil, &inputIncludeMissingError{Patterns: missingIncludes}
	}

	resolved := map[string]bool{}
	queued := map[string]bool{}
	queueReasons := map[string]inputReferenceQueueReason{}
	queue := make([]string, 0, len(requiredSeeds)+len(defaultSeeds))
	push := func(reason inputReferenceQueueReason, paths ...string) {
		for _, p := range paths {
			abs, aerr := filepath.Abs(strings.TrimSpace(p))
			if aerr != nil {
				continue
			}
			if prev, ok := queueReasons[abs]; !ok || reason > prev {
				queueReasons[abs] = reason
			}
			if queued[abs] {
				continue
			}
			queued[abs] = true
			queue = append(queue, abs)
		}
	}
	push(inputReferenceQueueReasonExplicit, requiredSeeds...)
	push(inputReferenceQueueReasonDefault, defaultSeeds...)

	discovered := make([]DiscoveredInputReference, 0, 16)
	warnings := make([]string, 0, 4)
	unresolvedInferred := make([]InferredReference, 0, 4)

	for len(queue) > 0 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		current := queue[0]
		queue = queue[1:]
		if resolved[current] {
			continue
		}
		if !isRegularFile(current) {
			continue
		}
		// Skip VCS metadata paths (e.g. .git/**) so we never try to mkdir a
		// path that collides with the worktree .git pointer file.
		if isVCSMetaPath(current) {
			continue
		}
		resolved[current] = true
		reason := queueReasons[current]
		if !opts.FollowReferences || !shouldScanInputReferences(current, reason) {
			continue
		}

		content, readErr := os.ReadFile(current)
		if readErr != nil {
			warnings = append(warnings, fmt.Sprintf("read %s: %v", current, readErr))
			continue
		}
		refs := opts.Scanner.Scan(current, content)
		candidates := append([]DiscoveredInputReference{}, refs...)
		inferred, inferWarnings := inferReferencesForDocument(ctx, opts, current, content)
		if len(inferWarnings) > 0 {
			warnings = append(warnings, inferWarnings...)
		}
		for _, inferredRef := range inferred {
			candidates = append(candidates, DiscoveredInputReference{
				SourceFile: current,
				Matched:    inferredRef.Pattern,
				Pattern:    inferredRef.Pattern,
				Kind:       classifyReferenceKind(inferredRef.Pattern),
				Confidence: "inferred",
			})
		}
		candidates = dedupeDiscoveredReferences(candidates)
		discovered = append(discovered, candidates...)
		for _, ref := range candidates {
			matches, matchErr := resolveInputReferenceCandidate(ref, current, roots, opts.ExistingSourceTargetMap)
			if matchErr != nil {
				warnings = append(warnings, matchErr.Error())
				continue
			}
			if len(matches) == 0 && ref.Confidence == "inferred" {
				unresolvedInferred = append(unresolvedInferred, InferredReference{
					Pattern:    ref.Pattern,
					Rationale:  "",
					Confidence: "inferred",
				})
			}
			push(inputReferenceReasonFromParent(reason), matches...)
		}
	}

	targetRoot := strings.TrimSpace(opts.TargetRoot)
	snapshotRoot := strings.TrimSpace(opts.SnapshotRoot)
	resolvedTargets := map[string]bool{}
	sourceToTarget := map[string]string{}

	resolvedSources := sortedStringSet(resolved)

	// Build source→target maps. When multiple sources resolve to the same
	// target path, prefer sources already inside targetRoot (live worktree
	// state written by prior nodes) over external sources such as the
	// run-startup snapshot. Without this, the snapshot—which sorts
	// alphabetically before the worktree path—would silently overwrite files
	// that a prior node has modified.
	targetToCopySource := map[string]string{}
	for _, source := range resolvedSources {
		targetRel := mapInputSourceToTargetPath(source, roots)
		sourceToTarget[source] = targetRel
		resolvedTargets[targetRel] = true
		if targetRoot == "" {
			continue
		}
		srcAbs, err := filepath.Abs(source)
		if err != nil {
			srcAbs = source
		}
		inTarget := strings.HasPrefix(srcAbs, targetRoot+string(filepath.Separator))
		if _, exists := targetToCopySource[targetRel]; !exists || inTarget {
			targetToCopySource[targetRel] = source
		}
	}

	snapshotWritten := map[string]bool{}
	for _, source := range resolvedSources {
		targetRel := sourceToTarget[source]
		if targetRoot != "" && targetToCopySource[targetRel] == source {
			if err := copyInputFile(source, filepath.Join(targetRoot, filepath.FromSlash(targetRel))); err != nil {
				return nil, err
			}
		}
		if snapshotRoot != "" && !snapshotWritten[targetRel] {
			snapshotWritten[targetRel] = true
			if err := copyInputFile(source, filepath.Join(snapshotRoot, filepath.FromSlash(targetRel))); err != nil {
				return nil, err
			}
		}
	}

	manifest := &InputManifest{
		Sources:                      append([]string{}, roots...),
		ResolvedFiles:                sortedStringSet(resolvedTargets),
		SourceTargetMap:              sortedSourceTargetMap(sourceToTarget),
		DiscoveredReferences:         sortDiscoveredReferences(discovered),
		UnresolvedInferredReferences: normalizeInferredReferences(unresolvedInferred),
		Warnings:                     dedupeAndSortStrings(warnings),
		GeneratedAt:                  time.Now().UTC().Format(time.RFC3339Nano),
	}
	return manifest, nil
}

func resolveInputReferenceCandidate(ref DiscoveredInputReference, sourceFile string, roots []string, existingMap map[string]string) ([]string, error) {
	pattern := strings.TrimSpace(ref.Pattern)
	if pattern == "" {
		return nil, nil
	}
	if ref.Kind == InputReferenceKindGlob {
		return resolveGlobReference(pattern, sourceFile, roots, existingMap)
	}
	return resolvePathReference(pattern, sourceFile, roots, existingMap), nil
}

func resolvePathReference(pattern string, sourceFile string, roots []string, existingMap map[string]string) []string {
	candidates := make([]string, 0, len(roots)+2)
	if isAbsolutePathLike(pattern) {
		candidates = append(candidates, pattern)
		if rel, ok := lookupSourceTargetMapping(existingMap, pattern); ok {
			for _, root := range roots {
				candidates = append(candidates, filepath.Join(root, filepath.FromSlash(rel)))
			}
		}
	} else {
		base := filepath.Dir(sourceFile)
		candidates = append(candidates, filepath.Join(base, filepath.FromSlash(pattern)))
		for _, root := range roots {
			candidates = append(candidates, filepath.Join(root, filepath.FromSlash(pattern)))
		}
	}
	return existingRegularFiles(candidates)
}

func resolveGlobReference(pattern string, sourceFile string, roots []string, existingMap map[string]string) ([]string, error) {
	globPatterns := make([]string, 0, len(roots)+2)
	if isAbsolutePathLike(pattern) {
		globPatterns = append(globPatterns, pattern)
		if rel, ok := lookupSourceTargetMapping(existingMap, pattern); ok {
			for _, root := range roots {
				globPatterns = append(globPatterns, filepath.Join(root, filepath.FromSlash(rel)))
			}
		}
	} else {
		base := filepath.Dir(sourceFile)
		globPatterns = append(globPatterns, filepath.Join(base, filepath.FromSlash(pattern)))
		for _, root := range roots {
			globPatterns = append(globPatterns, filepath.Join(root, filepath.FromSlash(pattern)))
		}
	}
	matches := map[string]bool{}
	for _, raw := range globPatterns {
		glob := filepath.FromSlash(raw)
		hits, err := doublestar.FilepathGlob(glob)
		if err != nil {
			return nil, fmt.Errorf("expand input glob %q: %w", pattern, err)
		}
		for _, hit := range hits {
			if isRegularFile(hit) {
				abs, err := filepath.Abs(hit)
				if err == nil {
					matches[abs] = true
				}
			}
		}
	}
	return sortedStringSet(matches), nil
}

func expandInputSeedPatterns(patterns []string, roots []string) ([]string, []string, error) {
	files := map[string]bool{}
	missing := make([]string, 0)
	for _, raw := range patterns {
		pattern := strings.TrimSpace(raw)
		if pattern == "" {
			continue
		}
		matches, err := expandSeedPattern(pattern, roots)
		if err != nil {
			return nil, nil, err
		}
		if len(matches) == 0 {
			missing = append(missing, pattern)
			continue
		}
		for _, m := range matches {
			files[m] = true
		}
	}
	return sortedStringSet(files), dedupeAndSortStrings(missing), nil
}

func expandSeedPattern(pattern string, roots []string) ([]string, error) {
	matches := map[string]bool{}
	if isAbsolutePathLike(pattern) {
		if containsGlobMeta(pattern) {
			hits, err := doublestar.FilepathGlob(filepath.FromSlash(pattern))
			if err != nil {
				return nil, fmt.Errorf("expand input include %q: %w", pattern, err)
			}
			for _, hit := range hits {
				if isRegularFile(hit) {
					abs, aerr := filepath.Abs(hit)
					if aerr == nil {
						matches[abs] = true
					}
				}
			}
			return sortedStringSet(matches), nil
		}
		if isRegularFile(pattern) {
			abs, err := filepath.Abs(pattern)
			if err == nil {
				matches[abs] = true
			}
		}
		return sortedStringSet(matches), nil
	}

	for _, root := range roots {
		if containsGlobMeta(pattern) {
			hits, err := doublestar.FilepathGlob(filepath.Join(root, filepath.FromSlash(pattern)))
			if err != nil {
				return nil, fmt.Errorf("expand input include %q: %w", pattern, err)
			}
			for _, hit := range hits {
				if isRegularFile(hit) {
					abs, aerr := filepath.Abs(hit)
					if aerr == nil {
						matches[abs] = true
					}
				}
			}
			continue
		}
		candidate := filepath.Join(root, filepath.FromSlash(pattern))
		if isRegularFile(candidate) {
			abs, err := filepath.Abs(candidate)
			if err == nil {
				matches[abs] = true
			}
		}
	}
	return sortedStringSet(matches), nil
}

func mapInputSourceToTargetPath(source string, roots []string) string {
	sourceAbs, err := filepath.Abs(source)
	if err == nil {
		source = sourceAbs
	}
	bestRoot := ""
	bestRel := ""
	for _, root := range roots {
		rel, err := filepath.Rel(root, source)
		if err != nil {
			continue
		}
		rel = filepath.Clean(rel)
		if rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." {
			continue
		}
		if len(root) > len(bestRoot) {
			bestRoot = root
			bestRel = rel
		}
	}
	if bestRoot != "" {
		return filepath.ToSlash(bestRel)
	}

	sum := sha256.Sum256([]byte(source))
	prefix := hex.EncodeToString(sum[:])[:12]
	sanitized := filepath.ToSlash(source)
	sanitized = strings.TrimPrefix(sanitized, "/")
	sanitized = strings.ReplaceAll(sanitized, ":", "_")
	return filepath.ToSlash(filepath.Join(".kilroy-inputs", "external", prefix, sanitized))
}

func copyInputFile(source string, target string) error {
	if !isRegularFile(source) {
		return fmt.Errorf("input source is not a regular file: %s", source)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	src, err := os.Open(source)
	if err != nil {
		return err
	}
	defer func() { _ = src.Close() }()
	info, err := src.Stat()
	if err != nil {
		return err
	}
	// Guard against self-copy: if target already exists and refers to the same
	// inode as source, opening with O_TRUNC would destroy the source content
	// before any bytes are read. Skip the copy — the file is already in place.
	if dstInfo, err := os.Stat(target); err == nil && os.SameFile(info, dstInfo) {
		return nil
	}
	dst, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode().Perm())
	if err != nil {
		return err
	}
	defer func() { _ = dst.Close() }()
	if _, err := io.Copy(dst, src); err != nil {
		return err
	}
	return nil
}

func inferReferencesForDocument(ctx context.Context, opts InputMaterializationOptions, sourceFile string, content []byte) ([]InferredReference, []string) {
	if !opts.InferWithLLM || opts.Inferer == nil {
		return nil, nil
	}
	if strings.TrimSpace(opts.InferProvider) == "" || strings.TrimSpace(opts.InferModel) == "" {
		return nil, []string{"input inference skipped: missing provider/model override"}
	}
	if len(content) == 0 {
		return nil, nil
	}

	hash := sha256.Sum256(content)
	cacheKey := hex.EncodeToString(hash[:])
	if opts.InferenceCache != nil {
		if cached, ok := opts.InferenceCache[cacheKey]; ok {
			return normalizeInferredReferences(cached), nil
		}
	}

	refs, err := opts.Inferer.Infer(ctx, []InputDocForInference{{
		Path:    sourceFile,
		Content: string(content),
	}}, InputInferenceOptions{
		Provider: strings.TrimSpace(opts.InferProvider),
		Model:    strings.TrimSpace(opts.InferModel),
	})
	if err != nil {
		return nil, []string{fmt.Sprintf("input inference failed for %s: %v", sourceFile, err)}
	}
	refs = normalizeInferredReferences(refs)
	if opts.InferenceCache != nil {
		opts.InferenceCache[cacheKey] = refs
	}
	return refs, nil
}

func dedupeDiscoveredReferences(in []DiscoveredInputReference) []DiscoveredInputReference {
	if len(in) == 0 {
		return nil
	}
	out := make([]DiscoveredInputReference, 0, len(in))
	seen := map[string]bool{}
	for _, ref := range in {
		pattern := strings.TrimSpace(ref.Pattern)
		if pattern == "" {
			continue
		}
		key := strings.ToLower(pattern) + "|" + ref.SourceFile + "|" + string(ref.Kind) + "|" + strings.TrimSpace(ref.Confidence)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, ref)
	}
	return sortDiscoveredReferences(out)
}

func normalizeInputRoots(roots []string) ([]string, error) {
	out := make([]string, 0, len(roots))
	seen := map[string]bool{}
	for _, raw := range roots {
		root := strings.TrimSpace(raw)
		if root == "" {
			continue
		}
		abs, err := filepath.Abs(root)
		if err != nil {
			return nil, err
		}
		if seen[abs] {
			continue
		}
		seen[abs] = true
		out = append(out, abs)
	}
	sort.Strings(out)
	return out, nil
}

func sortedStringSet(set map[string]bool) []string {
	if len(set) == 0 {
		return nil
	}
	out := make([]string, 0, len(set))
	for key, ok := range set {
		if ok {
			out = append(out, key)
		}
	}
	sort.Strings(out)
	return out
}

func sortDiscoveredReferences(in []DiscoveredInputReference) []DiscoveredInputReference {
	if len(in) == 0 {
		return nil
	}
	out := append([]DiscoveredInputReference{}, in...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].SourceFile != out[j].SourceFile {
			return out[i].SourceFile < out[j].SourceFile
		}
		if out[i].Pattern != out[j].Pattern {
			return out[i].Pattern < out[j].Pattern
		}
		return out[i].Matched < out[j].Matched
	})
	return out
}

func sortedSourceTargetMap(mapping map[string]string) []InputSourceTargetMapEntry {
	if len(mapping) == 0 {
		return nil
	}
	keys := make([]string, 0, len(mapping))
	for source := range mapping {
		keys = append(keys, source)
	}
	sort.Strings(keys)
	out := make([]InputSourceTargetMapEntry, 0, len(keys))
	for _, source := range keys {
		out = append(out, InputSourceTargetMapEntry{Source: source, Target: mapping[source]})
	}
	return out
}

func dedupeAndSortStrings(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	set := map[string]bool{}
	for _, raw := range in {
		s := strings.TrimSpace(raw)
		if s != "" {
			set[s] = true
		}
	}
	return sortedStringSet(set)
}

func existingRegularFiles(candidates []string) []string {
	set := map[string]bool{}
	for _, candidate := range candidates {
		if !isRegularFile(candidate) {
			continue
		}
		abs, err := filepath.Abs(candidate)
		if err != nil {
			continue
		}
		set[abs] = true
	}
	return sortedStringSet(set)
}

func lookupSourceTargetMapping(mapping map[string]string, absolutePath string) (string, bool) {
	if len(mapping) == 0 {
		return "", false
	}
	if rel, ok := mapping[absolutePath]; ok {
		return rel, true
	}
	if rel, ok := mapping[filepath.Clean(absolutePath)]; ok {
		return rel, true
	}
	normalized := strings.ReplaceAll(filepath.Clean(absolutePath), "\\", "/")
	for source, rel := range mapping {
		s := strings.ReplaceAll(filepath.Clean(source), "\\", "/")
		if s == normalized {
			return rel, true
		}
	}
	return "", false
}

func isRegularFile(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return info.Mode().IsRegular()
}

func containsGlobMeta(pattern string) bool {
	return strings.ContainsAny(pattern, "*?[")
}

func isAbsolutePathLike(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	if filepath.IsAbs(path) {
		return true
	}
	return windowsAbsPathRE.MatchString(path)
}

func inputReferenceReasonFromParent(parent inputReferenceQueueReason) inputReferenceQueueReason {
	if parent == inputReferenceQueueReasonExplicit {
		return inputReferenceQueueReasonExplicit
	}
	return inputReferenceQueueReasonDiscovered
}

func shouldScanInputReferences(path string, reason inputReferenceQueueReason) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	if reason == inputReferenceQueueReasonExplicit {
		return true
	}
	if !isReferenceDocumentPath(path) {
		return false
	}
	if isLikelyArtifactInputPath(path) {
		return false
	}
	return true
}

func isReferenceDocumentPath(path string) bool {
	base := strings.ToLower(strings.TrimSpace(filepath.Base(path)))
	ext := strings.ToLower(strings.TrimSpace(filepath.Ext(base)))
	switch ext {
	case ".md", ".markdown", ".mdx", ".txt", ".rst", ".adoc", ".asciidoc", ".dot", ".yaml", ".yml", ".toml":
		return true
	}
	if ext != "" {
		return false
	}
	switch base {
	case "readme", "license", "copying", "notice", "dod", "spec", "requirements", "tests", "testplan":
		return true
	default:
		return false
	}
}

// isVCSMetaPath reports whether path contains a VCS metadata directory segment
// (.git or .jj). Such files must never be copied into a worktree because the
// worktree already has a .git pointer *file* and MkdirAll would conflict with it.
func isVCSMetaPath(path string) bool {
	normalized := strings.ToLower(strings.ReplaceAll(filepath.Clean(path), "\\", "/"))
	for _, seg := range strings.Split(normalized, "/") {
		if seg == ".git" || seg == ".jj" {
			return true
		}
	}
	return false
}

func isLikelyArtifactInputPath(path string) bool {
	normalized := strings.ToLower(strings.ReplaceAll(filepath.Clean(path), "\\", "/"))
	if normalized == "" {
		return false
	}
	segments := strings.Split(normalized, "/")
	for _, seg := range segments {
		switch seg {
		case ".git", ".jj", "logs", "benchmarks", "worktree", "node_modules", ".pnpm-store", ".venv", "venv", "__pycache__", ".pytest_cache", "dist-info", "managed":
			return true
		}
	}
	return false
}

func inputRunManifestPath(logsRoot string) string {
	return filepath.Join(strings.TrimSpace(logsRoot), inputManifestFileName)
}

func inputStageManifestPath(logsRoot string, nodeID string) string {
	return filepath.Join(strings.TrimSpace(logsRoot), strings.TrimSpace(nodeID), inputManifestFileName)
}

func inputSnapshotFilesRoot(logsRoot string) string {
	return filepath.Join(strings.TrimSpace(logsRoot), inputSnapshotDirName, inputSnapshotFilesSubdir)
}

func inputInferenceCachePath(logsRoot string) string {
	return filepath.Join(strings.TrimSpace(logsRoot), inputSnapshotDirName, inputInferenceCacheFileName)
}

func (e *Engine) inputMaterializationEnabled() bool {
	if e == nil {
		return false
	}
	return e.InputMaterializationPolicy.Enabled
}

func (e *Engine) materializeRunStartupInputs(ctx context.Context) error {
	if !e.inputMaterializationEnabled() {
		return nil
	}
	if err := e.ensureLineageLoaded(); err != nil {
		return err
	}
	manifest, err := e.materializeInputsWithPolicy(ctx, inputMaterializationRunScope, "", []string{
		e.Options.RepoPath,
	}, e.WorktreeDir, inputSnapshotFilesRoot(e.LogsRoot), inputRunManifestPath(e.LogsRoot), true)
	if err != nil {
		return err
	}
	if err := e.ensureRunStartupLineage(manifest); err != nil {
		return err
	}
	if err := persistRevisionSnapshot(e.LogsRoot, e.inputLineage.RunHead, e.WorktreeDir, e.Options.RunID); err != nil {
		return err
	}
	return e.inputLineage.SaveAtomic(e.LogsRoot)
}

func (e *Engine) materializeStageInputs(ctx context.Context, nodeID string) error {
	e.currentInputManifestPath = ""
	if !e.inputMaterializationEnabled() {
		return nil
	}
	// Stage materialization runs immediately before handler execution and updates
	// the stage-local manifest path consumed by buildStageRuntimeEnv.
	roots := []string{e.WorktreeDir}
	snapshot := inputSnapshotFilesRoot(e.LogsRoot)
	if strings.TrimSpace(snapshot) != "" {
		roots = append(roots, snapshot)
	}
	manifestPath := inputStageManifestPath(e.LogsRoot, nodeID)
	manifest, err := e.materializeInputsWithPolicy(ctx, inputMaterializationStageScope, nodeID, roots, e.WorktreeDir, "", manifestPath, true)
	if err != nil {
		return err
	}
	if manifest != nil {
		e.attachStageLineageTuple(manifest)
		if err := writeJSON(manifestPath, manifest); err != nil {
			return err
		}
		// Keep the run-level manifest in sync with the latest closure expansion.
		runManifest := *manifest
		e.attachRunManifestLineage(&runManifest)
		_ = writeJSON(inputRunManifestPath(e.LogsRoot), &runManifest)
		e.currentInputManifestPath = manifestPath
	}
	return nil
}

func (e *Engine) materializeBranchStartupInputs(ctx context.Context, parentWorktree string, parentLogsRoot string) error {
	if !e.inputMaterializationEnabled() {
		return nil
	}
	parentLineage, err := LoadInputSnapshotLineage(parentLogsRoot)
	if err != nil {
		return err
	}
	e.inputLineage = parentLineage
	baseRev := strings.TrimSpace(parentLineage.RunHead)
	if baseRev == "" {
		return fmt.Errorf("branch startup missing parent run head revision")
	}
	e.activeBranchKey = branchKeyFromWorktree(e.WorktreeDir)
	e.activeRunBaseRevision = baseRev
	branchRev, err := e.inputLineage.ForkBranch(e.activeBranchKey, baseRev)
	if err != nil {
		return err
	}
	e.activeBranchRevision = branchRev
	if err := hydrateRunScopedRevision(e.WorktreeDir, e.Options.RunID, revisionHydrationRoot(parentLogsRoot, e.inputLineage, branchRev)); err != nil {
		return err
	}
	roots := []string{parentWorktree}
	if snapshot := strings.TrimSpace(inputSnapshotFilesRoot(parentLogsRoot)); snapshot != "" {
		roots = append(roots, snapshot)
	}
	manifest, err := e.materializeInputsWithPolicy(ctx, inputMaterializationBranchScope, "", roots, e.WorktreeDir, "", inputRunManifestPath(e.LogsRoot), true)
	if err != nil {
		return err
	}
	if manifest != nil {
		manifest.BaseRunRevision = baseRev
		manifest.BranchHeadRevision = branchRev
		if err := writeJSON(inputRunManifestPath(e.LogsRoot), manifest); err != nil {
			return err
		}
	}
	if err := persistRevisionSnapshot(e.LogsRoot, branchRev, e.WorktreeDir, e.Options.RunID); err != nil {
		return err
	}
	return e.inputLineage.SaveAtomic(e.LogsRoot)
}

func (e *Engine) materializeResumeStartupInputs(ctx context.Context) error {
	if !e.inputMaterializationEnabled() {
		return nil
	}
	if err := e.ensureLineageLoaded(); err != nil {
		return err
	}
	if runHead := strings.TrimSpace(e.inputLineage.RunHead); runHead != "" {
		if err := hydrateRunScopedRevision(e.WorktreeDir, e.Options.RunID, revisionHydrationRoot(e.LogsRoot, e.inputLineage, runHead)); err != nil {
			return err
		}
	}
	e.activeBranchKey = ""
	e.activeRunBaseRevision = ""
	e.activeBranchRevision = ""
	roots := []string{e.WorktreeDir}
	if snapshot := strings.TrimSpace(inputSnapshotFilesRoot(e.LogsRoot)); snapshot != "" {
		roots = append(roots, snapshot)
	}
	manifest, err := e.materializeInputsWithPolicy(ctx, inputMaterializationResumeScope, "", roots, e.WorktreeDir, "", inputRunManifestPath(e.LogsRoot), true)
	if err != nil {
		return err
	}
	if manifest != nil {
		e.attachRunManifestLineage(manifest)
		if err := writeJSON(inputRunManifestPath(e.LogsRoot), manifest); err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) ensureLineageLoaded() error {
	if e == nil {
		return fmt.Errorf("nil engine")
	}
	if e.inputLineage != nil {
		return nil
	}
	lineage, err := LoadInputSnapshotLineage(e.LogsRoot)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if lineage == nil {
		lineage = newInputSnapshotLineage(e.Options.RunID)
	}
	if strings.TrimSpace(lineage.RunID) == "" {
		lineage.RunID = strings.TrimSpace(e.Options.RunID)
	}
	e.inputLineage = lineage
	return nil
}

func (e *Engine) ensureRunStartupLineage(manifest *InputManifest) error {
	if e == nil {
		return fmt.Errorf("nil engine")
	}
	if err := e.ensureLineageLoaded(); err != nil {
		return err
	}
	if strings.TrimSpace(e.inputLineage.RunHead) == "" {
		digest, err := digestRunScopedState(e.WorktreeDir, e.Options.RunID)
		if err != nil {
			return err
		}
		next := e.inputLineage.CreateRunRevision("", digest)
		if strings.TrimSpace(next) == "" {
			return fmt.Errorf("create run startup lineage revision")
		}
		e.inputLineage.RunHead = next
	}
	e.activeBranchKey = ""
	e.activeRunBaseRevision = ""
	e.activeBranchRevision = ""
	e.attachRunManifestLineage(manifest)
	if manifest != nil {
		if err := writeJSON(inputRunManifestPath(e.LogsRoot), manifest); err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) attachStageLineageTuple(manifest *InputManifest) {
	if manifest == nil {
		return
	}
	if strings.TrimSpace(e.activeBranchKey) != "" {
		manifest.RunBaseRevision = strings.TrimSpace(e.activeRunBaseRevision)
		manifest.BranchRevision = strings.TrimSpace(e.activeBranchRevision)
		return
	}
	if e.inputLineage == nil {
		return
	}
	manifest.RunBaseRevision = strings.TrimSpace(e.inputLineage.RunHead)
	manifest.BranchRevision = ""
}

func (e *Engine) attachRunManifestLineage(manifest *InputManifest) {
	if manifest == nil {
		return
	}
	if strings.TrimSpace(e.activeBranchKey) != "" {
		manifest.BaseRunRevision = strings.TrimSpace(e.activeRunBaseRevision)
		manifest.BranchHeadRevision = strings.TrimSpace(e.activeBranchRevision)
		return
	}
	if e.inputLineage == nil {
		return
	}
	manifest.BaseRunRevision = strings.TrimSpace(e.inputLineage.RunHead)
	manifest.BranchHeadRevision = ""
}

func (e *Engine) advanceLineageAfterStage(nodeID string) error {
	if !e.inputMaterializationEnabled() {
		return nil
	}
	if err := e.ensureLineageLoaded(); err != nil {
		return err
	}
	digest, err := digestRunScopedState(e.WorktreeDir, e.Options.RunID)
	if err != nil {
		return fmt.Errorf("digest run-scoped state for %s: %w", strings.TrimSpace(nodeID), err)
	}
	var nextRev string
	if strings.TrimSpace(e.activeBranchKey) != "" {
		nextRev = e.inputLineage.AdvanceBranch(e.activeBranchKey, e.activeBranchRevision, digest)
		if strings.TrimSpace(nextRev) == "" {
			return fmt.Errorf("advance branch lineage for %s", strings.TrimSpace(nodeID))
		}
		e.activeBranchRevision = nextRev
	} else {
		nextRev = e.inputLineage.CreateRunRevision(e.inputLineage.RunHead, digest)
		if strings.TrimSpace(nextRev) == "" {
			return fmt.Errorf("advance run lineage for %s", strings.TrimSpace(nodeID))
		}
		e.inputLineage.RunHead = nextRev
	}
	if err := persistRevisionSnapshot(e.LogsRoot, nextRev, e.WorktreeDir, e.Options.RunID); err != nil {
		return err
	}
	if err := e.inputLineage.SaveAtomic(e.LogsRoot); err != nil {
		return err
	}
	runManifestPath := inputRunManifestPath(e.LogsRoot)
	manifest, err := loadInputManifest(runManifestPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	e.attachRunManifestLineage(manifest)
	return writeJSON(runManifestPath, manifest)
}

func digestRunScopedState(worktreeDir string, runID string) (map[string]string, error) {
	root := runScopedWorktreeRoot(worktreeDir, runID)
	digest := map[string]string{}
	info, err := os.Stat(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return digest, nil
		}
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("run-scoped root is not a directory: %s", root)
	}
	if err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(b)
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		digest[filepath.ToSlash(rel)] = "sha256:" + hex.EncodeToString(sum[:])
		return nil
	}); err != nil {
		return nil, err
	}
	return digest, nil
}

func persistRevisionSnapshot(logsRoot, revID, worktreeDir, runID string) error {
	revID = strings.TrimSpace(revID)
	if revID == "" {
		return fmt.Errorf("revision id is required")
	}
	src := runScopedWorktreeRoot(worktreeDir, runID)
	dst := inputRevisionRoot(logsRoot, revID)
	_ = os.RemoveAll(dst)
	if _, err := os.Stat(src); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return os.MkdirAll(dst, 0o755)
		}
		return err
	}
	return copyTree(src, dst)
}

func hydrateRunScopedRevision(worktreeDir string, runID string, revisionRoot string) error {
	target := runScopedWorktreeRoot(worktreeDir, runID)
	_ = os.RemoveAll(target)
	revisionRoot = strings.TrimSpace(revisionRoot)
	if revisionRoot == "" {
		return os.MkdirAll(target, 0o755)
	}
	info, err := os.Stat(revisionRoot)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return os.MkdirAll(target, 0o755)
		}
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("revision snapshot root is not a directory: %s", revisionRoot)
	}
	return copyTree(revisionRoot, target)
}

func copyTree(src string, dst string) error {
	src = strings.TrimSpace(src)
	dst = strings.TrimSpace(dst)
	if src == "" || dst == "" {
		return fmt.Errorf("copy tree requires source and destination")
	}
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := dst
		if rel != "." {
			target = filepath.Join(dst, rel)
		}
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		return copyInputFile(path, target)
	})
}

func runScopedWorktreeRoot(worktreeDir string, runID string) string {
	return filepath.Join(strings.TrimSpace(worktreeDir), ".ai", "runs", strings.TrimSpace(runID))
}

func branchKeyFromWorktree(worktreeDir string) string {
	key := strings.TrimSpace(filepath.Base(filepath.Clean(worktreeDir)))
	if key == "" || key == "." || key == string(filepath.Separator) {
		return "branch"
	}
	return strings.ReplaceAll(key, " ", "_")
}

func revisionHydrationRoot(logsRoot string, lineage *InputSnapshotLineage, revID string) string {
	revID = strings.TrimSpace(revID)
	if revID == "" {
		return ""
	}
	seen := map[string]bool{}
	var find func(string) string
	find = func(current string) string {
		current = strings.TrimSpace(current)
		if current == "" || seen[current] {
			return ""
		}
		seen[current] = true
		root := inputRevisionRoot(logsRoot, current)
		if info, err := os.Stat(root); err == nil && info.IsDir() {
			return root
		}
		if lineage == nil {
			return ""
		}
		rev, ok := lineage.Revisions[current]
		if !ok {
			return ""
		}
		for _, parent := range rev.ParentIDs {
			if root := find(parent); root != "" {
				return root
			}
		}
		return ""
	}
	if root := find(revID); root != "" {
		return root
	}
	return inputRevisionRoot(logsRoot, revID)
}

func (e *Engine) ensureInputMaterializationStateLoaded() {
	if e == nil {
		return
	}
	if e.InputInferenceCache == nil {
		e.InputInferenceCache = loadInputInferenceCache(inputInferenceCachePath(e.LogsRoot))
	}
	if e.InputSourceTargetMap == nil {
		e.InputSourceTargetMap = map[string]string{}
		if m, err := loadInputManifest(inputRunManifestPath(e.LogsRoot)); err == nil && m != nil {
			e.InputSourceTargetMap = sourceTargetMapFromManifest(m)
		}
	}
}

func (e *Engine) persistInputInferenceCache() {
	if e == nil || e.InputInferenceCache == nil {
		return
	}
	cachePath := inputInferenceCachePath(e.LogsRoot)
	if err := os.MkdirAll(filepath.Dir(cachePath), 0o755); err != nil {
		return
	}
	_ = writeJSON(cachePath, e.InputInferenceCache)
}

func (e *Engine) materializeInputsWithPolicy(
	ctx context.Context,
	scope inputMaterializationScope,
	nodeID string,
	sourceRoots []string,
	targetRoot string,
	snapshotRoot string,
	manifestPath string,
	failOnIncludeMissing bool,
) (*InputManifest, error) {
	if !e.inputMaterializationEnabled() {
		return nil, nil
	}
	e.ensureInputMaterializationStateLoaded()

	e.emitInputMaterializationStarted(scope, nodeID, sourceRoots, targetRoot)
	manifest, err := materializeInputClosure(ctx, InputMaterializationOptions{
		SourceRoots:             sourceRoots,
		Include:                 append([]string{}, e.InputMaterializationPolicy.Include...),
		DefaultInclude:          append([]string{}, e.InputMaterializationPolicy.DefaultInclude...),
		FollowReferences:        e.InputMaterializationPolicy.FollowReferences,
		TargetRoot:              targetRoot,
		SnapshotRoot:            snapshotRoot,
		ExistingSourceTargetMap: copyStringStringMap(e.InputSourceTargetMap),
		Scanner:                 deterministicInputReferenceScanner{},
		InferWithLLM:            e.InputMaterializationPolicy.InferWithLLM,
		Inferer:                 e.InputReferenceInferer,
		InferProvider:           e.InputMaterializationPolicy.InferProvider,
		InferModel:              e.InputMaterializationPolicy.InferModel,
		InferenceCache:          e.InputInferenceCache,
	})
	if err != nil {
		if includeErr, ok := err.(*inputIncludeMissingError); ok {
			e.emitInputMaterializationError(scope, nodeID, "input_include_missing", includeErr.Patterns, includeErr.Error())
			if failOnIncludeMissing {
				return nil, includeErr
			}
			e.emitInputMaterializationWarning(scope, nodeID, includeErr.Error())
			return nil, nil
		}
		e.emitInputMaterializationError(scope, nodeID, "input_materialization_error", nil, err.Error())
		return nil, err
	}

	if manifest == nil {
		e.emitInputMaterializationCompleted(scope, nodeID, 0, 0)
		return nil, nil
	}
	if strings.TrimSpace(manifestPath) != "" {
		if err := os.MkdirAll(filepath.Dir(manifestPath), 0o755); err != nil {
			e.emitInputMaterializationError(scope, nodeID, "input_materialization_error", nil, err.Error())
			return nil, err
		}
		if err := writeJSON(manifestPath, manifest); err != nil {
			e.emitInputMaterializationError(scope, nodeID, "input_materialization_error", nil, err.Error())
			return nil, err
		}
	}

	e.InputSourceTargetMap = sourceTargetMapFromManifest(manifest)
	e.persistInputInferenceCache()
	for _, warning := range manifest.Warnings {
		e.emitInputMaterializationWarning(scope, nodeID, warning)
	}
	e.emitInputMaterializationProgress(scope, nodeID, len(manifest.ResolvedFiles), len(manifest.DiscoveredReferences))
	e.emitInputMaterializationCompleted(scope, nodeID, len(manifest.ResolvedFiles), len(manifest.UnresolvedInferredReferences))
	return manifest, nil
}

type inputMaterializationScope string

const (
	inputMaterializationRunScope    inputMaterializationScope = "run_startup"
	inputMaterializationStageScope  inputMaterializationScope = "stage"
	inputMaterializationBranchScope inputMaterializationScope = "branch"
	inputMaterializationResumeScope inputMaterializationScope = "resume"
)

func (e *Engine) emitInputMaterializationStarted(scope inputMaterializationScope, nodeID string, sourceRoots []string, targetRoot string) {
	if e == nil {
		return
	}
	event := map[string]any{
		"event":        "input_materialization_started",
		"scope":        string(scope),
		"source_roots": append([]string{}, sourceRoots...),
		"target_root":  strings.TrimSpace(targetRoot),
	}
	if strings.TrimSpace(nodeID) != "" {
		event["node_id"] = strings.TrimSpace(nodeID)
	}
	e.appendProgress(event)
}

func (e *Engine) emitInputMaterializationProgress(scope inputMaterializationScope, nodeID string, resolved int, discovered int) {
	if e == nil {
		return
	}
	event := map[string]any{
		"event":               "input_materialization_progress",
		"scope":               string(scope),
		"resolved_file_count": resolved,
		"discovered_refs":     discovered,
	}
	if strings.TrimSpace(nodeID) != "" {
		event["node_id"] = strings.TrimSpace(nodeID)
	}
	e.appendProgress(event)
}

func (e *Engine) emitInputMaterializationCompleted(scope inputMaterializationScope, nodeID string, resolved int, unresolvedInferred int) {
	if e == nil {
		return
	}
	event := map[string]any{
		"event":                   "input_materialization_completed",
		"scope":                   string(scope),
		"resolved_file_count":     resolved,
		"unresolved_inferred_ref": unresolvedInferred,
	}
	if strings.TrimSpace(nodeID) != "" {
		event["node_id"] = strings.TrimSpace(nodeID)
	}
	e.appendProgress(event)
}

func (e *Engine) emitInputMaterializationWarning(scope inputMaterializationScope, nodeID string, warning string) {
	if e == nil || strings.TrimSpace(warning) == "" {
		return
	}
	event := map[string]any{
		"event":   "input_materialization_warning",
		"scope":   string(scope),
		"warning": strings.TrimSpace(warning),
	}
	if strings.TrimSpace(nodeID) != "" {
		event["node_id"] = strings.TrimSpace(nodeID)
	}
	e.appendProgress(event)
}

func (e *Engine) emitInputMaterializationError(scope inputMaterializationScope, nodeID string, reason string, unmatched []string, message string) {
	if e == nil {
		return
	}
	event := map[string]any{
		"event":          "input_materialization_error",
		"scope":          string(scope),
		"failure_reason": strings.TrimSpace(reason),
		"message":        strings.TrimSpace(message),
	}
	if len(unmatched) > 0 {
		event["unmatched_include_patterns"] = append([]string{}, unmatched...)
	}
	if strings.TrimSpace(nodeID) != "" {
		event["node_id"] = strings.TrimSpace(nodeID)
	}
	e.appendProgress(event)
}

func sourceTargetMapFromManifest(manifest *InputManifest) map[string]string {
	out := map[string]string{}
	if manifest == nil {
		return out
	}
	for _, entry := range manifest.SourceTargetMap {
		source := strings.TrimSpace(entry.Source)
		target := strings.TrimSpace(entry.Target)
		if source == "" || target == "" {
			continue
		}
		out[source] = target
	}
	return out
}

func loadInputManifest(path string) (*InputManifest, error) {
	b, err := os.ReadFile(strings.TrimSpace(path))
	if err != nil {
		return nil, err
	}
	var manifest InputManifest
	if err := json.Unmarshal(b, &manifest); err != nil {
		return nil, err
	}
	return &manifest, nil
}

func loadInputInferenceCache(path string) map[string][]InferredReference {
	cache := map[string][]InferredReference{}
	b, err := os.ReadFile(strings.TrimSpace(path))
	if err != nil {
		return cache
	}
	if err := json.Unmarshal(b, &cache); err != nil {
		return map[string][]InferredReference{}
	}
	return cache
}

func inputFailureOutcomeFromMaterializationError(err error) runtime.Outcome {
	if includeErr, ok := err.(*inputIncludeMissingError); ok {
		return runtime.Outcome{
			Status:        runtime.StatusFail,
			FailureReason: "input_include_missing",
			Notes:         strings.Join(includeErr.Patterns, ", "),
		}
	}
	return runtime.Outcome{
		Status:        runtime.StatusFail,
		FailureReason: strings.TrimSpace(err.Error()),
	}
}
