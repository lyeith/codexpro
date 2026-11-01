#!/usr/bin/env python3
"""Adjudicate only saved census/preflight evidence; perform no live observations."""
from pathlib import Path
import datetime
import hashlib
import json
import os
import stat

BASE = Path(__file__).resolve().parent / "evidence"
CENSUS_SHA = "ecd6f6a61f0570febd641931e368f515435b415632280f838802de0215b0f232"
PREFLIGHT = "startup-loaded-4-preflight.json"
SOURCE_LABEL = "evidence/" + PREFLIGHT


def saved(name):
    path = BASE / name
    fd = os.open(path, os.O_RDONLY | os.O_NOATIME | os.O_NOFOLLOW | os.O_CLOEXEC)
    try:
        before = os.fstat(fd)
        assert stat.S_ISREG(before.st_mode) and before.st_size <= 16 * 1024 * 1024
        with os.fdopen(fd, "rb", closefd=False) as source:
            raw = source.read(16 * 1024 * 1024 + 1)
        after = os.fstat(fd)
        signature = lambda v: (v.st_dev, v.st_ino, v.st_size, v.st_mtime_ns, v.st_ctime_ns)
        assert signature(before) == signature(after) == signature(path.stat())
        assert len(raw) == before.st_size
        return json.loads(raw), hashlib.sha256(raw).hexdigest()
    finally:
        os.close(fd)


def main():
    output = BASE / "final-readonly-census-adjudication.json"
    assert not os.path.lexists(output), "Refuse overwriting an adjudication"
    census, census_sha = saved("final-readonly-census.json")
    assert census_sha == CENSUS_SHA
    preflight, preflight_sha = saved(PREFLIGHT)
    saved_read = census["private_proofs"]["selected_evidence"][PREFLIGHT]["read"]
    assert saved_read["stable_window"] and saved_read["window_sha256"] == preflight_sha
    assert preflight["kind"] == "five-sample-live-health-preflight"
    assert preflight["load_workers_launched"] == 0 and preflight["passed"]
    classifications = {}
    allowed = {"exact-systemd-user-init-scope", "sd-pam-child-of-exact-user-systemd",
               "user-sshd-session-with-root-parent"}
    for key in ("admission_before", "admission_after"):
        admission = preflight[key]
        assert admission["safe"] and not admission["errors"] and not admission["blocking_processes"]
        assert not admission["landing_worktrees"]
        for item in admission["verified_user_supervisors_without_cwd"]:
            assert item["classification"] in allowed
            identity = (item["pid"], item["start_ticks"])
            assert classifications.get(identity, item["classification"]) == item["classification"]
            classifications[identity] = item["classification"]
    assert len(classifications) == 4
    excluded, retained = [], []
    for observation in census["private_proofs"]["process_observations"]:
        witness = observation["receipt_witness"]
        identity = (witness["pid"], witness["start_ticks"])
        if witness["sources"] == [SOURCE_LABEL] and identity in classifications:
            excluded.append({"pid": identity[0], "start_ticks": identity[1],
                             "classification": classifications[identity], "source": SOURCE_LABEL,
                             "original_present": observation.get("present"),
                             "original_identity_matches": observation.get("identity_matches"),
                             "reason": "Observed host supervisor; no private launch witness"})
        else:
            retained.append(observation)
    assert len(excluded) == 4 and len(retained) == 154
    assert all(row.get("present") is False and row.get("identity_matches") is False
               for row in retained), "Preserve any real private survivor or unknown"
    original_flags = census["open_reasons"]
    assert set(original_flags) == {"private_process_observation_incomplete",
                                   "known_private_process_identity_still_present"}
    result = {
        "schema_version": 1,
        "kind": "saved-census-process-scope-adjudication",
        "recorded_at": datetime.datetime.now(datetime.timezone.utc).isoformat(),
        "original_census_sha256": census_sha,
        "original_census_started_at": census["started_at"],
        "original_census_finished_at": census["finished_at"],
        "original_census_open_reasons": original_flags,
        "original_census_preserved": True,
        "source_preflight_sha256": preflight_sha,
        "source_preflight_matches_census_hash": True,
        "excluded_observational_supervisors": excluded,
        "retained_private_identity_observations": len(retained),
        "retained_private_identities_absent": len(retained),
        "retained_matching_live_identity_count": 0,
        "retained_unknown_process_observation_count": 0,
        "remaining_census_open_reasons": [],
        "census_observations_complete_after_scope_adjudication": True,
        "fresh_global_census_rerun": False,
        "live_process_or_state_accessed": False,
        "network_requests": 0,
        "process_control_actions": [],
        "limits": [
            "This corrects process-role aggregation using the original observations, not their values.",
            "The census remains a sequential bounded snapshot, not a global transactional inventory.",
            "Absent recorded identities do not prove all historical descendants quiescent; runtime cleanup receipts remain separate.",
            "The original optional socket-table permission limits and IPv4-only endpoint checks remain.",
            "This does not close the guarded loaded-startup gate, which remains OPEN."
        ]
    }
    with output.open("x") as handle:
        json.dump(result, handle, indent=2)
        handle.write("\n")
        handle.flush()
        os.fsync(handle.fileno())
    print(json.dumps({"output": str(output), "excluded_observational_supervisors": len(excluded),
                      "private_identities_absent": len(retained),
                      "census_observations_complete_after_scope_adjudication": True}))


if __name__ == "__main__":
    main()
