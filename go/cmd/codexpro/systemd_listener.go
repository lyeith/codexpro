package main

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"syscall"
)

// sdListenFdsStart is the first descriptor systemd passes (SD_LISTEN_FDS_START).
const sdListenFdsStart = 3

// systemdListener adopts the listening socket passed by systemd socket
// activation (sd_listen_fds protocol): LISTEN_PID names this process and
// LISTEN_FDS is at least one, with the socket at fd 3. It returns nil when this
// process was not socket-activated, so the caller binds its configured address.
//
// The activation variables are always removed so that detached runners and
// tool commands never mistake themselves for the activated process, and the
// inherited descriptors are not leaked to children: net.FileListener duplicates
// fd 3 with close-on-exec and the original is closed.
func systemdListener() (net.Listener, error) {
	pid, fds := os.Getenv("LISTEN_PID"), os.Getenv("LISTEN_FDS")
	for _, key := range []string{"LISTEN_PID", "LISTEN_FDS", "LISTEN_FDNAMES"} {
		_ = os.Unsetenv(key)
	}
	if pid == "" || fds == "" {
		return nil, nil
	}
	if owner, err := strconv.Atoi(pid); err != nil || owner != os.Getpid() {
		return nil, nil
	}
	count, err := strconv.Atoi(fds)
	if err != nil || count < 1 {
		return nil, fmt.Errorf("invalid LISTEN_FDS %q for socket activation", fds)
	}
	// Only fd 3 is served; any further passed descriptors stay out of children.
	for fd := sdListenFdsStart + 1; fd < sdListenFdsStart+count; fd++ {
		syscall.CloseOnExec(fd)
	}
	file := os.NewFile(uintptr(sdListenFdsStart), "systemd-socket")
	if file == nil {
		return nil, fmt.Errorf("socket activation descriptor %d is not open", sdListenFdsStart)
	}
	defer file.Close()
	listener, err := net.FileListener(file)
	if err != nil {
		return nil, fmt.Errorf("socket activation descriptor %d is not a listening socket: %w", sdListenFdsStart, err)
	}
	return listener, nil
}
