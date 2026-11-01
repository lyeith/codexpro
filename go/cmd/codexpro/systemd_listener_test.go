package main

import (
	"bufio"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func clearActivation(t *testing.T) {
	t.Helper()
	for _, key := range []string{"LISTEN_PID", "LISTEN_FDS", "LISTEN_FDNAMES"} {
		t.Setenv(key, "")
		_ = os.Unsetenv(key)
	}
}

func TestSystemdListenerAbsentWithoutActivation(t *testing.T) {
	clearActivation(t)
	listener, err := systemdListener()
	if err != nil || listener != nil {
		t.Fatalf("unexpected activation without LISTEN_FDS: %v %v", listener, err)
	}
}

func TestSystemdListenerIgnoresAnotherProcessesSockets(t *testing.T) {
	clearActivation(t)
	t.Setenv("LISTEN_PID", strconv.Itoa(os.Getpid()+1))
	t.Setenv("LISTEN_FDS", "1")
	t.Setenv("LISTEN_FDNAMES", "codexpro.socket")
	listener, err := systemdListener()
	if err != nil || listener != nil {
		t.Fatalf("adopted descriptors addressed to another process: %v %v", listener, err)
	}
	for _, key := range []string{"LISTEN_PID", "LISTEN_FDS", "LISTEN_FDNAMES"} {
		if _, present := os.LookupEnv(key); present {
			t.Fatalf("%s would leak to children", key)
		}
	}
}

func TestSystemdListenerRejectsInvalidCount(t *testing.T) {
	clearActivation(t)
	t.Setenv("LISTEN_PID", strconv.Itoa(os.Getpid()))
	t.Setenv("LISTEN_FDS", "0")
	if listener, err := systemdListener(); err == nil || listener != nil {
		t.Fatalf("accepted LISTEN_FDS=0: %v %v", listener, err)
	}
}

// The activated case runs in a child process whose fd 3 is a real listening
// socket, as systemd arranges it. LISTEN_PID is only known after fork, so the
// child sets it for itself before adopting the descriptor.
func TestSystemdListenerServesInheritedSocket(t *testing.T) {
	if os.Getenv("CODEXPRO_TEST_ACTIVATED_CHILD") == "1" {
		activatedChild()
		return
	}
	clearActivation(t)
	parent, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close()
	file, err := parent.(*net.TCPListener).File()
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	cmd := exec.Command(os.Args[0], "-test.run=^TestSystemdListenerServesInheritedSocket$")
	cmd.Env = append(os.Environ(), "CODEXPRO_TEST_ACTIVATED_CHILD=1", "LISTEN_FDS=1", "LISTEN_FDNAMES=codexpro.socket")
	cmd.ExtraFiles = []*os.File{file}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	lines := bufio.NewScanner(out)
	if !lines.Scan() || lines.Text() != "adopted" {
		t.Fatalf("child did not adopt the socket: %q", lines.Text())
	}
	// Close the parent's copies so only the child can accept.
	_ = parent.Close()
	_ = file.Close()
	conn, err := net.DialTimeout("tcp", parent.Addr().String(), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	reply, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil || reply != "served-by-activated-child\n" {
		t.Fatalf("connection not served through fd 3: %q %v", reply, err)
	}
	if !lines.Scan() || lines.Text() != "clean" {
		t.Fatalf("child leaked activation state: %q", lines.Text())
	}
}

func activatedChild() {
	_ = os.Setenv("LISTEN_PID", strconv.Itoa(os.Getpid()))
	listener, err := systemdListener()
	if err != nil || listener == nil {
		os.Stdout.WriteString("not-adopted\n")
		os.Exit(1)
	}
	os.Stdout.WriteString("adopted\n")
	conn, err := listener.Accept()
	if err != nil {
		os.Exit(1)
	}
	_, _ = conn.Write([]byte("served-by-activated-child\n"))
	_ = conn.Close()
	leaked := []string{}
	for _, key := range []string{"LISTEN_PID", "LISTEN_FDS", "LISTEN_FDNAMES"} {
		if _, present := os.LookupEnv(key); present {
			leaked = append(leaked, key)
		}
	}
	// fd 3 itself must be closed; the listener holds a close-on-exec duplicate.
	// If fd 3 was reused by the runtime it must not be the activation socket.
	var listenerStat, fd3Stat syscall.Stat_t
	raw, _ := listener.(*net.TCPListener).SyscallConn()
	_ = raw.Control(func(fd uintptr) {
		_ = syscall.Fstat(int(fd), &listenerStat)
		if flags, err := fdFlags(fd); err != nil || flags&syscall.FD_CLOEXEC == 0 {
			leaked = append(leaked, "listener-without-cloexec")
		}
	})
	if err := syscall.Fstat(3, &fd3Stat); err == nil && fd3Stat.Ino == listenerStat.Ino && fd3Stat.Dev == listenerStat.Dev {
		leaked = append(leaked, "fd3")
	}
	if len(leaked) > 0 {
		os.Stdout.WriteString("leaked " + strings.Join(leaked, ",") + "\n")
		os.Exit(1)
	}
	os.Stdout.WriteString("clean\n")
	os.Exit(0)
}

func fdFlags(fd uintptr) (int, error) {
	flags, _, errno := syscall.Syscall(syscall.SYS_FCNTL, fd, syscall.F_GETFD, 0)
	if errno != 0 {
		return 0, errno
	}
	return int(flags), nil
}
