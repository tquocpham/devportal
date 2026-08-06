package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type FileEntry struct {
	Path    string
	RelPath string // relative to project root
	Changed bool
}

// WalkAll returns every indexable file in the project
func WalkAll(cfg Config) ([]FileEntry, error) {
	var files []FileEntry

	err := filepath.Walk(cfg.ProjectPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Skip common non-code directories
		if info.IsDir() {
			name := info.Name()
			if name == ".git" || name == "Binaries" || name == "Intermediate" ||
				name == "Saved" || name == "DerivedDataCache" || name == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}

		ext := strings.ToLower(filepath.Ext(path))
		for _, allowed := range cfg.Extensions {
			if ext == allowed {
				rel, _ := filepath.Rel(cfg.ProjectPath, path)
				files = append(files, FileEntry{
					Path:    path,
					RelPath: rel,
				})
				break
			}
		}

		return nil
	})

	return files, err
}

// WalkChanged returns only files changed since the last git commit
// Run this after a merge to do incremental re-indexing
func WalkChanged(cfg Config) ([]FileEntry, error) {
	// Get files changed between HEAD~1 and HEAD (i.e. the last merge)
	cmd := exec.Command("git", "-C", cfg.ProjectPath, "diff", "--name-only", "HEAD~1", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	changedPaths := map[string]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			changedPaths[line] = true
		}
	}

	all, err := WalkAll(cfg)
	if err != nil {
		return nil, err
	}

	var changed []FileEntry
	for _, f := range all {
		if changedPaths[f.RelPath] {
			f.Changed = true
			changed = append(changed, f)
		}
	}

	return changed, nil
}
