package process

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"sync/atomic"
	"syscall"
)

const (
	TemporaryCommandStarted  = "started"
	TemporaryCommandFinished = "finished"

	TemporaryQuiescenceProcessGroup = "process_group"
	TemporaryQuiescenceNotLaunched  = "not_launched"
)

// TemporaryCommandObservation describes only this command's process group.
// It is never an SSD allocation, cgroup, or durable owner-settlement proof.
// Unknown identity or quiescence must remain an outstanding observation.
type TemporaryCommandObservation struct {
	CommandID        uint64 `json:"command_id"`
	Phase            string `json:"phase"`
	PID              int    `json:"pid"`
	ProcessIdentity  string `json:"process_identity"`
	BootID           string `json:"boot_id"`
	ExitCode         *int   `json:"exit_code"`
	CommandError     string `json:"command_error"`
	QuiescenceScope  string `json:"quiescence_scope"`
	Quiescent        bool   `json:"quiescent"`
	QuiescenceKnown  bool   `json:"quiescence_known"`
	ObservationError string `json:"observation_error"`
}

type temporaryCommandObserverKey struct{}

var temporaryCommandIDs atomic.Uint64

// WithTemporaryCommandObserver opts RunTemporaryCommand into two synchronous
// snapshots with one server-local command ID, including failed start attempts.
// The observer must return promptly. Its panic cannot replace the command's
// result; an undelivered/unknown snapshot is not evidence of quiescence.
func WithTemporaryCommandObserver(ctx context.Context, observer func(TemporaryCommandObservation)) context.Context {
	return context.WithValue(ctx, temporaryCommandObserverKey{}, observer)
}

type temporaryCommandObservation struct {
	observer func(TemporaryCommandObservation)
	value    TemporaryCommandObservation
}

// This is called after the original Start/priority operations. With no observer
// it performs no identity lookup, process-group probe, or command-ID allocation.
func observeTemporaryCommandStart(ctx context.Context, cmd *exec.Cmd, startErr error) *temporaryCommandObservation {
	observer, _ := ctx.Value(temporaryCommandObserverKey{}).(func(TemporaryCommandObservation))
	if observer == nil {
		return nil
	}
	state := &temporaryCommandObservation{
		observer: observer,
		value: TemporaryCommandObservation{
			CommandID: temporaryCommandIDs.Add(1), Phase: TemporaryCommandStarted,
			QuiescenceScope: TemporaryQuiescenceProcessGroup,
		},
	}
	if startErr != nil {
		state.value.CommandError = startErr.Error()
	}
	if cmd.Process == nil && startErr != nil {
		state.value.QuiescenceScope = TemporaryQuiescenceNotLaunched
	} else if cmd.Process == nil || startErr != nil {
		state.value.ObservationError = "Temporary command launch identity was unavailable."
	} else {
		state.value.PID = cmd.Process.Pid
		bootID, bootErr := BootID()
		state.value.BootID = bootID
		fields, err := procFields(cmd.Process.Pid)
		if bootErr != nil || err != nil || !observationBootPattern.MatchString(state.value.BootID) {
			state.value.ObservationError = "Temporary command process identity was unavailable."
		} else {
			pgid, groupErr := strconv.Atoi(fields[2])
			start, identityErr := strconv.ParseUint(fields[19], 10, 64)
			if groupErr != nil || pgid != cmd.Process.Pid || identityErr != nil || start == 0 {
				state.value.ObservationError = "Temporary command process-group identity was invalid."
			} else {
				state.value.ProcessIdentity = fields[19]
			}
		}
	}
	state.emit()
	return state
}

func (state *temporaryCommandObservation) finish(cmd *exec.Cmd, commandErr error) {
	if state == nil {
		return
	}
	state.value.Phase = TemporaryCommandFinished
	state.value.CommandError = ""
	if commandErr != nil {
		state.value.CommandError = commandErr.Error()
	}
	if cmd.ProcessState != nil {
		exitCode := cmd.ProcessState.ExitCode()
		state.value.ExitCode = &exitCode
	}
	if state.value.ObservationError == "" {
		state.value, _ = ObserveTemporaryCommandQuiescence(state.value)
	}
	state.emit()
}

// ObserveTemporaryCommandQuiescence refreshes only an original finished
// observation. It does not rerun the command or settle any SSD owner. Callers
// must retain and reconcile the original command identity around this read.
func ObserveTemporaryCommandQuiescence(observation TemporaryCommandObservation) (TemporaryCommandObservation, error) {
	out := observation
	out.QuiescenceKnown, out.Quiescent, out.ObservationError = false, false, ""
	fail := func(message string) (TemporaryCommandObservation, error) {
		out.ObservationError = message
		return out, errors.New(message)
	}
	if out.CommandID == 0 || out.Phase != TemporaryCommandFinished {
		return fail("Temporary command terminal observation was unverified.")
	}
	if out.QuiescenceScope == TemporaryQuiescenceNotLaunched {
		if out.PID != 0 || out.ProcessIdentity != "" || out.BootID != "" ||
			out.ExitCode != nil || out.CommandError == "" {
			return fail("Temporary command unlaunched observation was contradictory.")
		}
		out.QuiescenceKnown, out.Quiescent = true, true
		return out, nil
	}
	start, identityErr := strconv.ParseUint(out.ProcessIdentity, 10, 64)
	if out.QuiescenceScope != TemporaryQuiescenceProcessGroup || out.PID <= 0 ||
		identityErr != nil || start == 0 || !observationBootPattern.MatchString(out.BootID) {
		return fail("Temporary command original process identity was unverified.")
	}
	currentBoot, bootErr := BootID()
	if bootErr != nil || currentBoot != out.BootID {
		return fail("Temporary command original boot could not be verified.")
	}
	if err := verifyTemporaryCommandLeader(out); err != nil {
		return fail(err.Error())
	}
	// Signal zero delivers no signal. ESRCH proves the group absent without a
	// racy member enumeration; a present group, including zombies, stays pending.
	probeErr := syscall.Kill(-out.PID, 0)
	if err := verifyTemporaryCommandLeader(out); err != nil {
		return fail(err.Error())
	}
	switch probeErr {
	case syscall.ESRCH:
		out.QuiescenceKnown, out.Quiescent = true, true
	case nil, syscall.EPERM:
		out.QuiescenceKnown, out.Quiescent = true, false
	default:
		return fail("Temporary command process-group observation failed.")
	}
	return out, nil
}

func verifyTemporaryCommandLeader(observation TemporaryCommandObservation) error {
	fields, err := procFields(observation.PID)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return errors.New("Temporary command original leader could not be observed.")
	}
	pgid, err := strconv.Atoi(fields[2])
	if err != nil || pgid != observation.PID || fields[19] != observation.ProcessIdentity {
		return errors.New("Temporary command original leader identity changed.")
	}
	return nil
}

func (state *temporaryCommandObservation) emit() {
	value := state.value
	if value.ExitCode != nil {
		exitCode := *value.ExitCode
		value.ExitCode = &exitCode
	}
	if !callTemporaryCommandObserver(state.observer, value) {
		state.value.ObservationError = "Temporary command observer did not complete."
		state.value.QuiescenceKnown, state.value.Quiescent = false, false
	}
}

func callTemporaryCommandObserver(observer func(TemporaryCommandObservation), value TemporaryCommandObservation) (completed bool) {
	defer func() { _ = recover() }()
	observer(value)
	return true
}
