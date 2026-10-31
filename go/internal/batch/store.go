package batch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/lyeith/codexpro/go/internal/files"
	"github.com/lyeith/codexpro/go/internal/process"
	"github.com/lyeith/codexpro/go/internal/wire"
)

const StoreDir = ".codexpro-batches"
const StoreLimit = 20
const DefinitionVersion = 1

var autoFile = regexp.MustCompile(`(?i)^[0-9a-f]{4}\.json$`)

type StoredOperation struct {
	ID   string         `json:"id"`
	Tool string         `json:"tool"`
	Args map[string]any `json:"args"`
}
type Definition struct {
	Version         int               `json:"version"`
	Mode            string            `json:"mode"`
	ContinueOnError bool              `json:"continue_on_error"`
	Operations      []StoredOperation `json:"operations"`
}
type storedFile struct {
	Path, Abs string
	Modified  time.Time
}
type storedResult struct {
	Path, Tag   string
	Pruned      []string
	GitExcluded bool
}
type loadedFile struct {
	Path       string
	Raw        any
	AutoStored bool
}

func autoStored(path string) bool {
	path = strings.TrimPrefix(filepath.ToSlash(path), "./")
	return strings.HasPrefix(path, StoreDir+"/") && autoFile.MatchString(strings.TrimPrefix(path, StoreDir+"/"))
}

func (s *Service) load(w wire.Workspace, path string) (loadedFile, error) {
	if strings.TrimSpace(path) == "" {
		return loadedFile{}, wire.Errorf("", "batch path must not be empty.")
	}
	guard := s.fileService()
	abs, rel, err := guard.ResolvePath(w, path, false)
	if err != nil {
		return loadedFile{}, err
	}
	if !strings.HasSuffix(strings.ToLower(rel), ".json") {
		return loadedFile{}, wire.Errorf("", "Batch definition must be a .json file: %s", rel)
	}
	b, err := files.ReadSource(abs, max(s.cfg.Int("maxReadBytes", 200000), s.cfg.Int("maxWriteBytes", 2000000)))
	if err != nil {
		return loadedFile{}, err
	}
	var raw any
	if err = json.Unmarshal([]byte(files.DecodeText(b)), &raw); err != nil {
		return loadedFile{}, wire.Errorf("", "Invalid batch JSON in %s: %s", rel, err)
	}
	return loadedFile{Path: rel, Raw: raw, AutoStored: autoStored(rel)}, nil
}

func ensureExclude(ctx context.Context, w wire.Workspace, guard *files.Service) (bool, error) {
	top, err := process.RunBounded(ctx, 3*time.Second, 128*1024, "git", "-C", w.Root, "rev-parse", "--show-toplevel")
	if err != nil {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		return false, nil
	}
	exclude, err := process.RunBounded(ctx, 3*time.Second, 128*1024, "git", "-C", w.Root, "rev-parse", "--git-path", "info/exclude")
	if err != nil {
		return false, wire.Errorf("", "Unable to locate Git's local exclude file.")
	}
	gitRoot, err := filepath.Abs(strings.TrimSpace(string(top)))
	if err != nil {
		return false, err
	}
	rel, err := filepath.Rel(gitRoot, w.Root)
	if err != nil || !process.Within(gitRoot, w.Root) {
		return false, wire.Errorf("", "Workspace root is outside the Git top-level directory while preparing batch-file exclusion.")
	}
	line := "/"
	if rel != "." {
		line += filepath.ToSlash(rel) + "/"
	}
	line += StoreDir + "/"
	excludePath := strings.TrimSpace(string(exclude))
	if !filepath.IsAbs(excludePath) {
		excludePath = filepath.Join(w.Root, excludePath)
	}
	unlock := guard.LockPaths(excludePath)
	defer unlock()
	if err = os.MkdirAll(filepath.Dir(excludePath), 0700); err != nil {
		return false, err
	}
	var before []byte
	f, err := os.Open(excludePath)
	if err == nil {
		before, err = io.ReadAll(io.LimitReader(f, 8*1024*1024+1))
		_ = f.Close()
		if err != nil {
			return false, err
		}
		if len(before) > 8*1024*1024 {
			return false, errors.New("Git local exclude file exceeds the bounded metadata limit")
		}
	} else if !os.IsNotExist(err) {
		return false, err
	}
	for _, entry := range strings.Split(strings.ReplaceAll(string(before), "\r\n", "\n"), "\n") {
		if entry == line {
			return true, nil
		}
	}
	prefix := ""
	if len(before) > 0 && before[len(before)-1] != '\n' {
		prefix = "\n"
	}
	f, err = os.OpenFile(excludePath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0600)
	if err != nil {
		return false, err
	}
	_, err = io.WriteString(f, prefix+line+"\n")
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (s *Service) batchFiles(w wire.Workspace) ([]storedFile, error) {
	guard := s.fileService()
	directory, _, err := guard.ResolvePath(w, StoreDir, true)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(directory)
	if os.IsNotExist(err) {
		return []storedFile{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := []storedFile{}
	for _, entry := range entries {
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || !autoFile.MatchString(entry.Name()) {
			continue
		}
		abs, rel, err := guard.ResolvePath(w, StoreDir+"/"+entry.Name(), true)
		if err != nil {
			return nil, err
		}
		info, err := os.Stat(abs)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() {
			continue
		}
		out = append(out, storedFile{Path: rel, Abs: abs, Modified: info.ModTime()})
	}
	return out, nil
}

func retainedFiles(files []storedFile, keep string) map[string]bool {
	ordered := append([]storedFile{}, files...)
	sort.Slice(ordered, func(i, j int) bool {
		if !ordered[i].Modified.Equal(ordered[j].Modified) {
			return ordered[i].Modified.After(ordered[j].Modified)
		}
		return ordered[i].Path > ordered[j].Path
	})
	retained := map[string]bool{}
	if keep != "" {
		retained[filepath.ToSlash(keep)] = true
	}
	for _, file := range ordered {
		if len(retained) >= StoreLimit {
			break
		}
		retained[filepath.ToSlash(file.Path)] = true
	}
	return retained
}

func (s *Service) prune(w wire.Workspace, keep string) ([]string, error) {
	items, err := s.batchFiles(w)
	if err != nil {
		return nil, err
	}
	retained := retainedFiles(items, keep)
	candidates := []storedFile{}
	paths := []string{}
	for _, file := range items {
		if !retained[filepath.ToSlash(file.Path)] {
			candidates = append(candidates, file)
			paths = append(paths, file.Abs)
		}
	}
	if len(candidates) == 0 {
		return []string{}, nil
	}
	unlock := s.fileService().LockPaths(paths...)
	defer unlock()
	current, err := s.batchFiles(w)
	if err != nil {
		return nil, err
	}
	currentPaths := map[string]bool{}
	for _, file := range current {
		currentPaths[filepath.ToSlash(file.Path)] = true
	}
	retained = retainedFiles(current, keep)
	removed := []string{}
	for _, file := range candidates {
		key := filepath.ToSlash(file.Path)
		if !currentPaths[key] || retained[key] {
			continue
		}
		if err = os.Remove(file.Abs); err == nil {
			removed = append(removed, file.Path)
		} else if !os.IsNotExist(err) {
			return nil, err
		}
	}
	if len(removed) > 0 {
		directory, _, err := s.fileService().ResolvePath(w, StoreDir, true)
		if err != nil {
			return nil, err
		}
		if err = process.SyncDir(directory); err != nil {
			return nil, err
		}
	}
	return removed, nil
}

func (s *Service) materialize(ctx context.Context, scope wire.Scope, w wire.Workspace, definition Definition) (storedResult, error) {
	guard := s.fileService()
	directory, _, err := guard.ResolvePath(w, StoreDir, true)
	if err != nil {
		return storedResult{}, err
	}
	unlock := guard.LockPaths(directory)
	defer unlock()
	excluded, err := ensureExclude(ctx, w, guard)
	if err != nil {
		return storedResult{}, err
	}
	var tag, rel string
	for i := 0; i < 100; i++ {
		random, err := process.RandomHex(2)
		if err != nil {
			return storedResult{}, err
		}
		tag = strings.ToUpper(random)
		abs, path, err := guard.ResolvePath(w, StoreDir+"/"+tag+".json", true)
		if err != nil {
			return storedResult{}, err
		}
		if _, err = os.Lstat(abs); os.IsNotExist(err) {
			rel = path
			break
		} else if err != nil {
			return storedResult{}, err
		}
	}
	if rel == "" {
		return storedResult{}, wire.Errorf("", "Unable to allocate a unique four-hex batch file name. Remove old batch files and retry.")
	}
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err = encoder.Encode(definition); err != nil {
		return storedResult{}, err
	}
	b := encoded.Bytes()
	result, err := guard.Call(ctx, scope, "write", map[string]any{"workspace_id": w.ID, "path": rel, "content": string(b), "create_dirs": true, "overwrite": false})
	if err != nil {
		return storedResult{}, err
	}
	if result.IsError {
		return storedResult{}, wire.Errorf("batch_store_failed", "Unable to persist batch definition: %v", result.StructuredContent["error"])
	}
	abs, _, err := guard.ResolvePath(w, rel, true)
	if err != nil {
		return storedResult{}, err
	}
	if err = os.Chmod(abs, 0600); err != nil {
		return storedResult{}, err
	}
	pruned, err := s.prune(w, rel)
	if err != nil {
		return storedResult{}, err
	}
	return storedResult{Path: rel, Tag: tag, Pruned: pruned, GitExcluded: excluded}, nil
}

func (s *Service) maintain(ctx context.Context, w wire.Workspace, path string) (storedResult, error) {
	if s.cfg.Bool("connectionTest", false) || !autoStored(path) {
		return storedResult{Path: path, Pruned: []string{}}, nil
	}
	guard := s.fileService()
	abs, rel, err := guard.ResolvePath(w, path, true)
	if err != nil {
		return storedResult{}, err
	}
	dir, _, err := guard.ResolvePath(w, StoreDir, true)
	if err != nil {
		return storedResult{}, err
	}
	unlock := guard.LockPaths(dir, abs)
	defer unlock()
	excluded, err := ensureExclude(ctx, w, guard)
	if err != nil {
		return storedResult{}, err
	}
	now := time.UnixMilli(time.Now().UnixMilli())
	if err = os.Chtimes(abs, now, now); err != nil {
		return storedResult{}, err
	}
	pruned, err := s.prune(w, rel)
	if err != nil {
		return storedResult{}, err
	}
	return storedResult{Path: rel, Tag: strings.ToUpper(strings.TrimSuffix(filepath.Base(rel), filepath.Ext(rel))), Pruned: pruned, GitExcluded: excluded}, nil
}

func definitionError(path, message string) error {
	return batchError("batch_file_invalid", fmt.Sprintf("Invalid stored batch %s: %s", path, message))
}
