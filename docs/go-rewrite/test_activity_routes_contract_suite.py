import html
import json
import os
from pathlib import Path
import subprocess
import tempfile
import time
import unittest
import urllib.parse
from unittest.mock import patch

import activity_routes_contract_suite as subject
from telemetry_contract_suite import ContractFailure


class ActivityRouteHarnessTests(unittest.TestCase):
    def parse(self, value):
        parser = subject.RenderedHTML()
        parser.feed(value)
        parser.close()
        return parser.projection()

    def test_caller_text_stays_literal(self):
        value = '<h2>Evidence</h2><pre>' + html.escape(subject.PAYLOAD) + '</pre>'
        result = self.parse(value)
        self.assertEqual(result["preformatted_blocks"], [subject.PAYLOAD])
        self.assertIn("[preformatted-block-0]", result["visible_text"])

    def test_unescaped_payload_element_fails(self):
        with self.assertRaisesRegex(ContractFailure, "structural HTML"):
            self.parse(subject.PAYLOAD)

    def test_code_whitespace_and_link_targets_are_not_discarded(self):
        result = self.parse('<a href="/activity?project_id=a&amp;path=x">return</a><pre>a  b\n c</pre>')
        self.assertEqual(result["preformatted_blocks"], ["a  b\n c"])
        self.assertEqual(result["links"], ["/activity?project_id=a&path=x"])

    def test_styles_and_scripts_do_not_become_visible_content(self):
        result = self.parse('<style>body{color:red}</style><h2>visible</h2><script>var x=1;</script>')
        self.assertEqual(result["visible_text"], "visible")
        self.assertEqual(result["headings"], [["h2", "visible"]])

    def test_incomplete_html_does_not_produce_a_false_projection(self):
        with self.assertRaisesRegex(ContractFailure, "unterminated"):
            self.parse('<pre>truncated')

    def test_bytes_are_bounded_and_unchanged(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'fixture'
            subject.create_file(path, b'abc\n')
            before, witness = subject.stable_file(path)
            self.assertEqual(before, b'abc\n')
            self.assertEqual(witness["sha256"], subject.sha(before))
            self.assertEqual(witness, subject.stable_file(path)[1])
            with self.assertRaisesRegex(ContractFailure, "bounded regular"):
                subject.stable_file(path, limit=3)

    def test_fixture_creation_refuses_overwrite_and_symlinks(self):
        with tempfile.TemporaryDirectory() as directory:
            path, link = Path(directory) / 'fixture', Path(directory) / 'link'
            subject.create_file(path, b'unchanged')
            link.symlink_to(path)
            for target in (path, link):
                with self.assertRaises(FileExistsError):
                    subject.create_file(target, b'changed')
            with self.assertRaises(OSError):
                subject.stable_file(link)
            self.assertEqual(path.read_bytes(), b'unchanged')

    def test_relative_fixture_rejects_traversal_and_symlink_escape(self):
        with tempfile.TemporaryDirectory() as root, tempfile.TemporaryDirectory() as outside:
            side = subject.ActivitySide.__new__(subject.ActivitySide)
            side.root = Path(root)
            (side.root / 'escape').symlink_to(outside, target_is_directory=True)
            for value in ('../escape.json', '/tmp/absolute.json', 'escape/file.json'):
                with self.assertRaises(ContractFailure):
                    side.private_file(value)
            self.assertEqual(side.private_file('owned/file.json'), side.root / 'owned/file.json')

    def test_query_values_preserve_ampersands_quotes_and_spaces(self):
        class Side:
            project = 'fixture'
            workspace = 'ws_private'
            recipe_path = 'owned/a & "quote".json'
        parsed = urllib.parse.urlsplit(subject.batch_url(Side(), fragment=True))
        self.assertEqual(parsed.path, '/activity/batch')
        self.assertEqual(urllib.parse.parse_qs(parsed.query), {
            'project_id': ['fixture'], 'workspace_id': ['ws_private'],
            'path': ['owned/a & "quote".json'], 'fragment': ['1']})

    def test_new_output_required_before_connecting(self):
        with tempfile.TemporaryDirectory() as directory:
            with self.assertRaisesRegex(ContractFailure, 'must be new'):
                subject.run_existing_pair('missing-reference', 'missing-candidate', directory)

    def test_no_lifecycle_control_is_exposed(self):
        self.assertFalse(hasattr(subject.ActivitySide, 'start_server'))
        self.assertFalse(hasattr(subject.ActivitySide, 'stop_server'))

    def test_case_registry_requires_all_expected_business_checks(self):
        class Lane:
            def __init__(self):
                self.cases = []
            def check(self, label, function, **kwargs):
                self.cases.append({'case': label, 'passed': True})
        lane = Lane()
        subject.run_cases(lane)
        names = [row['case'] for row in lane.cases]
        self.assertEqual(len(names), 24)
        self.assertEqual(len(names), len(set(names)))
        self.assertIn('http-readonly-storage-witnesses', names)
        self.assertIn('completion-visible-after-drawers', names)
        self.assertIn('git-review-checkpoint-unchanged', names)

    def test_fragment_json_is_complete_without_page_attribution(self):
        class Side:
            definition = {'version': 1, 'mode': 'serial', 'continue_on_error': False,
                'operations': [{'id': 'read-0', 'tool': 'read', 'args': {'path': '<sp6-render-proof>"&.txt'}}]}
            def normalize(self, value):
                return value
        side = Side()
        body = '<span>serial 1 operation</span><summary>0 read-0 read</summary><pre>'
        body += html.escape(json.dumps(side.definition['operations'][0]['args'])) + '</pre>'
        body += '<summary>Raw JSON</summary><pre>' + html.escape(json.dumps(side.definition, sort_keys=True)) + '</pre>'
        response = {'http_status': 200, 'headers': {'content-type': 'text/html; charset=utf-8'}, 'body': body}
        result = subject.rendered_projection(side, response, kind='batch')
        self.assertEqual(result['rendered']['preformatted_blocks'][-1]['json'], side.definition)
        response['body'] = response['body'].replace('&quot;version&quot;: 1', '&quot;version&quot;: true')
        with self.assertRaisesRegex(ContractFailure, 'changed arguments'):
            subject.rendered_projection(side, response, kind='batch')

    def test_git_fixture_drops_routing_and_injected_config_environment(self):
        side = subject.ActivitySide.__new__(subject.ActivitySide)
        side.root, side.deadline = Path('/tmp/private-fixture'), time.monotonic() + 30
        injected = {'GIT_INDEX_FILE': '/outside/index', 'GIT_WORK_TREE': '/outside/tree',
                    'GIT_DIR': '/outside/git', 'GIT_COMMON_DIR': '/outside/common',
                    'GIT_CONFIG_COUNT': '1', 'GIT_CONFIG_KEY_0': 'alias.status',
                    'GIT_CONFIG_VALUE_0': '!external command'}
        completed = subprocess.CompletedProcess([], 0, stdout=b'', stderr=b'')
        with patch.dict(os.environ, injected), patch.object(subject.subprocess, 'run', return_value=completed) as run:
            side.git('status', '--porcelain')
        environment = run.call_args.kwargs['env']
        self.assertFalse(set(injected) & set(environment))
        self.assertEqual(environment['GIT_CONFIG_GLOBAL'], os.devnull)

    def cleanup_fixture(self, status='succeeded', error=None):
        class Client:
            credential, session, closed = 'fixture-private-credential', None, False
            def call(self, name, args):
                if error:
                    raise error
                return {'body': {'result': {'structuredContent': {'jobs': [
                    {'job_id': 'job_abcdef12', 'status': status}]}}}}
            def close(self):
                self.closed = True
        class Side:
            client, workspace, owned_jobs = Client(), 'ws_owned', {'job_abcdef12'}
        return Side()

    def test_running_cleanup_reply_cannot_pass_and_still_closes(self):
        side = self.cleanup_fixture(status='running')
        errors = subject.cleanup_side(side)
        self.assertEqual(len(errors), 1)
        self.assertIn('terminal settlement', errors[0])
        self.assertTrue(side.client.closed)

    def test_cleanup_transport_failure_still_closes(self):
        side = self.cleanup_fixture(error=TimeoutError('fixture timeout'))
        self.assertEqual(len(subject.cleanup_side(side)), 1)
        self.assertTrue(side.client.closed)

    def test_settled_owned_cleanup_passes(self):
        side = self.cleanup_fixture()
        self.assertEqual(subject.cleanup_side(side), [])
        self.assertTrue(side.client.closed)

    def test_review_baseline_warms_cache_without_changing_checkpoint(self):
        class Side:
            workspace = 'ws_owned'
            def __init__(self):
                self.calls = []
            def mcp(self, name, args):
                self.calls.append((name, dict(args)))
                return {'body': {'result': {'structuredContent': {
                    'analysis': {'cache': {'hit': len(self.calls) > 1, 'key': 'unchanged-real-inventory-key'}},
                    'diff': 'the unchanged controlled diff'}}}}
            def data(self, response):
                return response['body']['result']['structuredContent']
        side = Side()
        result = subject.stable_review_baseline(side)
        self.assertTrue(result['structuredContent']['analysis']['cache']['hit'])
        self.assertEqual(result['structuredContent']['analysis']['cache']['key'], 'unchanged-real-inventory-key')
        self.assertEqual(len(side.calls), 2)
        self.assertTrue(all(name == 'show_changes' and args['mark_reviewed'] is False
                            and args['since'] == 'last_shown' for name, args in side.calls))


if __name__ == '__main__':
    unittest.main()
