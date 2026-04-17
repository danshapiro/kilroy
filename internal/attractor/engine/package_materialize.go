// Copies workflow package scripts and prompts into the run workspace.
// Called during engine.run() when PackageDir is set in RunOptions.
package engine

import (
	"os"
	"path/filepath"
	"strings"
)

// materializePackage copies scripts/ and prompts/ from packageDir into
// workspaceDir at .kilroy/package/. Shell scripts are made executable.
func materializePackage(packageDir, workspaceDir string) error {
	mountDir := filepath.Join(workspaceDir, ".kilroy", "package")
	if err := os.MkdirAll(mountDir, 0o755); err != nil {
		return err
	}

	for _, subdir := range []string{"scripts", "prompts"} {
		src := filepath.Join(packageDir, subdir)
		info, err := os.Stat(src)
		if err != nil || !info.IsDir() {
			continue
		}
		dst := filepath.Join(mountDir, subdir)
		if err := copyDirContents(src, dst); err != nil {
			return err
		}
		// Make shell scripts executable.
		if subdir == "scripts" {
			_ = filepath.WalkDir(dst, func(path string, d os.DirEntry, err error) error {
				if err != nil || d.IsDir() {
					return err
				}
				if strings.HasSuffix(path, ".sh") || strings.HasSuffix(path, ".bash") {
					_ = os.Chmod(path, 0o755)
				}
				return nil
			})
		}
	}
	return nil
}
