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
                'next_step': 'Read STATE.md and verify the next approved acceptance item.' if action in m.SEND_ACTIONS else ''}

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

    def observe_peers(self, records, results=None, reconciled=None):
        source = m.Sources.__new__(m.Sources)
        source.base = FakeSources.base
        source.config = {}
        source.activity = lambda *_: {'schema_version': 1, 'generated_at': iso(time.time()),
                                      'project': self.packet['project']}
        calls = []
        def pilot(action, payload, key=None):
            calls.append((action, payload))
            if action == 'query.list':
                return [{'projectUrl': self.target['chatgpt_project_url'], **r} for r in records]
            if action == 'query.get':
                return reconciled
            if payload['id'] == 'q_old':
                return {'queryId': 'q_old', 'remoteConversationId': 'old-chat', 'state': 'ready',
                        'page': {'busy': False, 'capturedAt': iso(time.time())},
                        'turns': [{'id': 'done', 'state': 'completed'}]}
            result = (results or {})[payload['id']]
            if isinstance(result, Exception):
                raise result
            return result
        source.pilot = pilot
        return source.observe(self.target), calls

    def test_retired_peers_do_not_refresh_missing_tabs_or_exhaust_peer_limit(self):
        records = [{'id': 'retired-' + str(i), 'state': 'orphaned', 'tabId': None,
                    'turns': [{'state': 'completed'}, {'state': 'failed'}]} for i in range(15)]
        packet, calls = self.observe_peers(records)
        self.assertEqual(packet['other_conversations'], [])
        self.assertEqual([p['id'] for a, p in calls if a == 'query.result'], ['q_old'])
        self.assertEqual(m.eligibility(packet, self.target, self.config, time.time())[0], 'continue')

    def test_missing_tabs_with_unsettled_or_unknown_turns_still_block(self):
        for turns in [[{'state': 'uncertain'}], [{'state': 'streaming'}], [{'state': 'unknown'}], None]:
            with self.subTest(turns=turns):
                packet, calls = self.observe_peers([{'id': 'unresolved', 'state': 'orphaned', 'tabId': None, 'turns': turns}])
                self.assertEqual(packet['other_conversations'][0]['id'], 'unresolved')
                self.assertEqual(m.eligibility(packet, self.target, self.config, time.time())[0], 'wait')
                self.assertEqual([p['id'] for a, p in calls if a == 'query.result'], ['q_old'])

    def test_live_busy_peer_is_still_refreshed_and_blocks_dispatch(self):
        packet, calls = self.observe_peers([{'id': 'live', 'state': 'busy', 'tabId': 123}],
            {'live': {'state': 'busy', 'page': {'busy': True, 'capturedAt': iso(time.time())}}})
        self.assertIn(('query.result', {'id': 'live', 'refresh': True}), calls)
        self.assertEqual(m.eligibility(packet, self.target, self.config, time.time())[0], 'wait')

    def test_peer_disappearing_during_refresh_requires_terminal_receipt(self):
        for terminal in [True, False]:
            with self.subTest(terminal=terminal):
                packet, calls = self.observe_peers([{'id': 'vanished', 'state': 'ready', 'tabId': 123}],
                    {'vanished': m.MonitorError('tab missing')},
                    {'id': 'vanished', 'state': 'orphaned', 'tabId': None,
                     'turns': [{'state': 'completed' if terminal else 'uncertain'}]})
                self.assertIn(('query.get', {'id': 'vanished'}), calls)
                self.assertEqual(m.eligibility(packet, self.target, self.config, time.time())[0],
                                 'continue' if terminal else 'wait')

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
        self.assertIn('Work directly through CodexPro', prompt)
        self.assertIn('no other LLM agents or AI-Bridge delegation', prompt)
        self.assertIn('Read STATE.md and verify', prompt)

    def test_idle_blocked_run_gets_orchestrator_recovery_and_one_followup(self):
        self.packet['run'] = {'mode': 'ralph', 'state': 'blocked', 'unresolved_operations': 0,
                              'checkpoint': {'blockers': ['Missing acceptance command bindings']}}
        with patch.object(m, 'run_json', return_value=self.decision('recover')) as judge:
            report = m.check_once(self.config, self.sources, self.target, self.root, True)
        self.assertIn('recover', judge.call_args.args[1]['allowed_actions'])
        self.assertTrue(report['sent'])
        self.assertEqual(self.sources.calls[0][0], 'query.follow-up')
        self.assertTrue(self.sources.calls[0][1]['prompt'].startswith('Retry CodexPro'))
        with patch.object(m, 'run_json', return_value=self.decision('wait')):
            report = m.check_once(self.config, self.sources, self.target, self.root, True)
        self.assertFalse(report['sent'])
        self.assertEqual(len(self.sources.calls), 1)

    def test_recovery_does_not_override_activity_identity_or_human_stop(self):
        self.packet['run'] = {'mode': 'ralph', 'state': 'blocked', 'unresolved_operations': 1}
        cases = [('chatgpt.busy', True), ('project.inflight_counts.jobs', 1),
                 ('run.claimed', True), ('run.state', 'paused'), ('run.state', 'cancelled'),
                 ('run.state', 'recovering'), ('run.unresolved_operations', None),
                 ('chatgpt.turn_state', 'canceled'), ('chatgpt.conversation_id', 'wrong'),
                 ('chatgpt.state', 'needs-auth'), ('server_generated_at', iso(1))]
        for key, value in cases:
            with self.subTest(key=key):
                packet = copy.deepcopy(self.packet)
                target = packet
                parts = key.split('.')
                for part in parts[:-1]:
                    target = target[part]
                target[parts[-1]] = value
                self.assertNotIn(m.eligibility(packet, self.target, self.config, time.time())[0], m.SEND_ACTIONS)
        state = m.read_json(self.directory / 'state.json')
        state['project_status'] = 'blocked_human'
        self.assertEqual(m.guard(self.packet, self.target, self.config, state, time.time())[0], 'stopped')

    def test_recovery_without_conversation_opens_one_pro_worker(self):
        self.target.pop('query_id'); self.target.pop('conversation_url')
        self.packet['chatgpt'] = None
        self.packet['run'] = {'mode': 'ralph', 'state': 'draft', 'unresolved_operations': 0}
        with patch.object(m, 'run_json', return_value=self.decision('recover')):
            report = m.check_once(self.config, self.sources, self.target, self.root, True)
        self.assertTrue(report['sent'])
        self.assertEqual(self.sources.calls[0][0], 'query.start')
        self.assertEqual(self.sources.calls[0][1]['effort'], 'Pro')

    def test_short_prompt_limit_is_enforced_in_model_schema_and_validation(self):
        packet = {**self.packet, 'allowed_actions': ['continue']}
        decision = self.decision()
        decision['next_step'] = 'x' * (m.MAX_NEXT_STEP_CHARS + 1)
        with self.assertRaises(m.MonitorError):
            m.validate_decision(decision, packet)
        self.assertEqual(d.decision_schema(packet)['properties']['next_step']['maxLength'], 360)
        prompt = m.continuation_prompt({'project_id':'ibkr', 'run_id':'run_FfdqaQbE2bwzKEODqQAd1DAA'}, 'x' * 360)
        self.assertLess(len(prompt), 750)

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

    def test_confirmed_pre_send_failure_retries_without_uncertainty_window(self):
        state = m.read_json(self.directory / 'state.json')
        state['pending_send'] = {'at':time.time()-5, 'key':'failed-key', 'fingerprint':'old',
            'progress_key':m.progress_key(self.packet), 'action':'query.follow-up',
            'query_id':'q_old', 'prompt_digest':'exact-prompt', 'previous_turn_id':'previous'}
        self.packet['chatgpt'].update({'turn_state':'failed', 'failed_before_send':True,
            'turn_id':'failed', 'turn_predecessor_id':'previous', 'turn_prompt_digest':'exact-prompt'})
        self.assertEqual(m.guard(self.packet,self.target,self.config,state,time.time())[0], 'continue')
        self.assertNotIn('pending_send',state)
        self.assertEqual(len(state['failed_send_history']),1)
        # Failed preparation attempts still count toward retry limits.
        state['failed_send_history'] *= self.config['max_no_progress_sends']
        self.assertEqual(m.guard(self.packet,self.target,self.config,state,time.time())[0], 'intervene')

    def test_pre_send_failure_cannot_clear_an_unrelated_or_busy_send(self):
        for field, value in [('turn_prompt_digest','different'), ('turn_predecessor_id','unrelated'), ('busy',True)]:
            with self.subTest(field=field):
                state=m.read_json(self.directory/'state.json')
                state['pending_send']={'at':time.time()-5,'key':'pending','fingerprint':'old',
                    'progress_key':'old', 'action':'query.follow-up', 'query_id':'q_old',
                    'prompt_digest':'exact', 'previous_turn_id':'previous'}
                packet=copy.deepcopy(self.packet)
                packet['chatgpt'].update({'turn_state':'failed','failed_before_send':True,
                    'turn_id':'failed','turn_predecessor_id':'previous','turn_prompt_digest':'exact',field:value})
                self.assertEqual(m.guard(packet,self.target,self.config,state,time.time())[0],'wait')
                self.assertIn('pending_send',state)

    def test_pre_send_classification_uses_receipt_not_assistant_permission_claim(self):
        activity={'schema_version':1,'generated_at':iso(time.time()),'project':self.packet['project']}
        turn={'id':'failed','state':'failed','prompt':'continue',
              'error':'ChatGPT submission preparation failed before Send: composer timeout'}
        query={'queryId':'q_old','turns':[{'id':'previous'},turn], 'page':{}}
        packet=m.make_packet(self.target,activity,query,'https://example.test')
        self.assertTrue(packet['chatgpt']['failed_before_send'])
        turn['acceptanceEvidence']={'acceptedMessageId':'accepted'}
        self.assertFalse(m.make_packet(self.target,activity,query,'https://example.test')['chatgpt']['failed_before_send'])
        turn.pop('acceptanceEvidence');turn['error']='Request timed out after Send'
        self.assertFalse(m.make_packet(self.target,activity,query,'https://example.test')['chatgpt']['failed_before_send'])

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


class InboxTests(unittest.TestCase):
    def setUp(self):
        self.tmp=tempfile.TemporaryDirectory();self.root=Path(self.tmp.name)
        self.catalog=self.root/'catalog.json'
        self.catalog.write_text(json.dumps({'projects':[{'id':'alpha','root':str(self.root)}]}))
        self.item={'schema_version':1,'id':'rounding','project_id':'alpha','title':'Rounding','question':'How to round?',
            'blocking_scope':'ticket','blocked_work':['COM-01'],'status':'answered','revision':2,
            'answer':{'text':'Round up','by':'operator','request_id':'answer-1','at':iso(time.time())}}

    def tearDown(self):self.tmp.cleanup()

    def test_delivery_is_project_scoped_idempotent_and_preserves_prior_answers(self):
        req={'schema_version':1,'project_id':'alpha','answers':[self.item]}
        result=d.apply_inbox_answers(req,self.catalog)
        self.assertEqual(result['applied'],{'rounding':2})
        self.assertEqual(d.apply_inbox_answers(req,self.catalog),result)
        second={**self.item,'id':'floor','answer':{**self.item['answer'],'text':'Floor is fine'}}
        d.apply_inbox_answers({**req,'answers':[second]},self.catalog)
        self.assertEqual(len(json.loads((self.root/'INBOX_ANSWERS.json').read_text())['answers']),2)
        for item in [{**self.item,'project_id':'wrong'},{**self.item,'revision':1},{**self.item,'answer':{'text':'Changed at same revision'}}]:
            with self.assertRaises(d.monitor.MonitorError):d.apply_inbox_answers({**req,'answers':[item]},self.catalog)
        (self.root/'STATE.md').write_text('active')
        with patch.object(d,'git_read',return_value={'text':'fixture','truncated':False}):
            context=d.repository_context('alpha',self.catalog)
        self.assertTrue(any(f['path']=='INBOX_ANSWERS.json' for f in context['files']))

    def test_repository_outbox_is_validated_and_posts_without_waiting_for_llm(self):
        (self.root/'STATE.md').write_text('active')
        question={'schema_version':1,'id':'floor','project_id':'alpha','source':'worker','title':'Delivery','question':'Floor?','blocking_scope':'ticket'}
        path=self.root/'INBOX_QUESTIONS.json';path.write_text(json.dumps({'schema_version':1,'questions':[question]}))
        with patch.object(d,'git_read',return_value={'text':'fixture','truncated':False}):
            context=d.repository_context('alpha',self.catalog)
        self.assertIsNone(context['inbox_question_error']);self.assertEqual(context['inbox_questions'][0]['id'],'floor')
        with patch.object(m,'inbox_exchange',return_value={'schema_version':1}) as post:
            m.publish_questions({'inbox_command':['inbox']},{'project_id':'alpha'},{},{'action':'wait'},{},self.root,context['inbox_questions'])
        self.assertEqual(post.call_args.args[1]['question']['source'],'worker')
        for bad in [{**question,'project_id':'other'},{**question,'answer':'self-approved'}]:
            path.write_text(json.dumps({'schema_version':1,'questions':[bad]}))
            with patch.object(d,'git_read',return_value={'text':'fixture','truncated':False}):
                context=d.repository_context('alpha',self.catalog)
            self.assertTrue(context['inbox_question_error']);self.assertEqual(context['inbox_questions'],[])

    def test_symlink_delivery_cannot_escape_project(self):
        path=self.root/'INBOX_ANSWERS.json';path.symlink_to(self.root/'other.json')
        with self.assertRaises(d.monitor.MonitorError):d.apply_inbox_answers({'schema_version':1,'project_id':'alpha','answers':[self.item]},self.catalog)
        self.assertFalse((self.root/'other.json').exists())

    def test_answer_delivered_before_latch_release_and_never_clears_manual_hold(self):
        config={'inbox_command':['inbox'],'inbox_answers_command':['apply']}
        state={'project_status':'blocked_human','last_judgement':{'old':True}}
        target={'project_id':'alpha','hold':'Explicit stop'}
        calls=[]
        def run(command,request,**kwargs):
            calls.append((command,request))
            if command==['apply']:return {'project_id':'alpha','applied':{'rounding':2}}
            if request['operation']=='list':return {'schema_version':1,'items':[self.item],'next_offset':None}
            return {'schema_version':1}
        with patch.object(m,'run_json',side_effect=run):
            packet,changed=m.sync_inbox(config,target,state)
            self.assertTrue(changed);self.assertNotIn('project_status',state);self.assertEqual(target['hold'],'Explicit stop')
            self.assertEqual(calls[1][0],['apply']);self.assertEqual(calls[2][1]['operation'],'deliver')
            self.assertFalse(m.sync_inbox(config,target,state)[1])
        state={'project_status':'blocked_human'}
        with patch.object(m,'run_json',side_effect=[{'schema_version':1,'items':[self.item]},m.MonitorError('write failed')]):
            with self.assertRaises(m.MonitorError):m.sync_inbox(config,target,state)
        self.assertEqual(state['project_status'],'blocked_human');self.assertEqual(state['inbox_delivered'],{})

    def test_partial_questions_publish_without_latching_project_and_failures_stay_in_outbox(self):
        q={'id':'floor','title':'Floor','question':'Allow floor?','context':'','recommendation':'Yes','options':['Yes','No'],'blocking_scope':'ticket','blocked_work':['COM-04']}
        m.validate_questions([q])
        with self.assertRaises(m.MonitorError):m.validate_questions([{**q,'blocking_scope':'everything'}])
        state={};report={};config={'inbox_command':['inbox']};target={'project_id':'alpha'}
        with patch.object(m,'inbox_exchange',side_effect=m.MonitorError('offline')):
            m.publish_questions(config,target,state,{'questions':[q]},report,self.root)
        self.assertIn('floor',m.read_json(self.root/'inbox-outbox.json'));self.assertNotIn('project_status',state)
        with patch.object(m,'inbox_exchange',return_value={'schema_version':1}) as post:
            m.publish_questions(config,target,state,{},report,self.root)
        self.assertEqual(post.call_args.args[1]['question']['question'],'Allow floor?');self.assertEqual(m.read_json(self.root/'inbox-outbox.json'),{})


if __name__ == '__main__':
    unittest.main()
