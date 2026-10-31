import copy
import hashlib
import json
import os
from pathlib import Path
import socket
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import Mock, patch

import managed_contract_suite as lane


class ManagedHarnessTests(unittest.TestCase):
    def test_exact_twelve_ordered_business_cases(self):
        self.assertEqual(len(lane.CASE_IDS), 12)
        self.assertEqual(len(set(lane.CASE_IDS)), 12)
        for index, case in enumerate(lane.CASE_IDS, 1):
            self.assertTrue(case.startswith(f"MANAGED-{index:02d}-"))

    def test_occupied_port_is_refused_without_contact_or_signal(self):
        # No network permission is required for the offline guard test.
        listener=Mock();listener.bind.side_effect=OSError(98,"Address already in use")
        with patch.object(lane.socket,"socket",return_value=listener):
            with patch.object(lane.os,"killpg") as kill:
                with self.assertRaisesRegex(lane.ContractFailure,"occupied"):
                    lane.assert_free_ports((7210,))
                kill.assert_not_called()
        listener.bind.assert_called_once_with(("127.0.0.1",7210))
        listener.connect.assert_not_called()
        listener.close.assert_called_once()

    def test_port_check_precedes_state_or_child_creation(self):
        with tempfile.TemporaryDirectory(prefix="sp6-managed-") as directory:
            base=Path(directory);release=base/"0.31.0-44bb76b";(release/"dist").mkdir(parents=True)
            (release/"package.json").write_text('{"version":"0.31.0"}')
            (release/"dist"/"http.js").write_text("// test source")
            binary=base/"candidate";binary.write_text("#!/bin/sh\nexit 0\n");binary.chmod(0o700)
            parent=base/"new-state"
            with patch.object(lane,"assert_free_ports",side_effect=lane.ContractFailure("occupied")):
                with patch.object(lane.subprocess,"Popen") as launch:
                    with self.assertRaisesRegex(lane.ContractFailure,"occupied"):
                        lane.Pair(release,binary,parent,base/"result.json")
                    launch.assert_not_called()
            self.assertFalse(parent.exists())
            self.assertFalse((base/"result.json").exists())

    def test_process_environment_drops_inherited_live_selectors(self):
        with patch.dict(os.environ,{"CODEXPRO_HTTP_TOKEN":"must-not-inherit",
            "CODEXPRO_HOME":"/forbidden","CODEX_HOME":"/forbidden","HTTP_PROXY":"http://bad.invalid",
            "GIT_CONFIG_COUNT":"99","GIT_SSH_COMMAND":"not inherited"}):
            env=lane.sanitized_environment()
        self.assertFalse(any(key.startswith("CODEXPRO_") for key in env))
        self.assertNotIn("CODEX_HOME",env)
        self.assertNotIn("HTTP_PROXY",env)
        self.assertNotIn("GIT_CONFIG_COUNT",env)
        self.assertNotIn("GIT_SSH_COMMAND",env)

    def test_worktree_parser_preserves_identity_and_rejects_duplicates(self):
        raw="worktree /private/repo\0HEAD abc\0branch refs/heads/main\0\0worktree /private/managed\0HEAD def\0branch refs/heads/work\0\0"
        self.assertEqual(lane.parse_worktrees(raw),[
            {"worktree":"/private/repo","HEAD":"abc","branch":"refs/heads/main"},
            {"worktree":"/private/managed","HEAD":"def","branch":"refs/heads/work"}])
        for value in ["worktree /a\0HEAD abc", "HEAD abc\0\0",
                      "worktree /a\0HEAD abc\0HEAD def\0\0"]:
            with self.assertRaises(lane.ContractFailure): lane.parse_worktrees(value)

    def test_lease_identity_matches_captured_source_formulas(self):
        with tempfile.TemporaryDirectory() as directory:
            root=Path(directory);repo=root/"repo";(repo/".git").mkdir(parents=True)
            managed=root/"managed";managed.mkdir()
            workspace="wt_"+"A"*32;principal="connector_fixture";key="stable-key"
            value=lane.derive_lease_identity(repo,managed,workspace,principal,key)
            sha=lambda s:hashlib.sha256(s.encode()).hexdigest()
            repository=sha(str(repo/".git")+"\0")[:24]
            self.assertEqual(value["repositoryId"],repository)
            self.assertEqual(value["branch"],"codexpro/mcp/"+sha(workspace)[:24])
            self.assertEqual(value["checkoutRoot"],str(managed/"repositories"/repository/"checkouts"/sha(workspace)[:32]))
            self.assertEqual(value["creationKeyHash"],sha("\0".join(("contract",repository,principal,key))))
            self.assertEqual(value["ownerId"],principal)
            for invalid in ["ws_"+ "a"*24,"wt_short","wt_"+"a"*31,"wt_"+"!"*32]:
                with self.assertRaises(lane.ContractFailure):
                    lane.derive_lease_identity(repo,managed,invalid,principal,key)

    @staticmethod
    def summary(mode="safe"):
        return {"body":{"result":{"content":[{"type":"text","text":"# Workspace\n\nBash mode: "+mode+"\nWrite mode: workspace\nExact other text.\n"}],
                                 "structuredContent":{"bash_mode":mode,"write_mode":"workspace","other":None}}}}

    def test_only_exact_permission_fixture_fields_are_normalized(self):
        reference=self.summary("safe");candidate=self.summary("full")
        changes=[]
        lane.normalize_permission_summary(reference,"open_workspace","safe",changes,"case")
        lane.normalize_permission_summary(candidate,"open_workspace","full",changes,"case")
        self.assertEqual(reference,candidate)
        self.assertEqual(reference["body"]["result"]["structuredContent"]["write_mode"],"workspace")
        self.assertIn("other",reference["body"]["result"]["structuredContent"])
        self.assertIsNone(reference["body"]["result"]["structuredContent"]["other"])
        self.assertEqual(len(changes),4)

    def test_unexpected_permission_or_duplicate_line_is_not_masked(self):
        for value,mode in [(self.summary("full"),"safe"),(self.summary("safe"),"full")]:
            with self.assertRaises(lane.ContractFailure):
                lane.normalize_permission_summary(value,"open_workspace",mode,[],"case")
        value=self.summary()
        value["body"]["result"]["content"][0]["text"]+="\nBash mode: safe\n"
        with self.assertRaises(lane.ContractFailure):
            lane.normalize_permission_summary(value,"open_workspace","safe",[],"case")
        value=self.summary();value["body"]["result"]["content"][0]["text"]="Bash safe"
        with self.assertRaises(lane.ContractFailure):
            lane.normalize_permission_summary(value,"open_workspace","safe",[],"case")

    def test_no_other_response_difference_is_hidden(self):
        reference=self.summary("safe");candidate=self.summary("full")
        candidate["body"]["result"]["structuredContent"]["write_mode"]="off"
        lane.normalize_permission_summary(reference,"open_workspace","safe",[],"case")
        lane.normalize_permission_summary(candidate,"open_workspace","full",[],"case")
        self.assertNotEqual(reference,candidate)
        del candidate["body"]["result"]["structuredContent"]["other"]
        self.assertNotEqual(reference,candidate)

    def test_instruction_alias_is_bound_to_process_and_exact_owned_directory(self):
        with tempfile.TemporaryDirectory(prefix="sp6-managed-alias-") as directory:
            account=Path(directory);state=account/"private"/"reference"
            codex=state/"empty-codex";codex.mkdir(parents=True)
            manifest={"pid":222,"process_start_ticks":17,"state_root":str(state)}
            roles={"codex_dir":str(codex),"account_home":str(account)}
            with patch.object(lane,"process_start_ticks",return_value=17), \
                 patch.object(lane,"process_path_binding",return_value=roles) as bind:
                aliases,path=lane.managed_instruction_aliases(manifest)
            bind.assert_called_once_with(manifest)
            self.assertEqual(path,str(codex))
            home_spelling="[home]/private/reference/empty-codex"
            self.assertEqual(aliases,{str(codex):"<private-codex-dir>",home_spelling:"<private-codex-dir>"})
            self.assertNotIn("[home]",aliases)
            value={"known_absolute":str(codex),"known_home":home_spelling,
                   "text":"Global instructions: none found in "+home_spelling+".",
                   "unknown_home":"[home]/private/reference/unexpected-codex",
                   "unknown_run":"[home]/another-run/reference/empty-codex",
                   "unknown_other":"[home]/unrelated/file.txt",
                   "unknown_absolute":str(state/"unexpected-codex"),
                   "source_label":"[codex-data]","empty":None}
            normalized=lane.replace_strings(value,aliases)
            self.assertEqual(normalized["known_absolute"],"<private-codex-dir>")
            self.assertEqual(normalized["known_home"],"<private-codex-dir>")
            self.assertEqual(normalized["text"],"Global instructions: none found in <private-codex-dir>.")
            for key in ("unknown_home","unknown_run","unknown_other","unknown_absolute","source_label","empty"):
                self.assertEqual(normalized[key],value[key])

    def test_instruction_alias_refuses_unbound_path_and_changed_process(self):
        with tempfile.TemporaryDirectory(prefix="sp6-managed-alias-") as directory:
            account=Path(directory);state=account/"private"/"reference"
            codex=state/"empty-codex";codex.mkdir(parents=True)
            other=state/"unexpected-codex";other.mkdir()
            manifest={"pid":222,"process_start_ticks":17,"state_root":str(state)}
            roles={"codex_dir":str(other),"account_home":str(account)}
            with patch.object(lane,"process_start_ticks",return_value=17), \
                 patch.object(lane,"process_path_binding",return_value=roles):
                with self.assertRaisesRegex(lane.ContractFailure,"differs from the owned"):
                    lane.managed_instruction_aliases(manifest)
            roles["codex_dir"]=str(codex)
            with patch.object(lane,"process_start_ticks",side_effect=[17,18]), \
                 patch.object(lane,"process_path_binding",return_value=roles):
                with self.assertRaisesRegex(lane.ContractFailure,"changed during"):
                    lane.managed_instruction_aliases(manifest)
            with patch.object(lane,"process_start_ticks",return_value=18), \
                 patch.object(lane,"process_path_binding") as bind:
                with self.assertRaisesRegex(lane.ContractFailure,"changed before"):
                    lane.managed_instruction_aliases(manifest)
                bind.assert_not_called()

    def recent_fixture(self):
        side=lane.ManagedSide.__new__(lane.ManagedSide)
        old={"action_id":"cpa_"+"a"*32,"sequence":2,"finished_at":"2026-10-31T01:00:00.000Z",
             "tool_name":"write","status":"success","actor_ref":"actor_fixture","workspace_id":"wt_fixture",
             "changed_paths":["managed-proof.txt"],"changed_path_count":1}
        current={**old,"action_id":"cpa_"+"b"*32,"sequence":6,"finished_at":"2026-10-31T01:00:01.000Z",
                 "tool_name":"create_workspace","changed_paths":[],"changed_path_count":0}
        side.actions={old["action_id"]:(old,"old-write"),current["action_id"]:(current,"current-create")}
        side.audit_rows=lambda project:([old,current],{})
        brief={"enabled":True,"last_recorded_project_change":lane.last_change([old]),
               "latest_recorded_activity":old["finished_at"],
               "actions":lane.recent_actions([old],"wt_fixture"),
               "latest_sequence":5,"next_sequence":5,"earliest_sequence":1,
               "has_more":False,"gap_detected":False,"coverage":lane.COVERAGE}
        text="summary\n\nLast recorded project change: "+old["finished_at"]+". "+lane.COVERAGE
        response={"body":{"result":{"structuredContent":{"workspace_id":"wt_fixture","recent_activity":brief},
                                  "content":[{"type":"text","text":text}]}}}
        return side,response,{"latest_sequence":5,"retained_from_sequence":1},{"latest_sequence":6,"retained_from_sequence":1}

    def test_recent_projection_excludes_its_own_unfinished_create(self):
        side,response,before,after=self.recent_fixture()
        result=side.normalize_recent(response,before,after)
        brief=result["body"]["result"]["structuredContent"]["recent_activity"]
        self.assertEqual(brief["latest_sequence"],5)
        self.assertEqual(brief["actions"][0]["sequence"],2)
        self.assertEqual(len(brief["actions"]),1)
        self.assertEqual(brief["latest_recorded_activity"],"<finished:old-write>")
        self.assertEqual(response["body"]["result"]["structuredContent"]["recent_activity"]["latest_recorded_activity"],
                         "2026-10-31T01:00:00.000Z")

    def test_recent_watermarks_and_incorrect_self_projection_are_not_masked(self):
        side,response,before,after=self.recent_fixture()
        with self.assertRaises(lane.ContractFailure):
            side.normalize_recent(response,before,{**after,"latest_sequence":7})
        response["body"]["result"]["structuredContent"]["recent_activity"]["latest_recorded_activity"]="2026-10-31T01:00:01.000Z"
        with self.assertRaises(lane.ContractFailure):
            side.normalize_recent(response,before,after)

    def test_same_byte_fixture_commits_are_identical_and_roots_are_isolated(self):
        with tempfile.TemporaryDirectory(prefix="sp6-managed-") as directory:
            pair=SimpleNamespace(parent=Path(directory),release=Path(directory)/"release",binary=Path(directory)/"candidate")
            reference=lane.OwnedServer(pair,"reference")
            candidate=lane.OwnedServer(pair,"candidate")
            self.assertEqual(reference.main_commit,candidate.main_commit)
            self.assertEqual(reference.alternate_commit,candidate.alternate_commit)
            self.assertNotEqual(reference.main_commit,reference.alternate_commit)
            self.assertNotEqual(reference.root,candidate.root)
            self.assertNotEqual(reference.managed,candidate.managed)
            self.assertNotEqual(reference.token.read_bytes(),candidate.token.read_bytes())
            self.assertEqual(reference.token.stat().st_mode&0o777,0o600)
            self.assertEqual(reference.environment["CODEXPRO_BASH_MODE"],"safe")
            self.assertNotIn("CODEXPRO_HTTP_TOKEN",candidate.environment)
            self.assertEqual(candidate.argv[candidate.argv.index("--bash")+1],"full")
            self.assertEqual(candidate.argv[candidate.argv.index("--worktree-root")+1],str(candidate.managed))

    def test_reused_pid_is_never_signalled(self):
        server=lane.OwnedServer.__new__(lane.OwnedServer)
        server.child=Mock(pid=222);server.child.poll.return_value=None
        server.witness=12;server.log=None
        with patch.object(lane,"process_start_ticks",return_value=13):
            with patch.object(lane.os,"killpg") as kill:
                with self.assertRaisesRegex(lane.ContractFailure,"identity changed"):
                    server.stop()
                kill.assert_not_called()
        server.child.send_signal.assert_not_called()
        server.child.kill.assert_not_called()

    def test_already_exited_server_does_not_signal_unknown_group(self):
        server=lane.OwnedServer.__new__(lane.OwnedServer)
        server.child=Mock(pid=222,returncode=0);server.child.poll.return_value=0
        server.witness=12;server.log=None
        with patch.object(lane,"process_group_members",return_value={333:44}):
            with patch.object(lane.os,"killpg") as kill:
                with self.assertRaisesRegex(lane.ContractFailure,"previously witnessed"):
                    server.stop()
                kill.assert_not_called()


if __name__=="__main__":
    unittest.main()
