import concurrent.futures
import importlib.util
import json
import os
from pathlib import Path
import sqlite3
import subprocess
import sys
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

    def test_cursor_migration_recovers_partial_columns_and_preserves_history(self):
        for index, extras in enumerate(((), ('size',), ('mtime_ns',), ('mtime_ns', 'size'))):
            with self.subTest(existing_columns=extras):
                self.database = self.root / f'usage-{index}.db'
                self.path.write_text(self.line * 2)
                self.assertEqual(self.count(self.read()), 2)
                columns = ('path', 'device', 'inode', 'offset', 'prefix', 'suffix',
                           'discarding', 'skipped', *extras)
                with sqlite3.connect(self.database) as con:
                    con.execute('ALTER TABLE usage_cursors RENAME TO prior_cursors')
                    con.execute('CREATE TABLE usage_cursors(path TEXT PRIMARY KEY, '
                                'device INTEGER, inode INTEGER, offset INTEGER, '
                                'prefix TEXT, suffix TEXT, discarding INTEGER, skipped INTEGER' +
                                ''.join(f', {name} INTEGER' for name in extras) + ')')
                    names = ','.join(columns)
                    con.execute(f'INSERT INTO usage_cursors({names}) SELECT {names} FROM prior_cursors')
                    con.execute('DROP TABLE prior_cursors')
                self.assertEqual(self.count(self.read()), 2)
                self.assertEqual(self.count(self.read()), 2)
                with self.path.open('a') as stream:
                    stream.write(self.line)
                self.assertEqual(self.count(self.read()), 3)
                with sqlite3.connect(self.database) as con:
                    row = con.execute('SELECT size,mtime_ns FROM usage_cursors').fetchone()
                self.assertEqual(row, (self.path.stat().st_size, self.path.stat().st_mtime_ns))

    def test_unchanged_cursor_skips_content_reads_and_database_writes(self):
        self.path.write_text(self.line)
        self.read()
        with sqlite3.connect(self.database) as con, \
                patch.object(usage_store, 'guards', side_effect=AssertionError('unchanged log read')):
            before = con.total_changes
            value = usage_store.scan_file(con, self.path, usage_store.MAX_FILE_BYTES)
            self.assertEqual(con.total_changes, before)
        self.assertEqual(value['bytesRead'], 0)
        self.assertEqual(value['guardBytesRead'], 0)
        self.assertFalse(value['pending'])

    def test_server_scans_usage_without_an_initial_page_request(self):
        daemon_home = self.root / 'home'
        log = daemon_home / 'qodercli/logs/runs/one/qodercli.log'
        log.parent.mkdir(parents=True)
        log.write_text(self.line)
        process = subprocess.Popen([sys.executable, '-B', str(ROOT / 'panel/qoderwake-panel.py')],
            env={**os.environ, 'QW_ROOT': str(self.root), 'QW_HOME': str(daemon_home),
                 'QW_BIND': '127.0.0.1', 'QW_PORT': '0', 'QW_TLS_CERT': '', 'QW_TLS_KEY': ''},
            stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
        try:
            count = 0
            deadline = time.monotonic() + 10
            while time.monotonic() < deadline:
                if process.poll() is not None:
                    self.fail('Panel exited before its background scan')
                if self.database.exists():
                    try:
                        with sqlite3.connect(self.database) as con:
                            count = con.execute('SELECT COUNT(*) FROM usage_events').fetchone()[0]
                    except sqlite3.OperationalError:
                        pass
                if count:
                    break
                time.sleep(0.05)
            self.assertEqual(count, 1)
        finally:
            if process.poll() is None:
                process.terminate()
            process.communicate(timeout=10)

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


class UsageCacheTests(unittest.TestCase):
    def test_concurrent_cold_requests_do_not_start_background_scanners(self):
        original_thread = threading.Thread
        barrier = threading.Barrier(2)
        results = []

        def scan():
            barrier.wait(timeout=3)
            return {'scan': {'complete': True}}

        with patch.dict(app.USAGE_CACHE, {'at': 0, 'data': None, 'db': None, 'probe': None}, clear=True), \
                patch.object(app, 'usage_scan_once', side_effect=scan), \
                patch.object(app.threading, 'Thread') as spawned:
            callers = [original_thread(target=lambda: results.append(app.usage_data())) for _ in range(2)]
            for caller in callers:
                caller.start()
            for caller in callers:
                caller.join(timeout=5)
                self.assertFalse(caller.is_alive())
            self.assertEqual(len(results), 2)
            spawned.assert_not_called()


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

    def test_runtime_sdkbyok_policy_env_and_default_inherit(self):
        body = {'hotDeploy': False, 'embeddingDisabled': True, 'sdkByok': False, 'confirm': '/api/runtime/policy'}
        self.assertEqual(self.request('POST', '/api/runtime/policy', body, token=self.sec.admin)[0], 200)
        self.assertIs(app.runtime_policy()['sdkByok'], False)
        with patch.dict(os.environ, {}, clear=True):
            self.assertEqual(app.runtime_env()[app.SDK_BYOK_SWITCH], '0')
        (self.root / 'runtime-policy.json').unlink()
        with patch.dict(os.environ, {}, clear=True):
            self.assertEqual(app.runtime_env()[app.SDK_BYOK_SWITCH], '1')
        self.assertIs(app.runtime_state()['effectiveOnPanelRestart']['sdkByok'], True)
        body['sdkByok'] = 'false'
        self.assertEqual(self.request('POST', '/api/runtime/policy', body, token=self.sec.admin)[0], 400)
        self.assertFalse((self.root / 'runtime-policy.json').exists())

    def test_runtime_sdkbyok_observed_and_pending_flag(self):
        state = {'pid': os.getpid(), 'mode': 'direct', 'port': 19830}
        with patch.object(app, 'daemon_process_state', return_value=state), \
                patch.object(app, 'read_daemon_environ', return_value={}):
            value = app.runtime_state()
        self.assertEqual(len(value['observed']), 1)
        self.assertIsNone(value['observed'][0]['sdkByok'])
        self.assertIs(value['sdkByokPending'], True)
        with patch.object(app, 'daemon_process_state', return_value=state), \
                patch.object(app, 'read_daemon_environ',
                             return_value={app.SDK_BYOK_SWITCH.encode(): b'1'}):
            value = app.runtime_state()
        self.assertIs(value['observed'][0]['sdkByok'], True)
        self.assertIs(value['sdkByokPending'], False)

    def test_telemetry_route_writes_via_cli_and_validates(self):
        calls = []

        class Ok:
            returncode = 0
            stdout = ''
            stderr = ''

        class Fail:
            returncode = 1
            stdout = ''
            stderr = 'boom'

        def fake_cli(*args, timeout=30):
            calls.append(args)
            return Ok()

        with patch.object(app, 'cli', side_effect=fake_cli):
            status, _, body = self.request('POST', '/api/telemetry',
                                           {'enabled': False, 'confirm': '/api/telemetry'},
                                           token=self.sec.admin)
        self.assertEqual(status, 200)
        self.assertTrue(json.loads(body)['ok'])
        self.assertEqual(calls[0], ('config', 'set', 'telemetry', 'false'))
        with patch.object(app, 'cli', return_value=Fail()):
            status, _, body = self.request('POST', '/api/telemetry',
                                           {'enabled': True, 'confirm': '/api/telemetry'},
                                           token=self.sec.admin)
        result = json.loads(body)
        self.assertEqual(status, 502)
        self.assertFalse(result['ok'])
        self.assertEqual(result['message'], 'boom')
        self.assertEqual(self.request('POST', '/api/telemetry',
                                      {'enabled': 'false', 'confirm': '/api/telemetry'},
                                      token=self.sec.admin)[0], 400)
        self.assertEqual(self.request('POST', '/api/telemetry',
                                      {'enabled': False},
                                      token=self.sec.admin)[0], 409)

    def test_telemetry_display_reads_daemon_config_json(self):
        config = self.root / 'home' / 'config'
        config.mkdir(parents=True)
        (config / 'config.json').write_text('{"telemetry": false, "update": {}}')
        with patch.object(app, 'cached_state', return_value={}):
            value = app.panel_state('admin')
        self.assertIs(value['telemetry'], False)
        (config / 'config.json').write_text('{"telemetry": "maybe"}')
        with patch.object(app, 'cached_state', return_value={}):
            value = app.panel_state('admin')
        self.assertIsNone(value['telemetry'])

    def test_provider_probe_403_names_byok_causes(self):
        import urllib.error
        import provider_transport
        err = urllib.error.HTTPError('https://api.example.com/v1', 403, 'Forbidden', None, None)
        with patch.object(provider_transport, 'probe', side_effect=err):
            ok, msg = app.test_provider('https://api.example.com/v1', 'key')
        self.assertFalse(ok)
        self.assertIn('403', msg)
        self.assertIn('BYOK', msg)
        self.assertIn('同名冲突', msg)
