package engine

import (
	"os"
	"path/filepath"
	"testing"
)

// --- Task 1: Basic store/retrieve (in-memory) ---

func TestArtifactStore_StoreAndRetrieve_InMemory(t *testing.T) {
	store := NewArtifactStore("", DefaultFileBackingThreshold)
	data := []byte("hello world")
	info, err := store.Store("art-1", "greeting", data)
	if err != nil {
		t.Fatalf("Store() error: %v", err)
	}
	if info.ID != "art-1" {
		t.Fatalf("info.ID = %q, want %q", info.ID, "art-1")
	}
	if info.Name != "greeting" {
		t.Fatalf("info.Name = %q, want %q", info.Name, "greeting")
	}
	if info.SizeBytes != int64(len(data)) {
		t.Fatalf("info.SizeBytes = %d, want %d", info.SizeBytes, len(data))
	}
	if info.IsFileBacked {
		t.Fatalf("expected in-memory storage for small artifact")
	}
	if info.StoredAt.IsZero() {
		t.Fatalf("info.StoredAt should not be zero")
	}

	got, err := store.Retrieve("art-1")
	if err != nil {
		t.Fatalf("Retrieve() error: %v", err)
	}
	if string(got) != string(data) {
		t.Fatalf("Retrieve() = %q, want %q", string(got), string(data))
	}
}

// --- Task 2: File-backed storage ---

func TestArtifactStore_StoreAndRetrieve_FileBacked(t *testing.T) {
	dir := t.TempDir()
	// Use threshold=10 so even small test data triggers file-backing.
	store := NewArtifactStore(dir, 10)
	data := []byte("this data exceeds the 10 byte threshold for testing")
	info, err := store.Store("big-1", "large-output", data)
	if err != nil {
		t.Fatalf("Store() error: %v", err)
	}
	if !info.IsFileBacked {
		t.Fatalf("expected file-backed storage")
	}

	// Verify file was created at the spec §5.6 path: {base_dir}/artifacts/{artifact_id}.json
	artifactPath := filepath.Join(dir, "artifacts", "big-1.json")
	if _, err := os.Stat(artifactPath); err != nil {
		t.Fatalf("artifact file not created: %v", err)
	}

	got, err := store.Retrieve("big-1")
	if err != nil {
		t.Fatalf("Retrieve() error: %v", err)
	}
	if string(got) != string(data) {
		t.Fatalf("Retrieve() data mismatch")
	}
}

func TestArtifactStore_StoreFileBacked_NoBaseDir_FallsToMemory(t *testing.T) {
	// When baseDir is empty, file-backing is disabled regardless of threshold.
	store := NewArtifactStore("", 10)
	data := []byte("this data exceeds the 10 byte threshold")
	info, err := store.Store("art-mem", "fallback", data)
	if err != nil {
		t.Fatalf("Store() error: %v", err)
	}
	if info.IsFileBacked {
		t.Fatalf("expected in-memory when baseDir is empty")
	}
}

// --- Task 3: has, list, remove, clear, retrieve not found, content hash ---

func TestArtifactStore_Has(t *testing.T) {
	store := NewArtifactStore("", DefaultFileBackingThreshold)
	if store.Has("missing") {
		t.Fatalf("Has(missing) should be false")
	}
	if _, err := store.Store("x", "x", []byte("data")); err != nil {
		t.Fatalf("Store: %v", err)
	}
	if !store.Has("x") {
		t.Fatalf("Has(x) should be true after store")
	}
}

func TestArtifactStore_List(t *testing.T) {
	store := NewArtifactStore("", DefaultFileBackingThreshold)
	if _, err := store.Store("b", "second", []byte("2")); err != nil {
		t.Fatalf("Store: %v", err)
	}
	if _, err := store.Store("a", "first", []byte("1")); err != nil {
		t.Fatalf("Store: %v", err)
	}
	infos := store.List()
	if len(infos) != 2 {
		t.Fatalf("List() len = %d, want 2", len(infos))
	}
	// Sorted by ID.
	if infos[0].ID != "a" || infos[1].ID != "b" {
		t.Fatalf("List() not sorted: got %q, %q", infos[0].ID, infos[1].ID)
	}
}

func TestArtifactStore_List_Empty(t *testing.T) {
	store := NewArtifactStore("", DefaultFileBackingThreshold)
	infos := store.List()
	if len(infos) != 0 {
		t.Fatalf("List() on empty store should return empty slice, got len %d", len(infos))
	}
}

func TestArtifactStore_Remove(t *testing.T) {
	store := NewArtifactStore("", DefaultFileBackingThreshold)
	if store.Remove("missing") {
		t.Fatalf("Remove(missing) should return false")
	}
	if _, err := store.Store("x", "x", []byte("data")); err != nil {
		t.Fatalf("Store: %v", err)
	}
	if !store.Remove("x") {
		t.Fatalf("Remove(x) should return true")
	}
	if store.Has("x") {
		t.Fatalf("Has(x) should be false after remove")
	}
}

func TestArtifactStore_Remove_CleansUpFile(t *testing.T) {
	dir := t.TempDir()
	store := NewArtifactStore(dir, 10)
	if _, err := store.Store("big", "big", []byte("this exceeds threshold")); err != nil {
		t.Fatalf("Store: %v", err)
	}
	artifactPath := filepath.Join(dir, "artifacts", "big.json")
	if _, err := os.Stat(artifactPath); err != nil {
		t.Fatalf("artifact file should exist before remove")
	}
	store.Remove("big")
	if _, err := os.Stat(artifactPath); !os.IsNotExist(err) {
		t.Fatalf("artifact file should be deleted after remove")
	}
}

func TestArtifactStore_Clear(t *testing.T) {
	store := NewArtifactStore("", DefaultFileBackingThreshold)
	if _, err := store.Store("a", "a", []byte("1")); err != nil {
		t.Fatalf("Store: %v", err)
	}
	if _, err := store.Store("b", "b", []byte("2")); err != nil {
		t.Fatalf("Store: %v", err)
	}
	if _, err := store.Store("c", "c", []byte("3")); err != nil {
		t.Fatalf("Store: %v", err)
	}
	count := store.Clear()
	if count != 3 {
		t.Fatalf("Clear() = %d, want 3", count)
	}
	if len(store.List()) != 0 {
		t.Fatalf("List() should be empty after clear")
	}
}

func TestArtifactStore_Clear_CleansUpFiles(t *testing.T) {
	dir := t.TempDir()
	store := NewArtifactStore(dir, 10)
	if _, err := store.Store("f1", "f1", []byte("exceeds threshold data")); err != nil {
		t.Fatalf("Store: %v", err)
	}
	if _, err := store.Store("f2", "f2", []byte("also exceeds threshold")); err != nil {
		t.Fatalf("Store: %v", err)
	}
	count := store.Clear()
	if count != 2 {
		t.Fatalf("Clear() = %d, want 2", count)
	}
	// Both files should be gone.
	for _, id := range []string{"f1", "f2"} {
		path := filepath.Join(dir, "artifacts", id+".json")
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("artifact file %s should be deleted after clear", id)
		}
	}
}

func TestArtifactStore_Retrieve_NotFound(t *testing.T) {
	store := NewArtifactStore("", DefaultFileBackingThreshold)
	_, err := store.Retrieve("missing")
	if err == nil {
		t.Fatalf("Retrieve(missing) should return error")
	}
}

func TestArtifactStore_ContentHash(t *testing.T) {
	store := NewArtifactStore("", DefaultFileBackingThreshold)
	data := []byte("deterministic content")
	info1, err := store.Store("a", "a", data)
	if err != nil {
		t.Fatalf("Store: %v", err)
	}
	store.Remove("a")
	info2, err := store.Store("a", "a", data)
	if err != nil {
		t.Fatalf("Store: %v", err)
	}
	if info1.ContentHash != info2.ContentHash {
		t.Fatalf("ContentHash should be deterministic: %q != %q", info1.ContentHash, info2.ContentHash)
	}
	if info1.ContentHash == "" {
		t.Fatalf("ContentHash should not be empty")
	}
}

func TestArtifactStore_StoreReplace_InMemory(t *testing.T) {
	store := NewArtifactStore("", DefaultFileBackingThreshold)
	if _, err := store.Store("x", "original", []byte("v1")); err != nil {
		t.Fatalf("Store: %v", err)
	}
	if _, err := store.Store("x", "replaced", []byte("v2")); err != nil {
		t.Fatalf("Store: %v", err)
	}
	got, err := store.Retrieve("x")
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if string(got) != "v2" {
		t.Fatalf("Retrieve after replace = %q, want %q", string(got), "v2")
	}
	info, ok := store.Info("x")
	if !ok {
		t.Fatalf("Info(x) should return true after store")
	}
	if info.Name != "replaced" {
		t.Fatalf("Info.Name = %q, want %q", info.Name, "replaced")
	}
}

func TestArtifactStore_Info(t *testing.T) {
	store := NewArtifactStore("", DefaultFileBackingThreshold)
	if _, ok := store.Info("missing"); ok {
		t.Fatalf("Info(missing) should return false")
	}
	if _, err := store.Store("x", "test-name", []byte("data")); err != nil {
		t.Fatalf("Store: %v", err)
	}
	info, ok := store.Info("x")
	if !ok {
		t.Fatalf("Info(x) should return true")
	}
	if info.ID != "x" || info.Name != "test-name" {
		t.Fatalf("Info mismatch: ID=%q Name=%q", info.ID, info.Name)
	}
}

// --- Path traversal prevention (security) ---

func TestArtifactStore_PathTraversal_Store(t *testing.T) {
	dir := t.TempDir()
	store := NewArtifactStore(dir, 10)
	malicious := []string{
		"../../../etc/passwd",
		"../../escape",
		"foo/bar",
		"foo\\bar",
		"..secret",
		"/etc/shadow",
		"",
		".",
		"..",
	}
	for _, id := range malicious {
		_, err := store.Store(id, "test", []byte("data that exceeds the threshold for file-backed"))
		if err == nil {
			t.Errorf("Store(%q) should have returned error, got nil", id)
		}
	}
}

func TestArtifactStore_PathTraversal_Retrieve(t *testing.T) {
	store := NewArtifactStore("", DefaultFileBackingThreshold)
	malicious := []string{"../../../etc/passwd", "foo/bar", "foo\\bar", "..", ""}
	for _, id := range malicious {
		_, err := store.Retrieve(id)
		if err == nil {
			t.Errorf("Retrieve(%q) should have returned error, got nil", id)
		}
	}
}

func TestArtifactStore_PathTraversal_Remove(t *testing.T) {
	store := NewArtifactStore("", DefaultFileBackingThreshold)
	malicious := []string{"../../../etc/passwd", "foo/bar", "..", ""}
	for _, id := range malicious {
		if store.Remove(id) {
			t.Errorf("Remove(%q) should return false for invalid ID", id)
		}
	}
}

func TestArtifactStore_PathTraversal_Has(t *testing.T) {
	store := NewArtifactStore("", DefaultFileBackingThreshold)
	malicious := []string{"../../../etc/passwd", "foo/bar", "..", ""}
	for _, id := range malicious {
		if store.Has(id) {
			t.Errorf("Has(%q) should return false for invalid ID", id)
		}
	}
}

func TestArtifactStore_PathTraversal_Info(t *testing.T) {
	store := NewArtifactStore("", DefaultFileBackingThreshold)
	malicious := []string{"../../../etc/passwd", "foo/bar", "..", ""}
	for _, id := range malicious {
		if _, ok := store.Info(id); ok {
			t.Errorf("Info(%q) should return false for invalid ID", id)
		}
	}
}

func TestArtifactStore_SafeID_Accepted(t *testing.T) {
	store := NewArtifactStore("", DefaultFileBackingThreshold)
	// Normal IDs without path separators should work fine.
	safeIDs := []string{"art-1", "my_artifact", "artifact.v2", "UPPER-case-123"}
	for _, id := range safeIDs {
		_, err := store.Store(id, "test", []byte("data"))
		if err != nil {
			t.Errorf("Store(%q) should succeed, got error: %v", id, err)
		}
	}
}

// --- Task 4: Execution integration ---

func TestArtifactStore_AccessibleFromExecution(t *testing.T) {
	store := NewArtifactStore("", DefaultFileBackingThreshold)
	exec := &Execution{
		Artifacts: store,
	}
	if exec.Artifacts == nil {
		t.Fatalf("Execution.Artifacts should not be nil")
	}
	info, err := exec.Artifacts.Store("test-1", "test", []byte("data"))
	if err != nil {
		t.Fatalf("Store via Execution error: %v", err)
	}
	if info.ID != "test-1" {
		t.Fatalf("unexpected ID: %q", info.ID)
	}
}
