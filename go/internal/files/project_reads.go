package files

import (
	"bytes"
	"context"
	"golang.org/x/sys/unix"
	"io"
	"os"
	"path/filepath"

	"strings"

	"github.com/lyeith/codexpro/go/internal/wire"
)

func unavailable() error {
	return wire.Errorf("path_unavailable", "The requested project path is unavailable.")
}

func (s *Service) privateReadPath(w wire.Workspace, rel string) bool {
	abs := filepath.Join(w.Root, rel)
	policyDir := s.Config.String("principalsFile", "")
	if policyDir != "" {
		policyDir = filepath.Dir(policyDir)
	}
	for _, path := range []string{policyDir, s.Config.Home, s.Config.TokenFile, s.Config.EnvFile, s.Config.ProjectsFile,
		s.Config.String("principalsFile", ""), s.Config.String("codexDir", ""), filepath.Join(w.Root, s.Config.String("contextDir", ".ai-bridge"))} {
		if path != "" && Within(path, w.Root) && Within(abs, path) {
			return true
		}
	}
	return s.IsBlocked(rel)
}

// Keep the checked directory and file handles pinned during a personal read.
// Refusing links, nested repositories and private service files also prevents a
// granted repository from aliasing an otherwise blocked name inside its root.
func (s *Service) openProjectRead(w wire.Workspace, input string) (*os.File, string, error) {
	if input == "" {
		input = "."
	}
	if !filepath.IsLocal(input) || strings.ContainsRune(input, '\x00') {
		return nil, "", unavailable()
	}
	for _, part := range strings.Split(filepath.ToSlash(input), "/") {
		if part != "." && strings.HasPrefix(part, ".") {
			return nil, "", unavailable()
		}
	}
	abs, rel, err := s.ResolvePath(w, input, false)
	if err != nil || !filepath.IsAbs(w.Root) {
		return nil, "", unavailable()
	}
	for part := rel; part != "."; part = filepath.Dir(part) {
		if s.privateReadPath(w, part) {
			return nil, "", unavailable()
		}
	}
	// The catalog root's parents can also move. Pin each component from /,
	// refusing links throughout, including above the workspace itself.
	const flags = unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK
	fd, err := unix.Open("/", flags|unix.O_DIRECTORY, 0)
	if err != nil {
		return nil, "", unavailable()
	}
	parts := strings.Split(strings.TrimPrefix(abs, "/"), "/")
	rootParts := len(strings.Split(strings.Trim(w.Root, "/"), "/"))
	for i, name := range parts {
		var st unix.Stat_t
		mode := flags
		if i < len(parts)-1 {
			mode |= unix.O_DIRECTORY
		}
		if unix.Fstatat(fd, name, &st, unix.AT_SYMLINK_NOFOLLOW) != nil || (st.Mode&unix.S_IFMT != unix.S_IFREG && st.Mode&unix.S_IFMT != unix.S_IFDIR) {
			unix.Close(fd)
			return nil, "", unavailable()
		}
		next, err := unix.Openat(fd, name, mode, 0)
		unix.Close(fd)
		if err != nil {
			return nil, "", unavailable()
		}
		fd = next
		if unix.Fstat(fd, &st) != nil {
			unix.Close(fd)
			return nil, "", unavailable()
		}
		switch st.Mode & unix.S_IFMT {
		case unix.S_IFDIR:
			if i >= rootParts && unix.Fstatat(fd, ".git", &st, unix.AT_SYMLINK_NOFOLLOW) != unix.ENOENT {
				unix.Close(fd)
				return nil, "", unavailable()
			}
		case unix.S_IFREG:
			if i != len(parts)-1 || st.Nlink != 1 {
				unix.Close(fd)
				return nil, "", unavailable()
			}
		default:
			unix.Close(fd)
			return nil, "", unavailable()
		}
	}
	return os.NewFile(uintptr(fd), rel), rel, nil
}

func (s *Service) ReadProjectFile(w wire.Workspace, path string, limit int, binary bool) ([]byte, string, error) {
	f, rel, err := s.openProjectRead(w, path)
	if err != nil {
		return nil, "", err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Size() > int64(limit) {
		return nil, "", unavailable()
	}
	b, err := io.ReadAll(io.LimitReader(f, int64(limit)+1))
	if err != nil || len(b) > limit || !binary && bytes.IndexByte(b, 0) >= 0 {
		return nil, "", unavailable()
	}
	return b, rel, nil
}

func (s *Service) projectDir(ctx context.Context, w wire.Workspace, path string) ([]os.DirEntry, error) {
	f, rel, err := s.openProjectRead(w, path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	entries, err := f.ReadDir(20001)
	if err != nil && err != io.EOF || len(entries) > 20000 {
		return nil, unavailable()
	}
	visible := entries[:0]
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		child := filepath.Join(rel, entry.Name())
		if strings.HasPrefix(entry.Name(), ".") || s.privateReadPath(w, child) || !entry.IsDir() && !entry.Type().IsRegular() {
			continue
		}
		if entry.IsDir() {
			d, _, err := s.openProjectRead(w, child)
			if err != nil {
				continue
			}
			d.Close()
		} else {
			var st unix.Stat_t
			if unix.Fstatat(int(f.Fd()), entry.Name(), &st, unix.AT_SYMLINK_NOFOLLOW) != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Nlink != 1 {
				continue
			}
		}
		visible = append(visible, entry)
	}
	return visible, nil
}

// ProjectFiles supplies the bounded fixed-string search with only ordinary
// files visible to the same reader as the tree and read tools.
func (s *Service) ProjectFiles(ctx context.Context, w wire.Workspace, path string, limit int) ([]string, error) {
	f, rel, err := s.openProjectRead(w, path)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	f.Close()
	if err != nil {
		return nil, unavailable()
	}
	if st.Mode().IsRegular() {
		return []string{rel}, nil
	}
	var paths []string
	remaining := limit
	var walk func(string) error
	walk = func(dir string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		entries, err := s.projectDir(ctx, w, dir)
		if err != nil {
			return err
		}
		remaining -= len(entries) + 1
		if remaining < 0 {
			return wire.Errorf("search_budget", "Narrow the project search path.")
		}
		for _, entry := range entries {
			child := filepath.Join(dir, entry.Name())
			if entry.IsDir() {
				if err := walk(child); err != nil {
					return err
				}
			} else {
				paths = append(paths, child)
			}
		}
		return nil
	}
	if err := walk(rel); err != nil {
		return nil, err
	}
	return paths, nil
}
