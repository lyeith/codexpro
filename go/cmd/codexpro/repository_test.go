package main

import (
	"io/fs"
	"path/filepath"
	"testing"
)

// CodexPro is Go only. The TypeScript service and its Node tooling are retired;
// this keeps their sources from returning.
func TestRepositoryHasNoTypeScriptOrNodeModules(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	retired := map[string]bool{".ts": true, ".tsx": true, ".mts": true, ".cts": true, ".js": true, ".jsx": true, ".mjs": true, ".cjs": true}
	retiredNames := map[string]bool{"package.json": true, "package-lock.json": true, "tsconfig.json": true}
	var found []string
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if name := entry.Name(); name == ".git" || name == "node_modules" || name == "__pycache__" {
				return filepath.SkipDir
			}
			return nil
		}
		if retired[filepath.Ext(entry.Name())] || retiredNames[entry.Name()] {
			rel, _ := filepath.Rel(root, path)
			found = append(found, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 0 {
		t.Fatalf("retired TypeScript/Node files are back: %v", found)
	}
}
