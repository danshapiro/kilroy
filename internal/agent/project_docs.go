package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type ProjectDoc struct {
	// Path is a stable, human-friendly identifier for the instruction file (relative to git root when available).
	Path string
	// Content is the raw file content (may be truncated when total budget is exceeded).
	Content string
}

const (
	projectDocByteBudget = 32 * 1024
	projectDocTruncMark  = "[Project instructions truncated at 32KB]"
)

// LoadProjectDocs discovers and loads project instruction files from git root (or working directory when not
// in a git repo) down to the current working directory. Files are loaded in depth order (root first; deeper
// files have higher precedence) and filtered by the active provider profile (caller-provided list).
func LoadProjectDocs(env ExecutionEnvironment, filenames ...string) ([]ProjectDoc, bool) {
	if env == nil {
		return nil, false
	}

	cwd := strings.TrimSpace(env.WorkingDirectory())
	if cwd == "" {
		return nil, false
	}

	root := cwd
	if gr := gitRootOrEmpty(env, cwd); gr != "" {
		root = gr
	}

	dirs := dirsFromRootToCwd(root, cwd)
	out := []ProjectDoc{}
	used := 0
	for _, dir := range dirs {
		relDir := "."
		if r, err := filepath.Rel(root, dir); err == nil {
			relDir = r
		}
		for _, name := range filenames {
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			path := filepath.Join(dir, name)
			b, err := os.ReadFile(path)
			if err != nil {
				continue
			}

			key := name
			if relDir != "." && relDir != "" {
				key = filepath.Join(relDir, name)
			}

			content := string(b)
			if used+len(content) > projectDocByteBudget {
				remain := projectDocByteBudget - used
				if remain < 0 {
					remain = 0
				}
				if remain < len(content) {
					content = content[:remain]
				}
				if !strings.HasSuffix(content, "\n") {
					content += "\n"
				}
				content += projectDocTruncMark + "\n"
				out = append(out, ProjectDoc{Path: key, Content: content})
				return out, true
			}
			used += len(content)
			out = append(out, ProjectDoc{Path: key, Content: content})
		}
	}
	return out, false
}

func dirsFromRootToCwd(root, cwd string) []string {
	root = filepath.Clean(root)
	cwd = filepath.Clean(cwd)

	rel, err := filepath.Rel(root, cwd)
	if err != nil {
		return []string{cwd}
	}
	if rel == "." {
		return []string{root}
	}
	// If cwd is outside root, just treat cwd as the only directory.
	if strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." {
		return []string{cwd}
	}

	out := []string{root}
	cur := root
	for _, p := range strings.Split(rel, string(filepath.Separator)) {
		if p == "" || p == "." {
			continue
		}
		cur = filepath.Join(cur, p)
		out = append(out, cur)
	}
	return out
}

func gitRootOrEmpty(env ExecutionEnvironment, cwd string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	res, err := env.ExecCommand(ctx, "git rev-parse --show-toplevel", 2_000, cwd, nil)
	if err != nil || res.ExitCode != 0 {
		return ""
	}
	root := strings.TrimSpace(res.Stdout)
	if root == "" {
		return ""
	}
	// Best-effort sanity check: ensure the returned root is a prefix of cwd.
	root = filepath.Clean(root)
	cwd = filepath.Clean(cwd)
	if root != cwd && !strings.HasPrefix(cwd, root+string(filepath.Separator)) {
		return ""
	}
	return root
}
