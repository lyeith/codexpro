package process

import (
	"context"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// RunTemporaryCommand starts and waits for an unstarted exec.CommandContext
// made with ctx. It owns the fresh process group, cancellation and wait;
// callers retain their environment, I/O, output limits and error mapping.
// Cancellation escalates the group from TERM to KILL after one second, and
// the escalation is joined before returning so no delayed signal outlives it.
func RunTemporaryCommand(ctx context.Context, cmd *exec.Cmd) error {
	attributes := syscall.SysProcAttr{}
	if cmd.SysProcAttr != nil {
		attributes = *cmd.SysProcAttr
	}
	attributes.Setpgid, attributes.Pgid = true, 0
	cmd.SysProcAttr = &attributes

	done := make(chan struct{})
	var escalation sync.WaitGroup
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		pid := cmd.Process.Pid
		err := syscall.Kill(-pid, syscall.SIGTERM)
		escalation.Add(1)
		go func() {
			defer escalation.Done()
			select {
			case <-done:
			case <-time.After(time.Second):
				select {
				case <-done:
					return
				default:
				}
				_ = syscall.Kill(-pid, syscall.SIGKILL)
			}
		}()
		if err == syscall.ESRCH {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.WaitDelay = 1500 * time.Millisecond
	err := cmd.Start()
	if err == nil {
		_ = syscall.Setpriority(syscall.PRIO_PROCESS, cmd.Process.Pid, 10)
		err = cmd.Wait()
	}
	// A direct exit can leave descendants holding pipes. A nonzero exit can
	// mask ErrWaitDelay, so every unsuccessful wait also drains the group.
	if cmd.Process != nil && (err != nil || ctx.Err() != nil) {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	close(done)
	// Cmd.Wait joins Cancel; no later Add can race this Wait.
	escalation.Wait()
	return err
}
