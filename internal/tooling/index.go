package tooling

import (
	"io/fs"
	"path"
	"path/filepath"
	"strings"
)

// FSPathIndex is a static index of a directory tree: the set of relative paths and
// the set of directories that exist in it. "" is the root.
type FSPathIndex struct {
	files map[string]struct{}
	dirs  map[string]struct{}
}

var defaultSkip = []string{".git", "node_modules", "__pycache__", ".venv", "checkpoints", "needle-data"}

// IndexFS walks root and records every file and directory, skipping the vendored,
// generated and VCS trees named in skip (defaultSkip when empty).
func IndexFS(root string, skip ...string) (*FSPathIndex, error) {
	if len(skip) == 0 {
		skip = defaultSkip
	}
	skipped := make(map[string]struct{}, len(skip))
	for _, name := range skip {
		skipped[name] = struct{}{}
	}

	index := &FSPathIndex{
		files: make(map[string]struct{}),
		dirs:  map[string]struct{}{"": {}},
	}
	err := filepath.WalkDir(root, func(current string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if current == root {
			return nil
		}
		if _, skip := skipped[entry.Name()]; skip {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		rel, relErr := filepath.Rel(root, current)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		if entry.IsDir() {
			index.dirs[rel] = struct{}{}
			return nil
		}
		index.files[rel] = struct{}{}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return index, nil
}

// HasPath reports whether rel names a file in the tree.
func (i *FSPathIndex) HasPath(rel string) bool {
	_, ok := i.files[normalize(rel)]
	return ok
}

// HasDir reports whether rel names a directory in the tree ("" is the root).
func (i *FSPathIndex) HasDir(rel string) bool {
	_, ok := i.dirs[normalize(rel)]
	return ok
}

// Len reports the indexed file count.
func (i *FSPathIndex) Len() int { return len(i.files) }

func normalize(rel string) string {
	clean := path.Clean(filepath.ToSlash(strings.TrimSpace(rel)))
	if clean == "." || clean == "/" {
		return ""
	}
	return strings.TrimPrefix(clean, "./")
}
