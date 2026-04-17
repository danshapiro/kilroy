// Workflow package loader. A package bundles a graph with scripts and prompts
// into a portable, self-contained directory.
package workflows

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// Package represents a self-contained workflow directory.
type Package struct {
	// Dir is the absolute path to the package directory.
	Dir string

	// GraphPath is the path to the DOT graph file.
	GraphPath string

	// Manifest is the parsed workflow.toml (nil if no manifest).
	Manifest *PackageManifest
}

// PackageManifest declares package metadata alongside the DOT graph.
type PackageManifest struct {
	Name        string            `toml:"name"`
	Description string            `toml:"description"`
	Version     string            `toml:"version"`
	Inputs      []ManifestInput   `toml:"inputs"`
	Outputs     []string          `toml:"outputs"`
	Defaults    ManifestDefaults  `toml:"defaults"`
	Metadata    map[string]string `toml:"metadata"`
}

// ManifestInput declares a required or optional input for the workflow.
type ManifestInput struct {
	Name        string `toml:"name"`
	Description string `toml:"description"`
	Required    bool   `toml:"required"`
	Default     string `toml:"default"`
}

// ManifestDefaults declares default settings for runs using this package.
type ManifestDefaults struct {
	Labels map[string]string `toml:"labels"`
}

// LoadPackage reads a workflow package from a directory. The directory must
// contain at minimum a graph.dot file. scripts/, prompts/, and workflow.toml
// are optional.
func LoadPackage(dir string) (*Package, error) {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("resolve package path: %w", err)
	}
	info, err := os.Stat(absDir)
	if err != nil {
		return nil, fmt.Errorf("package directory: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("package path is not a directory: %s", absDir)
	}

	// Find the graph file. Prefer graph.dot, fall back to *.dot.
	graphPath := filepath.Join(absDir, "graph.dot")
	if _, err := os.Stat(graphPath); os.IsNotExist(err) {
		entries, _ := filepath.Glob(filepath.Join(absDir, "*.dot"))
		if len(entries) == 1 {
			graphPath = entries[0]
		} else if len(entries) > 1 {
			return nil, fmt.Errorf("package has multiple .dot files; use graph.dot or provide exactly one")
		} else {
			return nil, fmt.Errorf("package missing graph.dot: %s", absDir)
		}
	}

	pkg := &Package{
		Dir:       absDir,
		GraphPath: graphPath,
	}

	// Load optional manifest.
	manifestPath := filepath.Join(absDir, "workflow.toml")
	if _, err := os.Stat(manifestPath); err == nil {
		var m PackageManifest
		if _, err := toml.DecodeFile(manifestPath, &m); err != nil {
			return nil, fmt.Errorf("parse workflow.toml: %w", err)
		}
		pkg.Manifest = &m
	}

	return pkg, nil
}

// RequiredInputNames returns the names of inputs marked as required.
func (p *Package) RequiredInputNames() []string {
	if p == nil || p.Manifest == nil {
		return nil
	}
	var names []string
	for _, in := range p.Manifest.Inputs {
		if in.Required {
			names = append(names, in.Name)
		}
	}
	return names
}

// HasScripts returns true if the package has a scripts/ directory.
func (p *Package) HasScripts() bool {
	if p == nil {
		return false
	}
	info, err := os.Stat(filepath.Join(p.Dir, "scripts"))
	return err == nil && info.IsDir()
}

// HasPrompts returns true if the package has a prompts/ directory.
func (p *Package) HasPrompts() bool {
	if p == nil {
		return false
	}
	info, err := os.Stat(filepath.Join(p.Dir, "prompts"))
	return err == nil && info.IsDir()
}

// MaterializeTo copies package scripts and prompts into the workspace at
// .kilroy/package/. Returns the mount path within the workspace.
func (p *Package) MaterializeTo(workspaceDir string) (string, error) {
	if p == nil {
		return "", fmt.Errorf("nil package")
	}
	mountDir := filepath.Join(workspaceDir, ".kilroy", "package")
	if err := os.MkdirAll(mountDir, 0o755); err != nil {
		return "", err
	}

	// Copy scripts/ if present.
	srcScripts := filepath.Join(p.Dir, "scripts")
	if info, err := os.Stat(srcScripts); err == nil && info.IsDir() {
		dstScripts := filepath.Join(mountDir, "scripts")
		if err := copyTree(srcScripts, dstScripts); err != nil {
			return "", fmt.Errorf("copy scripts: %w", err)
		}
		// Make scripts executable.
		_ = filepath.WalkDir(dstScripts, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			if strings.HasSuffix(path, ".sh") || strings.HasSuffix(path, ".bash") {
				_ = os.Chmod(path, 0o755)
			}
			return nil
		})
	}

	// Copy prompts/ if present.
	srcPrompts := filepath.Join(p.Dir, "prompts")
	if info, err := os.Stat(srcPrompts); err == nil && info.IsDir() {
		if err := copyTree(srcPrompts, filepath.Join(mountDir, "prompts")); err != nil {
			return "", fmt.Errorf("copy prompts: %w", err)
		}
	}

	return mountDir, nil
}

// copyTree recursively copies src directory to dst.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode())
	})
}
