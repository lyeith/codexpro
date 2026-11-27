package process

import (
	"encoding/json"
	"errors"
	"strings"
)

func validateGuardedSettlement(data []byte, claim ResourceClaim) error {
	fields, err := exactObject(data, []string{"run_id", "status", "quiescent", "cancelled"}, nil)
	if err != nil {
		return err
	}
	var quiet, cancelled *bool
	var run, status string
	if json.Unmarshal(fields["run_id"], &run) != nil || run != claim.RunID ||
		json.Unmarshal(fields["status"], &status) != nil ||
		json.Unmarshal(fields["quiescent"], &quiet) != nil || quiet == nil || !*quiet ||
		json.Unmarshal(fields["cancelled"], &cancelled) != nil || cancelled == nil {
		return errors.New("guarded resource settlement was incomplete")
	}
	switch status {
	case "cancelled":
		if *cancelled {
			return nil
		}
	case "succeeded", "failed", "abandoned":
		if !*cancelled {
			return nil
		}
	}
	return errors.New("guarded resource settlement was not terminal")
}

func validateGuardedPreparedInspection(data []byte) error {
	var in resourceInspection
	if err := decodeResourceInspection(data, &in); err != nil {
		return err
	}
	r := in.Receipt
	if r == nil || r.Owner == nil || r.Status != "created" || r.ExitCode != nil ||
		r.InvocationID != "" || r.Owner.InvocationID != "" ||
		(len(r.Owner.Cgroup) != 0 && strings.TrimSpace(string(r.Owner.Cgroup)) != "null") || in.Pending == nil || !*in.Pending ||
		in.Alive == nil || *in.Alive || in.Quiescent == nil || *in.Quiescent ||
		in.AllocationClaim != "reserved" || len(in.UnitState) != 0 {
		return errors.New("guarded activation requires its original unstarted pending receipt")
	}
	return nil
}
