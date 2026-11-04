//go:build unix

package plansaccess

import (
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// Open each component relative to its already-open parent. The final private
// directory and all parent components must remain under trusted ownership.
func privateDir(dir string) (int, error) {
	if !filepath.IsAbs(dir) || dir != filepath.Clean(dir) || dir == "/" {
		return -1, errUnavailable
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY, 0)
	if err != nil {
		return -1, errUnavailable
	}
	parts := strings.Split(dir[1:], "/")
	for i, name := range parts {
		next, e := unix.Openat(fd, name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_DIRECTORY, 0)
		_ = unix.Close(fd)
		if e != nil {
			return -1, errUnavailable
		}
		fd = next
		var st unix.Stat_t
		// A sticky system temporary directory may hold a private, owned test
		// directory. Every other ancestor must exclude untrusted replacement.
		if unix.Fstat(fd, &st) != nil || (st.Uid != uint32(os.Geteuid()) && st.Uid != 0) ||
			(st.Mode&0022 != 0 && !(st.Uid == 0 && st.Mode&unix.S_ISVTX != 0)) ||
			(i == len(parts)-1 && (st.Uid != uint32(os.Geteuid()) || st.Mode&0077 != 0)) {
			_ = unix.Close(fd)
			return -1, errUnavailable
		}
	}
	return fd, nil
}

func privateFD(dir int, name string, flags int) (*os.File, error) {
	fd, err := unix.Openat(dir, name, flags|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0600)
	if err != nil {
		return nil, err
	}
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || st.Uid != uint32(os.Geteuid()) || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Mode&0777 != 0600 || st.Nlink != 1 {
		_ = unix.Close(fd)
		return nil, errUnavailable
	}
	return os.NewFile(uintptr(fd), name), nil
}

func readAt(dir int, name string) (*Policy, error) {
	f, err := privateFD(dir, name, unix.O_RDONLY)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxSize+1))
	if err != nil {
		return nil, errUnavailable
	}
	return parse(data)
}

// Load validates the complete current policy on each authorization. An error
// means all policy-backed access is unavailable, including any legacy bridge.
func Load(file string) (*Policy, error) {
	if file != filepath.Clean(file) {
		return nil, errUnavailable
	}
	dir, err := privateDir(filepath.Dir(file))
	if err != nil {
		return nil, errUnavailable
	}
	defer unix.Close(dir)
	p, err := readAt(dir, filepath.Base(file))
	if err != nil {
		return nil, errUnavailable
	}
	return p, nil
}
