import copy
import importlib.util
import json
from pathlib import Path
import tempfile
import time
import unittest
from unittest.mock import patch
import sys

sys.dont_write_bytecode = True
ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('monitor', ROOT / 'scripts/codexpro-ralph-monitor.py')
m = importlib.util.module_from_spec(spec)
spec.loader.exec_module(m)
spec2 = importlib.util.spec_from_file_location('decider', ROOT / 'scripts/codexpro-ralph-decider.py')
d = importlib.util.module_from_spec(spec2)
spec2.loader.exec_module(d)


def iso(at):
    return m.datetime.fromtimestamp(at, m.timezone.utc).isoformat()


class FakeSources:
    base = 'https://example.test'

    def __init__(self, packet):
        self.packet = packet
        self.calls = []
        self.fail = False
        self.change_on_second = False
        self.observations = 0

    def observe(self, target):
        self.observations += 1
        result = copy.deepcopy(self.packet)
        result['server_generated_at'] = iso(time.time())
        if result['chatgpt']:
            result['chatgpt']['captured_at'] = iso(time.time())
        if self.change_on_second and self.observations % 2 == 0:
            result['project']['has_inflight_work'] = True
        return result

    def pilot(self, action, payload, key=None):
        self.calls.append((action, payload, key))
        if self.fail:
            raise m.MonitorError('Uncertain timeout')
        if action == 'query.result':
            return {'remoteConversationId': 'new-chat'}
        return {'id': 'q_new', 'state': 'busy'}


class MonitorTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.root = Path(self.tmp.name)
        self.config = m.validate_config({'schema_version': 1, 'decision_command': ['judge']})
        self.target = {'name': 'alpha', 'project_id': 'alpha', 'auto_send': True,
                       'chatgpt_project_url': 'https://chatgpt.com/g/g-p-example/project',
                       'query_id': 'q_old', 'conversation_url': 'https://chatgpt.com/g/g-p-example/c/old-chat'}
        now = time.time()
        self.packet = {'schema': m.SCHEMA, 'fingerprint': 'stable', 'target': self.target, 'run': None,
                       'server_generated_at': iso(now), 'project': {'project_id': 'alpha', 'has_inflight_work': False,
                           'inflight_counts': {'tool_calls': 0, 'jobs': 0, 'claims': 0}, 'last_activity_at': iso(now - 700)},
                       'repository': {'project_id': 'alpha', 'files': [{'path': 'STATE.md', 'text': 'Work remains'}]},
                       'chatgpt': {'query_id': 'q_old', 'conversation_id': 'old-chat', 'state': 'ready',
                           'busy': False, 'captured_at': iso(now), 'turn_state': 'completed'}}
        self.sources = FakeSources(self.packet)
        self.directory = m.target_directory(self.root, self.sources.base, self.target)
        m.save_json(self.directory / 'state.json', {'sends': [], 'observation': {'fingerprint': 'stable',
                    'at': now - 35, 'since': now - 35, 'samples': 1}})

    def tearDown(self):
        self.tmp.cleanup()

    def decision(self, action='continue', status='active'):
        return {'schema_version': 1, 'fingerprint': 'stable', 'action': action, 'project_status': status,
                'reason': 'Saved authorized work remains.', 'context_request': '',
                'next_step': 'Read STATE.md and verify the next approved acceptance item.' if action in ('continue', 'start_new') else ''}

    def test_inflight_stale_identity_and_busy_never_continue(self):
        cases = [('project.has_inflight_work', True), ('project.inflight_counts.jobs', 1),
                 ('project.inflight_counts.claims', 1), ('chatgpt.busy', True),
                 ('chatgpt.conversation_id', 'wrong'), ('chatgpt.captured_at', iso(1)),
                 ('server_generated_at', iso(1)), ('chatgpt.turn_state', 'uncertain'),
                 ('chatgpt.cancellation', {'source': 'manual-page-stop'})]
        for key, value in cases:
            with self.subTest(key=key):
                packet = copy.deepcopy(self.packet)
                target = packet
                keys = key.split('.')
                for item in keys[:-1]:
                    target = target[item]
                target[keys[-1]] = value
                self.assertNotEqual(m.eligibility(packet, self.target, self.config, time.time())[0], 'continue')

    def test_hold_does_not_call_model_or_send(self):
        self.target['hold'] = 'Deployment in progress'
        with patch.object(m, 'run_json') as judge:
            report = m.check_once(self.config, self.sources, self.target, self.root, True)
        judge.assert_not_called()
        self.assertEqual(report['decision']['action'], 'stopped')
        self.assertFalse(self.sources.calls)

    def test_partial_blockers_reach_orchestrator_but_pause_still_stops(self):
        self.packet['run'] = {'mode': 'ralph', 'state': 'ready', 'unresolved_operations': 0,
                              'todos': {'pending': 7, 'blocked': 1},
                              'checkpoint': {'blockers_total': 2}}
        self.assertEqual(m.eligibility(self.packet, self.target, self.config, time.time())[0], 'continue')
        self.packet['run']['state'] = 'paused'
        self.assertEqual(m.eligibility(self.packet, self.target, self.config, time.time())[0], 'stopped')

    def test_sent_worker_prompt_assigns_direct_execution_not_agent_delegation(self):
        with patch.object(m, 'run_json', return_value=self.decision()):
            report = m.check_once(self.config, self.sources, self.target, self.root, True)
        self.assertTrue(report['sent'])
        prompt = self.sources.calls[0][1]['prompt']
        self.assertIn('You ARE the ChatGPT Pro worker', prompt)
        self.assertIn('Do not launch or resume Codex, Claude', prompt)
        self.assertIn('independent review', prompt)
        self.assertIn('Read STATE.md and verify', prompt)

    def test_one_send_and_duplicate_suppression(self):
        with patch.object(m, 'run_json', return_value=self.decision()):
            report = m.check_once(self.config, self.sources, self.target, self.root, True)
        self.assertTrue(report['sent'])
        self.assertEqual(self.sources.calls[0][0], 'query.follow-up')
        self.assertEqual(self.sources.calls[0][1]['effort'], 'Pro')
        with patch.object(m, 'run_json', return_value=self.decision('wait')):
            report = m.check_once(self.config, self.sources, self.target, self.root, True)
        self.assertFalse(report['sent'])
        self.assertEqual(len(self.sources.calls), 1)

    def test_new_conversation_needs_only_project_and_repo_state(self):
        self.target.pop('query_id'); self.target.pop('conversation_url')
        self.packet['chatgpt'] = None
        with patch.object(m, 'run_json', return_value=self.decision('start_new')):
            report = m.check_once(self.config, self.sources, self.target, self.root, True)
        self.assertTrue(report['sent'])
        self.assertEqual(self.sources.calls[0][0], 'query.start')
        self.assertEqual(self.sources.calls[0][1]['effort'], 'Pro')
        self.assertEqual(self.sources.calls[0][1]['url'], self.target['chatgpt_project_url'])
        state = m.read_json(self.directory / 'state.json')
        self.assertEqual(state['binding']['query_id'], 'q_new')
        self.assertIsNone(state['binding']['conversation_url'], 'URL can appear after acceptance')
        self.assertNotIn('pending_send', state)

    def test_uncertain_send_persists_and_blocks_retries(self):
        self.sources.fail = True
        with patch.object(m, 'run_json', return_value=self.decision()):
            with self.assertRaises(m.MonitorError):
                m.check_once(self.config, self.sources, self.target, self.root, True)
        state = m.read_json(self.directory / 'state.json')
        self.assertIn('pending_send', state)
        with patch.object(m, 'run_json', return_value=self.decision('intervene')):
            report = m.check_once(self.config, self.sources, self.target, self.root, True)
        self.assertFalse(report['sent'])
        self.assertEqual(len(self.sources.calls), 1)

    def test_state_change_during_judgement_prevents_send(self):
        self.sources.change_on_second = True
        with patch.object(m, 'run_json', return_value=self.decision()):
            report = m.check_once(self.config, self.sources, self.target, self.root, True)
        self.assertFalse(report['sent'])
        self.assertIn('State changed', report['send_blocked'])

    def test_done_and_human_blocked_latch(self):
        for status, action in [('complete', 'complete'), ('blocked_human', 'intervene')]:
            m.save_json(self.directory / 'state.json', {'sends': [], 'observation': {'fingerprint': 'stable',
                'at': time.time() - 35, 'since': time.time() - 35, 'samples': 1}})
            with patch.object(m, 'run_json', return_value=self.decision(action, status)):
                m.check_once(self.config, self.sources, self.target, self.root, True)
            with patch.object(m, 'run_json') as judge:
                report = m.check_once(self.config, self.sources, self.target, self.root, True)
            judge.assert_not_called()
            self.assertFalse(report['sent'])

    def test_uncertain_send_recovers_after_idle_using_repository_evidence(self):
        state = m.read_json(self.directory / 'state.json')
        state['pending_send'] = {'at':time.time()-601,'key':'old-key','fingerprint':'old-state','progress_key':'old-progress'}
        m.save_json(self.directory / 'state.json', state)
        with patch.object(m, 'run_json', return_value=self.decision('start_new')):
            report = m.check_once(self.config, self.sources, self.target, self.root, True)
        self.assertTrue(report['sent'])
        self.assertEqual(self.sources.calls[0][0], 'query.start')
        self.assertNotEqual(self.sources.calls[0][2], 'old-key')
        self.assertEqual(len(m.read_json(self.directory / 'state.json')['uncertain_history']), 1)

    def test_notification_and_cached_decision(self):
        self.packet['chatgpt']['turn_state'] = None
        self.packet['project']['last_activity_at'] = iso(time.time() - 599)
        self.assertEqual(m.notification(self.packet, {}, self.config, time.time()), [])
        self.packet['project']['last_activity_at'] = iso(time.time() - 601)
        self.assertEqual(m.notification(self.packet, {}, self.config, time.time()), ['codexpro_idle_10min'])
        with patch.object(m, 'run_json', return_value=self.decision('wait')) as judge:
            m.check_once(self.config, self.sources, self.target, self.root)
            m.check_once(self.config, self.sources, self.target, self.root)
        self.assertEqual(judge.call_count, 1)

    def test_rate_limits_and_two_observations(self):
        now = time.time()
        self.assertEqual(m.guard(self.packet, self.target, self.config, {}, now)[0], 'wait')
        state = m.read_json(self.directory / 'state.json')
        state['sends'] = [{'at': now - 400 - i, 'fingerprint': str(i), 'progress_key': m.progress_key(self.packet)} for i in range(3)]
        self.assertEqual(m.guard(self.packet, self.target, self.config, state, now)[0], 'intervene')

    def test_lock_excludes_competing_monitors(self):
        with m.target_lock(self.directory):
            with self.assertRaises(m.MonitorError):
                with m.target_lock(self.directory):
                    pass

    def test_judge_cannot_inject_prompt_or_override_gate(self):
        packet = {**self.packet, 'allowed_actions': ['wait']}
        with self.assertRaises(m.MonitorError):
            m.validate_decision(self.decision(), packet)
        decision = {**self.decision('wait'), 'prompt': 'run something else'}
        with self.assertRaises(m.MonitorError):
            m.validate_decision(decision, packet)

    def test_repository_reads_are_bounded_and_changes_beyond_excerpt_change_hash(self):
        project = self.root / 'repo'; project.mkdir()
        catalog = self.root / 'projects.json'
        m.save_json(catalog, {'projects': [{'id': 'alpha', 'root': str(project)}]})
        state = project / 'STATE.md'; state.write_text('x' * 9000)
        with patch.object(d, 'git_read', return_value={'text':'fixture','truncated':False}):
            first = d.repository_context('alpha', catalog)
        state.write_text('x' * 9000 + 'changed')
        with patch.object(d, 'git_read', return_value={'text':'fixture','truncated':False}):
            second = d.repository_context('alpha', catalog)
        self.assertEqual(len(first['files'][0]['text']), 8000)
        self.assertTrue(first['files'][0]['truncated'])
        self.assertNotEqual(first['files'][0]['sha256'], second['files'][0]['sha256'])
        state.unlink(); state.symlink_to(catalog)
        with self.assertRaises(d.monitor.MonitorError):
            d.repository_context('alpha', catalog)

    def test_codex_resumes_exact_project_session(self):
        from argparse import Namespace
        args = Namespace(state_dir=self.root / 'judge', provider='codex', executable='fake-codex', model=None, timeout=10, context_command_file=None)
        packet = {**self.packet, 'allowed_actions': ['continue', 'wait']}
        calls = []
        def fake(command, prompt, **kwargs):
            calls.append(command)
            m.save_json(Path(command[command.index('--output-last-message') + 1]), self.decision())
            return [{'type': 'thread.started', 'thread_id': 'fixed-project-session'}]
        with patch.object(d.monitor, 'run_json', side_effect=fake):
            d.decide(packet, args)
            d.decide(packet, args)
        self.assertNotIn('resume', calls[0])
        self.assertEqual(calls[1][2:4], ['resume', 'fixed-project-session'])
        self.assertNotIn('--last', calls[1])
        self.assertEqual(m.read_json(args.state_dir / 'alpha/codex/session.json')['notifications'], 2)

    def test_inspector_cannot_escape_project_or_execute_arbitrary_commands(self):
        project = self.root / 'repo'; project.mkdir()
        (project / 'STATE.md').write_text('first\nsecond\nthird\n')
        catalog = self.root / 'projects.json'
        m.save_json(catalog, {'projects':[{'id':'alpha','root':str(project)}]})
        request = {'project_id':'alpha','operation':'read','path':'STATE.md','start_line':2}
        self.assertEqual(d.inspect_repository(request,catalog)['text'], 'second\nthird')
        for name in ('../projects.json', str(catalog), '.git/config'):
            with self.assertRaises(d.monitor.MonitorError):
                d.inspect_repository({**request,'path':name},catalog)
        with self.assertRaises(d.monitor.MonitorError):
            d.inspect_repository({**request,'operation':'execute'},catalog)
        (project / 'outside').symlink_to(catalog)
        with self.assertRaises(d.monitor.MonitorError):
            d.inspect_repository({**request,'path':'outside'},catalog)

    def test_mcp_inspection_is_pinned_to_configured_project(self):
        spec = importlib.util.spec_from_file_location('inspect_mcp', ROOT / 'scripts/codexpro-ralph-inspect.py')
        module = importlib.util.module_from_spec(spec); spec.loader.exec_module(module)
        request = {'method':'tools/call','params':{'name':'project_inspect','arguments':{'operation':'history'}}}
        with patch.object(module.m,'run_json',return_value={'text':'history'}) as call:
            module.handle(request,'alpha',['read-helper'])
        self.assertEqual(call.call_args.args[1]['project_id'],'alpha')
        request['params']['arguments']['project_id']='other'
        with self.assertRaises(module.m.MonitorError):
            module.handle(request,'alpha',['read-helper'])


if __name__ == '__main__':
    unittest.main()
