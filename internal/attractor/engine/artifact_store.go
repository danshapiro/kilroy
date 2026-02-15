package engine

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// sanitizeArtifactID validates and sanitizes an artifact ID to prevent path
// traversal attacks (e.g., "../../../etc/passwd"). Returns an error if the ID
// is empty, contains path separators, or contains ".." components.
func sanitizeArtifactID(id string) (string, error) {
	if id == "" {
		return "", fmt.Errorf("artifact ID must not be empty")
	}
	if strings.Contains(id, "/") || strings.Contains(id, "\\") {
		return "", fmt.Errorf("artifact ID must not contain path separators: %q", id)
	}
	if strings.Contains(id, "..") {
		return "", fmt.Errorf("artifact ID must not contain '..': %q", id)
	}
	// Safety net: filepath.Base strips any remaining path components.
	safe := filepath.Base(id)
	if safe != id || safe == "." || safe == ".." {
		return "", fmt.Errorf("artifact ID is not a safe filename: %q", id)
	}
	return safe, nil
}

// DefaultFileBackingThreshold is 100KB per spec §5.5.
// Artifacts below this threshold are stored in memory; above it, they are
// written to disk under the run's artifacts/ subdirectory.
const DefaultFileBackingThreshold = 100 * 1024

// ArtifactInfo describes a stored artifact (spec §5.5 ArtifactInfo).
type ArtifactInfo struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	SizeBytes    int64     `json:"size_bytes"`
	StoredAt     time.Time `json:"stored_at"`
	IsFileBacked bool      `json:"is_file_backed"`
	ContentHash  string    `json:"content_hash,omitempty"`
}

// artifactEntry holds the info and the stored data (either in-memory bytes or file path).
type artifactEntry struct {
	info ArtifactInfo
	data []byte // non-nil when in-memory
	path string // non-empty when file-backed
}

// ArtifactStore provides named, typed storage for large stage outputs
// (spec §5.5). Thread-safe via RWMutex per the spec's ReadWriteLock requirement.
type ArtifactStore struct {
	mu        sync.RWMutex
	artifacts map[string]*artifactEntry
	baseDir   string // empty means no file backing possible
	threshold int64  // file-backing threshold in bytes
}

// NewArtifactStore creates a new artifact store.
// baseDir is the run's LogsRoot (artifacts/ subdirectory is created underneath
// on first file-backed write). Pass "" to disable file-backing entirely.
// threshold is the file-backing threshold in bytes; use DefaultFileBackingThreshold
// for the spec default of 100KB.
func NewArtifactStore(baseDir string, threshold int64) *ArtifactStore {
	return &ArtifactStore{
		artifacts: make(map[string]*artifactEntry),
		baseDir:   baseDir,
		threshold: threshold,
	}
}

// Store adds or replaces an artifact (spec §5.5 store).
// Data is stored in memory if size <= threshold or baseDir is empty;
// otherwise it is written to {baseDir}/artifacts/{artifactID}.json.
func (s *ArtifactStore) Store(artifactID, name string, data []byte) (ArtifactInfo, error) {
	safeID, err := sanitizeArtifactID(artifactID)
	if err != nil {
		return ArtifactInfo{}, fmt.Errorf("invalid artifact ID: %w", err)
	}
	artifactID = safeID

	s.mu.Lock()
	defer s.mu.Unlock()

	size := int64(len(data))
	hash := sha256.Sum256(data)
	contentHash := fmt.Sprintf("sha256:%x", hash)

	isFileBacked := size > s.threshold && s.baseDir != ""
	info := ArtifactInfo{
		ID:           artifactID,
		Name:         name,
		SizeBytes:    size,
		StoredAt:     time.Now(),
		IsFileBacked: isFileBacked,
		ContentHash:  contentHash,
	}

	entry := &artifactEntry{info: info}
	if isFileBacked {
		dir := filepath.Join(s.baseDir, "artifacts")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return ArtifactInfo{}, fmt.Errorf("create artifacts dir: %w", err)
		}
		path := filepath.Join(dir, artifactID+".json")
		if err := os.WriteFile(path, data, 0o644); err != nil {
			return ArtifactInfo{}, fmt.Errorf("write artifact file: %w", err)
		}
		entry.path = path
	} else {
		stored := make([]byte, len(data))
		copy(stored, data)
		entry.data = stored
	}

	// If replacing a file-backed artifact, clean up the old file.
	if old, ok := s.artifacts[artifactID]; ok && old.info.IsFileBacked && old.path != "" {
		_ = os.Remove(old.path)
	}

	s.artifacts[artifactID] = entry
	return info, nil
}

// Retrieve returns the artifact data (spec §5.5 retrieve).
// For file-backed artifacts, the data is read from disk.
// Returns an error if the artifact does not exist.
func (s *ArtifactStore) Retrieve(artifactID string) ([]byte, error) {
	if _, err := sanitizeArtifactID(artifactID); err != nil {
		return nil, fmt.Errorf("invalid artifact ID: %w", err)
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	entry, ok := s.artifacts[artifactID]
	if !ok {
		return nil, fmt.Errorf("artifact not found: %s", artifactID)
	}
	if entry.info.IsFileBacked {
		data, err := os.ReadFile(entry.path)
		if err != nil {
			return nil, fmt.Errorf("read artifact file: %w", err)
		}
		return data, nil
	}
	// Return a copy to prevent callers from mutating the stored data.
	out := make([]byte, len(entry.data))
	copy(out, entry.data)
	return out, nil
}

// Has returns whether an artifact exists (spec §5.5 has).
func (s *ArtifactStore) Has(artifactID string) bool {
	if _, err := sanitizeArtifactID(artifactID); err != nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.artifacts[artifactID]
	return ok
}

// List returns all artifact infos, sorted by ID (spec §5.5 list).
func (s *ArtifactStore) List() []ArtifactInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	infos := make([]ArtifactInfo, 0, len(s.artifacts))
	for _, entry := range s.artifacts {
		infos = append(infos, entry.info)
	}
	sort.Slice(infos, func(i, j int) bool {
		return infos[i].ID < infos[j].ID
	})
	return infos
}

// Remove deletes an artifact, returning true if it existed (spec §5.5 remove).
// For file-backed artifacts, the backing file is also removed.
func (s *ArtifactStore) Remove(artifactID string) bool {
	if _, err := sanitizeArtifactID(artifactID); err != nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.artifacts[artifactID]
	if !ok {
		return false
	}
	if entry.info.IsFileBacked && entry.path != "" {
		_ = os.Remove(entry.path)
	}
	delete(s.artifacts, artifactID)
	return true
}

// Clear removes all artifacts, returning the count removed (spec §5.5 clear).
// For file-backed artifacts, the backing files are also removed.
func (s *ArtifactStore) Clear() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	count := len(s.artifacts)
	for _, entry := range s.artifacts {
		if entry.info.IsFileBacked && entry.path != "" {
			_ = os.Remove(entry.path)
		}
	}
	s.artifacts = make(map[string]*artifactEntry)
	return count
}

// Info returns the ArtifactInfo for a stored artifact, or false if not found.
// This is a convenience method for checking metadata without retrieving content.
func (s *ArtifactStore) Info(artifactID string) (ArtifactInfo, bool) {
	if _, err := sanitizeArtifactID(artifactID); err != nil {
		return ArtifactInfo{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	entry, ok := s.artifacts[artifactID]
	if !ok {
		return ArtifactInfo{}, false
	}
	return entry.info, true
}
