import concurrent.futures
import importlib.util
import json
import os
from pathlib import Path
import sqlite3
import tempfile
import threading
import time
import unittest
from unittest.mock import patch

import test_panel
from log_io import tail_records
import usage_store
import panel_security
import gateway_runtime

app = test_panel.app
ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('bounded_gateway', ROOT / 'ops/uplink-gw.py')
gw = importlib.util.module_from_spec(spec)
spec.loader.exec_module(gw)


class TailTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.root = Path(self.tmp.name).resolve()
        self.path = self.root / 'audit'

    def tearDown(self):
        self.tmp.cleanup()

    def test_large_file_reads_only_bounded_tail(self):
        with self.path.open('wb') as stream:
            stream.seek(110 * 1024 * 1024)
            stream.write(b'\nold\nnew\npartial')
        value = tail_records([self.path], max_lines=2, max_bytes=32768)
        self.assertEqual(value['lines'], ['old', 'new'])
        self.assertLessEqual(value['bytesRead'], 32768)
        self.assertTrue(value['truncated'])

    def test_rotated_files_are_ordered_and_share_one_budget(self):
        old = self.root / 'audit.1'
        old.write_bytes(b'one\ntwo\n')
        self.path.write_bytes(b'three\nfour\n')
        value = tail_records([self.path, old], max_lines=3, max_bytes=100)
        self.assertEqual(value['lines'], ['two', 'three', 'four'])
        self.assertEqual(value['sources'], 2)
        self.assertLessEqual(value['bytesRead'], 100)

    def test_directory_walk_only_requires_read_access_at_leaf(self):
        opened = []
        def open_path(name, flags, **kwargs):
            opened.append((name, flags))
            return len(opened) + 10
        with patch.object(gateway_runtime.os, 'O_PATH', 0x200000, create=True), \
                patch.object(gateway_runtime.os, 'open', side_effect=open_path), \
                patch.object(gateway_runtime.os, 'close'):
            gateway_runtime._open_directory('/parent/private/logs')
        self.assertTrue(all(flags & 0x200000 for _, flags in opened[:-1]))
        self.assertFalse(opened[-1][1] & 0x200000)
        self.assertTrue(all(flags & os.O_NOFOLLOW for _, flags in opened))

    def test_parent_directory_symlink_is_rejected(self):
        directory = self.root / 'actual'
        directory.mkdir()
        (directory / 'audit').write_text('record\n')
        link = self.root / 'linked'
        link.symlink_to(directory, target_is_directory=True)
        with self.assertRaises(OSError):
            tail_records([link / 'audit'])

    def test_byte_boundary_never_parses_partial_record(self):
        self.path.write_bytes(b'not-a-complete-record\nnew\n')
        value = tail_records([self.path], max_bytes=12)
        self.assertEqual(value['lines'], ['new'])

    def test_missing_empty_and_invalid_entries(self):
        self.assertEqual(tail_records([self.path])['lines'], [])
        self.path.touch()
        self.assertEqual(tail_records([self.path])['lines'], [])
        link = self.root / 'link'
        link.symlink_to(self.path)
        with self.assertRaises((OSError, ValueError)):
            tail_records([link])
        fifo = self.root / 'fifo'
        os.mkfifo(fifo)
        with self.assertRaises(ValueError):
            tail_records([fifo])


class GatewayLogTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.root = Path(self.tmp.name).resolve()
        self.path = self.root / 'gateway' / 'audit'
        self.change = patch.multiple(gw, LOG=self.path, LOG_MAX_BYTES=256, LOG_BACKUPS=3)
        self.change.start()

    def tearDown(self):
        self.change.stop()
        self.tmp.cleanup()

    def test_rotation_bounds_files_and_preserves_order(self):
        gw.prepare_log()
        for number in range(100):
            gw.logline({'number': number})
        files = [self.path] + [self.path.with_name('audit.' + str(i)) for i in range(1, 4)]
        self.assertTrue(all(path.stat().st_size <= 256 for path in files))
        self.assertTrue(all(path.stat().st_mode & 0o777 == 0o600 for path in files))
        rows = [json.loads(line)['number'] for line in tail_records(files)['lines']]
        self.assertEqual(rows, list(range(rows[0], 100)))
        self.assertFalse(self.path.with_name('audit.4').exists())

    def test_concurrent_records_are_complete_and_not_lost(self):
        with patch.object(gw, 'LOG_MAX_BYTES', 100000):
            with concurrent.futures.ThreadPoolExecutor(max_workers=8) as pool:
                list(pool.map(lambda number: gw.logline({'number': number}), range(100)))
        values = [json.loads(line)['number'] for line in self.path.read_text().splitlines()]
        self.assertEqual(sorted(values), list(range(100)))

    def test_symlink_archive_and_lock_are_rejected_without_touching_target(self):
        target = self.root / 'target'
        target.write_bytes(b'private')
        for suffix in ('', '.1', '.lock'):
            gw.prepare_log()
            path = self.path.with_name(self.path.name + suffix)
            path.unlink(missing_ok=True)
            path.symlink_to(target)
            with self.assertRaises((OSError, ValueError)):
                gw.logline({'event': 'fixture'})
            self.assertEqual(target.read_bytes(), b'private')
            path.unlink()

    def test_fifo_and_hardlink_rejected_without_blocking(self):
        self.path.parent.mkdir()
        os.mkfifo(self.path)
        with self.assertRaises(ValueError):
            gw.prepare_log()
        self.path.unlink()
        target = self.root / 'other'
        target.write_bytes(b'original')
        os.link(target, self.path)
        with self.assertRaises(ValueError):
            gw.prepare_log()
        self.assertEqual(target.read_bytes(), b'original')

    def test_oversize_record_does_not_write(self):
        with self.assertRaisesRegex(ValueError, 'record_too_large'):
            gw.logline({'value': 'x' * gw.MAX_LOG_RECORD})
        self.assertFalse(self.path.exists())


class UsageTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.root = Path(self.tmp.name).resolve()
        self.database = self.root / 'usage.db'
        self.runs = self.root / 'runs'
        self.path = self.runs / 'one' / 'qodercli.log'
        self.path.parent.mkdir(parents=True)
        self.line = '2026-10-06T08:00:00 turn.started model="fixture/model"\n'

    def tearDown(self):
        self.tmp.cleanup()

    def read(self):
        return usage_store.usage_data(self.database, self.runs)

    def count(self, value):
        return dict(value['perModel']).get('fixture/model', 0)

    def test_append_duplicate_lines_and_reopen_do_not_double_count(self):
        self.path.write_text(self.line * 2)
        self.assertEqual(self.count(self.read()), 2)
        self.assertEqual(self.read()['scan']['bytesRead'], 0)
        with self.path.open('a') as stream:
            stream.write(self.line)
        value = self.read()
        self.assertEqual(self.count(value), 3)
        self.assertEqual(value['scan']['bytesRead'], len(self.line))
        self.assertEqual(self.count(self.read()), 3)

    def test_partial_line_is_counted_only_after_newline(self):
        self.path.write_text(self.line[:-1])
        value = self.read()
        self.assertEqual(self.count(value), 0)
        self.assertEqual(value['scan']['pendingFiles'], 1)
        with self.path.open('a') as stream:
            stream.write('\n')
        self.assertEqual(self.count(self.read()), 1)

    def test_truncate_and_replace_keep_existing_history(self):
        self.path.write_text(self.line * 2)
        self.assertEqual(self.count(self.read()), 2)
        self.path.write_text(self.line)
        self.assertEqual(self.count(self.read()), 2)
        replacement = self.path.with_suffix('.new')
        replacement.write_text(self.line + self.line.replace('08:00:00', '08:01:00'))
        replacement.replace(self.path)
        self.assertEqual(self.count(self.read()), 3)
        self.path.unlink()
        self.assertEqual(self.count(self.read()), 3)

    def test_old_event_schema_migrates_without_recounting(self):
        self.path.write_text(self.line)
        self.assertEqual(self.count(self.read()), 1)
        with sqlite3.connect(self.database) as con:
            con.execute('DROP TABLE usage_cursors')
            con.execute('DROP TABLE usage_occurrences')
        self.assertEqual(self.count(self.read()), 1)

    def test_large_logs_progress_under_per_request_budget(self):
        self.path.write_text(('unrelated log data\n' * 20000) + self.line)
        with patch.multiple(usage_store, MAX_SCAN_BYTES=80000, MAX_FILE_BYTES=40000):
            for _ in range(20):
                value = self.read()
                self.assertLessEqual(value['scan']['bytesRead'], 80000)
                if value['scan']['complete']:
                    break
        self.assertTrue(value['scan']['complete'])
        self.assertEqual(self.count(value), 1)
        self.assertEqual(self.read()['scan']['bytesRead'], 0)

    def test_oversize_record_is_reported_and_following_event_survives(self):
        self.path.write_text('x' * (usage_store.MAX_LINE_BYTES * 3) + '\n' + self.line)
        value = self.read()
        self.assertEqual(self.count(value), 1)
        self.assertEqual(value['scan']['skippedLines'], 1)
        self.assertFalse(value['scan']['complete'])

    def test_symlink_and_fifo_are_reported_unavailable(self):
        target = self.root / 'target'
        target.write_text(self.line)
        self.path.symlink_to(target)
        self.assertEqual(self.read()['scan']['unavailableFiles'], 1)
        self.path.unlink()
        os.mkfifo(self.path)
        self.assertEqual(self.read()['scan']['unavailableFiles'], 1)


class CompletionPanelTests(unittest.TestCase):
    setUp = test_panel.PanelTests.setUp
    tearDown = test_panel.PanelTests.tearDown
    request = test_panel.PanelTests.request

    def test_state_uses_official_http_without_cli_or_remote_profile_fetch(self):
        routes = []
        def daemon(method, path, **kwargs):
            routes.append(path)
            if path == '/api/models':
                return {'data': {'models': []}}
            if path == '/api/agents':
                return {'data': [{'agentId': 'w1'}, {'agentId': 'w2'}]}
            if path.startswith('/api/model-preferences/'):
                return {'data': {'noProject': {'modelId': 'fixture/model', 'reasoningEffort': 'high'}, 'byProject': {}}}
            if path == '/api/frontend-auth/me':
                return {'data': {'authenticated': True, 'profile': {'name': 'Fixture User', 'email': 'never-return@example.test', 'access_token': 'never-return'}}}
            raise AssertionError(path)
        with patch.object(app, 'daemon_json', side_effect=daemon), patch.object(app, 'cli') as cli:
            value = app.api_state()
        cli.assert_not_called()
        self.assertEqual(len(routes), 5)
        self.assertNotIn('/api/user/profile', routes)
        self.assertEqual(value['whoami'], {'name': 'Fixture User'})
        self.assertEqual([row['preference'] for row in value['wakers']], ['fixture/model'] * 2)

    def test_preference_missing_and_invalid_are_not_default(self):
        for value in ({}, {'noProject': []}, {'noProject': {'model': 'one', 'modelId': 'two'}}, {'byProject': []}):
            with patch.object(app, 'daemon_json', return_value={'data': value}):
                self.assertEqual(app.waker_summary({'agentId': 'w1'})['preference'], '(未知)')
        with patch.object(app, 'daemon_json', return_value={'data': {'byProject': {}}}):
            self.assertEqual(app.waker_summary({'agentId': 'w1'})['preference'], '(默认 auto)')

    def test_state_preference_concurrency_is_bounded(self):
        lock = threading.Lock()
        active, peak = 0, 0
        def preference(waker):
            nonlocal active, peak
            with lock:
                active += 1
                peak = max(peak, active)
            time.sleep(.01)
            with lock:
                active -= 1
            return {'noProject': ''}
        def daemon(method, path, **kwargs):
            if path == '/api/models':
                return {'data': {'models': []}}
            if path == '/api/agents':
                return {'data': [{'agentId': 'w' + str(i)} for i in range(12)]}
            return {'data': {}}
        with patch.object(app, 'daemon_json', side_effect=daemon), patch.object(app, 'deletion_preference', side_effect=preference):
            self.assertEqual(len(app.api_state()['wakers']), 12)
        self.assertGreater(peak, 1)
        self.assertLessEqual(peak, 4)

    def test_runtime_unknown_is_not_pending_and_cannot_restart(self):
        with patch.object(app, 'daemon_process_state', return_value=None):
            value = app.runtime_state()
        self.assertIsNone(value['pendingRestart'])
        self.assertEqual(value['observation'], 'unknown')
        self.assertFalse(value['restartAllowed'])

    def test_runtime_uplink_policy_is_explicit_and_old_clients_preserve_it(self):
        body = {'hotDeploy': False, 'embeddingDisabled': True, 'sessionProjectionUplink': False,
                'remoteExecutionUplink': None, 'confirm': '/api/runtime/policy'}
        self.assertEqual(self.request('POST', '/api/runtime/policy', body, token=self.sec.admin)[0], 200)
        with patch.dict(os.environ, {'QODERWAKE_REMOTE_EXECUTION_UPLINK': 'off'}, clear=True):
            env = app.runtime_env()
            self.assertEqual(env['QODERWAKE_SESSION_PROJECTION_UPLINK'], '0')
            self.assertEqual(env['QODERWAKE_REMOTE_EXECUTION_UPLINK'], 'off')
        self.assertEqual(self.request('POST', '/api/runtime/policy', {
            'hotDeploy': True, 'embeddingDisabled': True, 'confirm': '/api/runtime/policy'}, token=self.sec.admin)[0], 200)
        self.assertIs(app.runtime_policy()['sessionProjectionUplink'], False)
        before = (self.root / 'runtime-policy.json').read_bytes()
        body['sessionProjectionUplink'] = 'false'
        self.assertEqual(self.request('POST', '/api/runtime/policy', body, token=self.sec.admin)[0], 400)
        self.assertEqual((self.root / 'runtime-policy.json').read_bytes(), before)

    def test_runtime_policy_refuses_symlink(self):
        target = self.root / 'target'
        target.write_text('{"hotDeploy":false,"embeddingDisabled":true}')
        (self.root / 'runtime-policy.json').symlink_to(target)
        with self.assertRaises(ValueError):
            app.runtime_policy()

    def test_network_malformed_rows_do_not_break_valid_window(self):
        log = self.root.resolve() / 'logs' / 'uplink-gw.jsonl'
        log.parent.mkdir()
        log.write_text('\n'.join(json.dumps(row) for row in [None, [], {},
            {'path': '/fixture', 'method': 'POST', 'q': -1, 'status': 200},
            {'path': '/fixture', 'method': 'POST', 'q': 42, 'status': 204}]) + '\n')
        with patch.multiple(app, GWLOG=log), patch.object(app, 'gateway_active', return_value={'managed': False, 'healthy': False}), patch.object(app, 'daemon_process_state', return_value=None), patch.object(app, 'gw_cfg', return_value={'mode': 'enforce'}), patch.object(app, 'fw_state', return_value={}):
            value = app.net_state()
        self.assertEqual(value['total'], 1)
        self.assertEqual(value['up_bytes'], 42)
        self.assertTrue(value['logWindow']['available'])
        self.assertFalse(value['active']['managed'])

    def test_cookie_seen_throttle_keeps_revocation_and_expiry_checks(self):
        with patch.object(panel_security.time, 'time', return_value=1000):
            token, _ = self.sec.login(self.sec.admin)
        headers = {'Cookie': panel_security.COOKIE + '=' + token}
        sid = panel_security.digest(token)
        for now in (1001, 1020, 1059):
            with patch.object(panel_security.time, 'time', return_value=now):
                self.sec.authenticate(headers)
        with self.sec.db() as con:
            self.assertEqual(con.execute('SELECT seen FROM sessions WHERE id=?', (sid,)).fetchone()[0], 1000)
        with patch.object(panel_security.time, 'time', return_value=1060):
            identity = self.sec.authenticate(headers)
        with self.sec.db() as con:
            self.assertEqual(con.execute('SELECT seen FROM sessions WHERE id=?', (sid,)).fetchone()[0], 1060)
        self.sec.logout(identity)
        with self.assertRaises(panel_security.AccessError):
            self.sec.authenticate(headers)

    def test_cookie_idle_and_absolute_expiry_remain_enforced(self):
        for elapsed in (panel_security.IDLE_TTL + 1, panel_security.MAX_TTL + 1):
            with patch.object(panel_security.time, 'time', return_value=1000):
                token, _ = self.sec.login(self.sec.admin)
            if elapsed > panel_security.MAX_TTL:
                with self.sec.db() as con:
                    con.execute('UPDATE sessions SET seen=? WHERE id=?',
                                (1000 + elapsed - 1, panel_security.digest(token)))
            with patch.object(panel_security.time, 'time', return_value=1000 + elapsed):
                with self.assertRaises(panel_security.AccessError):
                    self.sec.authenticate({'Cookie': panel_security.COOKIE + '=' + token})

    def test_runtime_policy_fifo_is_rejected_without_blocking(self):
        os.mkfifo(self.root / 'runtime-policy.json')
        with self.assertRaises(ValueError):
            app.runtime_policy()
