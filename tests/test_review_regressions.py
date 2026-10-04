import concurrent.futures
import io
import json
from pathlib import Path
import threading
import unittest
from unittest.mock import patch

import test_panel
from test_deletion_preflight import Sources

app = test_panel.app


class ReviewPanelTests(unittest.TestCase):
    setUp = test_panel.PanelTests.setUp
    tearDown = test_panel.PanelTests.tearDown
    request = test_panel.PanelTests.request

    def test_pending_delete_keeps_target_locked_and_replays_once(self):
        preview = {'kind': 'waker', 'target': 'w1', 'revision': 'fp',
                   'impactDigest': 'a' * 64, 'checks': [], 'warningsRequired': [],
                   'deletable': True, 'completeness': 'complete'}
        with patch.object(app, 'deletion_scan', return_value=preview), \
                patch.object(app, 'daemon_json', side_effect=app.DaemonRequestError(
                    202, 'daemon_operation_pending')) as delete:
            _, _, raw = self.request('POST', '/api/deletion/preview',
                {'kind': 'waker', 'target': 'w1'}, token=self.sec.admin)
            plan = json.loads(raw)
            body = {'id': 'w1', 'preview': plan['id'], 'impactDigest': plan['impactDigest'],
                    'acknowledgedWarnings': [], 'confirm': '/api/waker/delete'}
            headers = {'Idempotency-Key': 'review-delete-pending'}
            for _ in range(2):
                status, _, raw = self.request('POST', '/api/waker/delete', body,
                    token=self.sec.admin, headers=headers)
                self.assertEqual(status, 202)
                self.assertEqual(json.loads(raw)['status'], 'pending')
            self.assertEqual(self.request('POST', '/api/deletion/preview',
                {'kind': 'waker', 'target': 'w1'}, token=self.sec.admin)[0], 409)
            status, _, raw = self.request('POST', '/api/deletion/status',
                {'idempotencyKey': headers['Idempotency-Key']}, token=self.sec.admin)
            self.assertEqual(status, 202)
            self.assertEqual(json.loads(raw)['status'], 'pending')
        self.assertEqual(delete.call_count, 1)
        self.assertTrue(self.sec.has_unresolved_deletions())
        caller = self.sec.create_caller('fixture', ['w1'], 1, 1)
        with patch.object(app, 'gw_ask') as ask:
            self.assertEqual(self.request('POST', '/api/gw',
                {'wakerId': 'w1', 'message': 'fixture'}, token=caller['token'])[0], 409)
            ask.assert_not_called()
        self.assertEqual(self.sec.callers()[0]['used'], 0)

    def test_new_dependency_is_rejected_during_fresh_delete_scan(self):
        sources = Sources()
        sources.callers = self.sec.callers
        entered, release = threading.Event(), threading.Event()
        scan = app.deletion_scan
        def paused_scan(*args):
            value = scan(*args)
            entered.set()
            if not release.wait(5):
                raise RuntimeError('test_timeout')
            return value
        with patch.object(app, 'DeletionSources', return_value=sources), \
                patch.object(app, 'daemon_json', return_value={'success': True}) as delete:
            preview = app.deletion_preview({'kind': 'waker', 'target': 'w1'}, 'fixture-admin')
            body = {'kind': 'waker', 'target': 'w1', 'preview': preview['id'],
                    'impactDigest': preview['impactDigest'],
                    'acknowledgedWarnings': preview['warningsRequired']}
            with patch.object(app, 'deletion_scan', side_effect=paused_scan):
                with concurrent.futures.ThreadPoolExecutor(max_workers=1) as pool:
                    future = pool.submit(app.execute_deletion, body, 'fixture-admin', 'review-delete-race')
                    try:
                        self.assertTrue(entered.wait(5))
                        status, _, raw = self.request('POST', '/api/callers/create', {
                            'name': 'fixture', 'wakers': ['w1'], 'quota': 1, 'days': 1,
                            'confirm': '/api/callers/create'}, token=self.sec.admin)
                        self.assertEqual(status, 409)
                        self.assertEqual(json.loads(raw)['error'], 'resource_mutation_in_progress')
                    finally:
                        release.set()
                    self.assertEqual(future.result(timeout=5)['status'], 'succeeded')
        self.assertEqual(self.sec.callers(), [])
        delete.assert_called_once_with('DELETE', '/api/agents/w1')

    def test_null_dependency_sources_do_not_become_empty_lists(self):
        for payload in ({}, {'data': None}, {'data': False}, {'data': ''}):
            with patch.object(app, 'daemon_json', return_value=payload):
                for call in (app.deletion_wakers, app.deletion_channels,
                             lambda: app.waker_detail('w1')):
                    with self.subTest(payload=payload, call=call):
                        with self.assertRaises(ValueError):
                            call()
        with patch.object(app, 'daemon_json', return_value={'data': {'agentId': 'other'}}):
            with self.assertRaisesRegex(ValueError, 'invalid_waker_detail'):
                app.waker_detail('w1')

    def test_null_providers_load_as_empty_without_mutating_disk(self):
        raw = b'{"providers":null,"unknown":true}'
        app.SETTINGS.write_bytes(raw)
        with patch.object(app, 'api_state', return_value={
                'models': [], 'wakers': [], 'whoami': None, 'daemon': 'running'}):
            app.invalidate_state()
            status, _, body = self.request('GET', '/api/state', token=self.sec.admin)
        self.assertEqual(status, 200)
        self.assertEqual(json.loads(body)['providers'], [])
        self.assertEqual(app.SETTINGS.read_bytes(), raw)

    def test_refresh_does_not_publish_result_invalidated_by_write(self):
        entered, release = threading.Event(), threading.Event()
        base = {'models': [], 'wakers': [], 'whoami': None, 'daemon': 'running'}
        calls = []
        def refresh():
            calls.append(1)
            if len(calls) == 1:
                entered.set()
                if not release.wait(5):
                    raise RuntimeError('test_timeout')
                return dict(base, marker='old')
            return dict(base, marker='new')
        with patch.object(app, 'api_state', side_effect=refresh), \
                patch.object(app, 'set_preference', return_value=(True, '')):
            app.invalidate_state()
            with concurrent.futures.ThreadPoolExecutor(max_workers=1) as pool:
                future = pool.submit(app.panel_state, 'viewer')
                try:
                    self.assertTrue(entered.wait(5))
                    self.assertEqual(self.request('GET', '/api/whoami', token=self.sec.viewer)[0], 200)
                    self.assertEqual(self.request('POST', '/api/preference',
                        {'wakerId': 'w1', 'model': 'fixture/model'}, token=self.sec.admin)[0], 200)
                finally:
                    release.set()
                self.assertEqual(future.result(timeout=5)['marker'], 'new')
            self.assertEqual(app.panel_state('viewer')['marker'], 'new')
        self.assertEqual(len(calls), 2)

    def test_state_http_reads_are_bounded_and_closed(self):
        responses = []
        class Response(io.BytesIO):
            status = 200
            def __init__(self):
                super().__init__(b'x' * (app.MAX_BOOTSTRAP_BYTES + 2))
                self.sizes = []
            def read(self, size=-1):
                self.sizes.append(size)
                return super().read(size)
        class Session:
            def open(self, *args, **kwargs):
                response = Response()
                responses.append(response)
                return response
        with patch.object(app, 'console_op', return_value=(Session(), {})), \
                patch.object(app, 'cli', side_effect=RuntimeError('fixture')):
            value = app.api_state()
        self.assertEqual(value['error'], 'daemon_unavailable')
        self.assertEqual(responses[0].sizes, [app.MAX_BOOTSTRAP_BYTES + 1])
        self.assertTrue(responses[0].closed)

    def test_failed_preference_is_unknown_not_auto(self):
        def daemon(method, path, **kwargs):
            return {'data': {'models': []}} if path == '/api/models' else {'data': [{'agentId': 'w1'}]}
        with patch.object(app, 'daemon_json', side_effect=daemon), \
                patch.object(app, 'cli', side_effect=RuntimeError('fixture')):
            self.assertEqual(app.api_state()['wakers'][0]['preference'], '(未知)')


if __name__ == '__main__':
    unittest.main()
