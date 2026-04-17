// File browser API endpoints for run logs and workspace directories.
// Provides directory listing and file download with path traversal protection.
package server

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/danshapiro/kilroy/internal/attractor/rundb"
)

type fileEntry struct {
	Name       string    `json:"name"`
	Size       int64     `json:"size"`
	IsDir      bool      `json:"is_dir"`
	ModifiedAt time.Time `json:"modified_at"`
}

// resolveRunDirs looks up logs_root and worktree_dir for a run ID.
func (s *Server) resolveRunDirs(id string) (logsRoot, worktreeDir string) {
	db, err := rundb.Open(rundb.DefaultPath())
	if err == nil {
		defer db.Close()
		run, err := db.GetRun(id)
		if err == nil && run != nil {
			logsRoot = run.LogsRoot
			worktreeDir = run.WorktreeDir
		}
	}
	if logsRoot == "" {
		if p, ok := s.registry.Get(id); ok && p != nil {
			logsRoot = p.LogsRoot
		}
	}
	return
}

func (s *Server) handleBrowseFiles(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	subpath := r.PathValue("path")

	logsRoot, _ := s.resolveRunDirs(id)
	if logsRoot == "" {
		writeError(w, http.StatusNotFound, "run not found")
		return
	}

	serveDirOrFile(w, logsRoot, subpath)
}

func (s *Server) handleBrowseWorkspace(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	subpath := r.PathValue("path")

	_, worktreeDir := s.resolveRunDirs(id)
	if worktreeDir == "" {
		writeError(w, http.StatusNotFound, "run not found or no workspace")
		return
	}

	if _, err := os.Stat(worktreeDir); err != nil {
		writeError(w, http.StatusNotFound, "workspace no longer available")
		return
	}

	serveDirOrFile(w, worktreeDir, subpath)
}

// serveDirOrFile handles both directory listing and file download.
func serveDirOrFile(w http.ResponseWriter, root, subpath string) {
	// Sanitize path to prevent traversal.
	clean := filepath.Clean("/" + subpath)
	if strings.Contains(clean, "..") {
		writeError(w, http.StatusBadRequest, "invalid path")
		return
	}

	target := filepath.Join(root, clean)

	// Ensure the resolved path is within root.
	absRoot, _ := filepath.Abs(root)
	absTarget, _ := filepath.Abs(target)
	if !strings.HasPrefix(absTarget, absRoot) {
		writeError(w, http.StatusBadRequest, "path traversal denied")
		return
	}

	info, err := os.Stat(target)
	if err != nil {
		writeError(w, http.StatusNotFound, "not found: "+clean)
		return
	}

	if info.IsDir() {
		listDirectory(w, target, clean)
		return
	}

	serveFile(w, target, info)
}

func listDirectory(w http.ResponseWriter, dirPath, relPath string) {
	entries, err := os.ReadDir(dirPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "read directory: "+err.Error())
		return
	}

	var files []fileEntry
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, fileEntry{
			Name:       e.Name(),
			Size:       info.Size(),
			IsDir:      e.IsDir(),
			ModifiedAt: info.ModTime().UTC(),
		})
	}

	if files == nil {
		files = []fileEntry{}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"path":  relPath,
		"files": files,
		"count": len(files),
	})
}

func serveFile(w http.ResponseWriter, filePath string, info os.FileInfo) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "read file: "+err.Error())
		return
	}

	// Set content type based on extension.
	ext := strings.ToLower(filepath.Ext(filePath))
	switch ext {
	case ".json":
		w.Header().Set("Content-Type", "application/json")
	case ".md":
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	case ".txt", ".log":
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	case ".dot":
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	case ".html":
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
	case ".yaml", ".yml":
		w.Header().Set("Content-Type", "text/yaml; charset=utf-8")
	case ".png":
		w.Header().Set("Content-Type", "image/png")
	case ".jpg", ".jpeg":
		w.Header().Set("Content-Type", "image/jpeg")
	case ".svg":
		w.Header().Set("Content-Type", "image/svg+xml")
	default:
		w.Header().Set("Content-Type", "application/octet-stream")
	}

	w.WriteHeader(http.StatusOK)
	w.Write(data)
}
