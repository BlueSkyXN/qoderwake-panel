import http.server
import importlib.util
import json
import os
from pathlib import Path
import shutil
import socket
import ssl
import subprocess
import sys
import tempfile
import threading
import unittest
from unittest.mock import patch

import test_deletion_preflight as deletion_tests
import test_gateway_manager as gateway_tests
import test_panel
import test_process_control as process_tests

ROOT = Path(__file__).resolve().parents[1]
app = test_panel.app
pc = process_tests.pc
gm = gateway_tests.gm


class ProcessSafetyTests(unittest.TestCase):
    def setUp(self):
        self.fixture = process_tests.ProcessControlTests()
        self.fixture.setUp()
        self.controller = self.fixture.controller

    def tearDown(self):
        self.fixture.tearDown()

    def save_state(self):
        self.controller.directory.mkdir(parents=True, exist_ok=True)
        state = self.fixture.state()
        self.controller.write_state(state)
        return state

    def test_explicit_uplink_disables_survive_both_child_environments(self):
        values = {key: '0' for key in pc.UPLINK_ENV}
        with patch.dict(os.environ, dict(values, HTTPS_PROXY='http://example.invalid',
                                        PRIVATE_TOKEN='synthetic'), clear=True):
            panel = self.controller.child_environment()
            daemon = pc.Controller(
                self.fixture.root, 'daemon-cn', 'daemon', self.fixture.launch,
                self.fixture.home, 19830, 'direct', self.fixture.root / 'daemon.log',
                'http://127.0.0.1:19830/api/health', proc_root=self.fixture.proc)
            environments = (panel, daemon.child_environment())
        for environment in environments:
            self.assertEqual({key: environment[key] for key in values}, values)
            self.assertNotIn('HTTPS_PROXY', environment)
            self.assertNotIn('PRIVATE_TOKEN', environment)

    def test_invalid_uplink_value_is_rejected_before_start(self):
        for key in pc.UPLINK_ENV:
            with patch.dict(os.environ, {key: 'disabled'}, clear=True):
                with self.assertRaisesRegex(ValueError, 'invalid_uplink_switch'):
                    self.controller.environment()

    def test_restart_cannot_silently_drop_saved_uplink_disable(self):
        state = self.save_state()
        for key in pc.UPLINK_ENV:
            state['environment'][key] = '0'
        self.controller.write_state(state)
        with patch.dict(os.environ, {}, clear=True), \
                patch.object(self.controller, 'stop') as stop, \
                patch.object(pc.subprocess, 'Popen') as start:
            with self.assertRaisesRegex(ValueError, 'uplink_disable_not_preserved'):
                self.controller.start()
        stop.assert_not_called()
        start.assert_not_called()

    def test_status_stop_start_reject_mismatched_instance(self):
        self.save_state()
        for attribute, value in (
                ('home', self.fixture.root / 'another-home'),
                ('port', 19999), ('launch', self.fixture.root / 'another-python'),
                ('script', self.fixture.root / 'another-script.py'),
                ('mode', 'direct'), ('endpoint', 'http://127.0.0.1:19840')):
            with self.subTest(attribute=attribute), \
                    patch.object(self.controller, attribute, value), \
                    patch.object(pc, 'health_version') as health, \
                    patch.object(pc.signal, 'pidfd_send_signal', create=True) as send, \
                    patch.object(pc.subprocess, 'Popen') as start:
                for action in ('status', 'stop', 'start'):
                    with self.assertRaisesRegex(ValueError, 'instance_mismatch'):
                        getattr(self.controller, action)()
                health.assert_not_called()
                send.assert_not_called()
                start.assert_not_called()

    def test_unknown_or_conflicting_port_never_signals(self):
        state = self.save_state()
        for ownership in ('unknown', 'other'):
            with self.subTest(ownership=ownership), \
                    patch.object(pc, 'identity_status', return_value='match'), \
                    patch.object(pc, 'port_status', return_value=ownership), \
                    patch.object(pc.os, 'pidfd_open', create=True) as open_pid, \
                    patch.object(pc.signal, 'pidfd_send_signal', create=True) as send:
                with self.assertRaises(ValueError):
                    self.controller.stop()
                open_pid.assert_not_called()
                send.assert_not_called()
        self.assertEqual(self.controller.read_state(), state)

    def test_socket_ownership_is_rechecked_after_opening_pidfd(self):
        self.save_state()
        with patch.object(pc, 'identity_status', return_value='match'), \
                patch.object(pc, 'port_status', side_effect=['owned', 'unknown']), \
                patch.object(pc.os, 'pidfd_open', return_value=99, create=True), \
                patch.object(pc.signal, 'pidfd_send_signal', create=True) as send, \
                patch.object(pc.os, 'close'):
            with self.assertRaisesRegex(ValueError, 'visibility_required'):
                self.controller.stop()
        send.assert_not_called()

    def test_verified_unbound_process_can_be_cleaned_up(self):
        self.save_state()
        with patch.object(pc, 'identity_status', return_value='match'), \
                patch.object(pc, 'port_status', return_value='unbound'), \
                patch.object(pc, 'listening_socket_inodes', return_value=set()), \
                patch.object(pc.os, 'pidfd_open', return_value=99, create=True), \
                patch.object(pc.signal, 'pidfd_send_signal', create=True) as send, \
                patch.object(pc.select, 'select', return_value=([99], [], [])), \
                patch.object(pc.os, 'close'):
            self.assertTrue(self.controller.stop()['stopped'])
        send.assert_called_once()


class GatewaySafetyTests(unittest.TestCase):
    def setUp(self):
        self.fixture = gateway_tests.ManagerTests()
        self.fixture.setUp()
        self.manager = self.fixture.manager
        self.state = self.fixture.state(self.fixture.snapshot())

    def tearDown(self):
        self.fixture.tearDown()

    def test_unknown_or_conflicting_port_never_signals(self):
        for ownership in ('unknown', 'other'):
            with self.subTest(ownership=ownership), \
                    patch.object(self.manager, 'process_status', return_value='match'), \
                    patch.object(gm, 'gateway_port_status', return_value=ownership), \
                    patch.object(gm.os, 'pidfd_open', create=True) as open_pid, \
                    patch.object(gm.signal, 'pidfd_send_signal', create=True) as send:
                with self.assertRaisesRegex(ValueError, 'listener_identity_unknown'):
                    self.manager.stop(self.state)
                open_pid.assert_not_called()
                send.assert_not_called()

    def test_port_is_rechecked_after_opening_pidfd(self):
        with patch.object(self.manager, 'process_status', return_value='match'), \
                patch.object(gm, 'gateway_port_status', side_effect=['owned', 'unknown']), \
                patch.object(gm.os, 'pidfd_open', return_value=99, create=True), \
                patch.object(gm.signal, 'pidfd_send_signal', create=True) as send, \
                patch.object(gm.os, 'close'):
            with self.assertRaisesRegex(ValueError, 'listener_identity_unknown'):
                self.manager.stop(self.state)
        send.assert_not_called()


class DeletionSafetyTests(unittest.TestCase):
    def test_malformed_group_members_block_both_deletion_kinds(self):
        for member in ({}, None, [], '', '  ', 'local:',
                       {'wakerKey': 'w1', 'agentId': 'w2'},
                       {'wakerKey': [], 'agentId': 'w1'}):
            for position in ('member', 'leader'):
                source = deletion_tests.Sources()
                source.data['groups'] = [{
                    'group': {'id': 'g1'},
                    'members': [member] if position == 'member' else ['w2'],
                    'leader': member if position == 'leader' else 'w2'
                }]
                for kind, target, key in (
                        ('provider', 'provider', 'group_model'),
                        ('waker', 'w1', 'group_membership')):
                    with self.subTest(member=member, position=position, kind=kind):
                        result = app.DeletionPreflight(source).scan(kind, target)
                        checks = {row['id']: row for row in result['checks']}
                        self.assertEqual(checks[key]['status'], 'unknown')
                        self.assertFalse(result['deletable'])

    def test_unknown_group_model_schema_blocks_provider_deletion(self):
        source = deletion_tests.Sources()
        source.data['groups'] = [{
            'group': {'id': 'g1'},
            'members': [{'wakerKey': 'w1', 'futureModel': {'key': 'provider/model'}}],
            'leader': {'wakerKey': 'w2'}
        }]
        result = app.DeletionPreflight(source).scan('provider', 'provider')
        self.assertFalse(result['deletable'])
        self.assertEqual(next(check for check in result['checks']
                              if check['id'] == 'group_model')['status'], 'unknown')

    def test_group_detail_must_match_the_requested_id(self):
        for detail in ({}, {'group': None},
                       {'group': {'id': 'other'}, 'members': [], 'leader': 'w2'},
                       {'group': {'id': 'g1', 'groupId': 'other'}, 'members': [], 'leader': 'w2'}):
            with patch.object(app, 'daemon_json', side_effect=[
                    {'data': {'groups': [{'id': 'g1'}]}}, {'data': detail}]):
                with self.assertRaisesRegex(ValueError, 'invalid_group_detail'):
                    app.deletion_groups()
        detail = {'group': {'id': 'g1'}, 'members': [], 'leader': 'w2'}
        with patch.object(app, 'daemon_json', side_effect=[
                {'data': {'groups': [{'id': 'g1'}]}}, {'data': detail}]):
            self.assertEqual(app.deletion_groups(), [detail])


class PanelSafetyTests(unittest.TestCase):
    setUp = test_panel.PanelTests.setUp
    tearDown = test_panel.PanelTests.tearDown
    request = test_panel.PanelTests.request

    def test_malformed_existing_provider_is_read_only_over_http(self):
        for old in ({}, None, False, []):
            original = json.dumps({'providers': {'p': old}}).encode()
            app.SETTINGS.write_bytes(original)
            revision = app.provider_store().read()['revision']
            status, _, raw = self.request('POST', '/api/provider', {
                'name': 'p', 'baseUrl': 'https://example.invalid/v1',
                'apiKey': 'synthetic-key', 'model': 'm', 'display': 'm',
                'baseRevision': revision}, token=self.sec.admin)
            self.assertEqual(status, 400)
            self.assertEqual(json.loads(raw)['error'], 'provider_requires_official_editor')
            self.assertEqual(app.SETTINGS.read_bytes(), original)
            self.assertFalse((self.root / 'provider-settings.previous.json').exists())

    def test_group_becoming_malformed_blocks_fresh_scan_and_preserves_provider(self):
        original = json.dumps({'providers': {'provider': {
            'type': 'openai-compatible', 'authType': 'bearer',
            'apiKey': 'synthetic-key', 'baseUrl': 'https://example.invalid/v1',
            'models': [{'model': 'model'}]}}}).encode()
        app.SETTINGS.write_bytes(original)
        revision = app.provider_store().read()['revision']
        source = deletion_tests.Sources()
        with patch.object(app, 'DeletionSources', return_value=source):
            status, _, raw = self.request('POST', '/api/deletion/preview', {
                'kind': 'provider', 'target': 'provider', 'baseRevision': revision},
                token=self.sec.admin)
            self.assertEqual(status, 200)
            preview = json.loads(raw)
            self.assertTrue(preview['deletable'])
            source.data['groups'] = [{'group': {'id': 'g1'}, 'members': [{}], 'leader': {}}]
            status, _, raw = self.request('POST', '/api/provider/delete', {
                'name': 'provider', 'baseRevision': revision, 'preview': preview['id'],
                'impactDigest': preview['impactDigest'],
                'acknowledgedWarnings': preview['warningsRequired'],
                'confirm': '/api/provider/delete'}, token=self.sec.admin,
                headers={'Idempotency-Key': 'malformed-group-delete'})
            self.assertEqual(status, 409)
            self.assertEqual(json.loads(raw)['error'], 'stale_deletion_preview')
        self.assertEqual(app.SETTINGS.read_bytes(), original)
        self.assertFalse(self.sec.has_unresolved_deletions())


class RuntimeIdentityTests(unittest.TestCase):
    def setUp(self):
        self.fixture = process_tests.ProcessControlTests()
        self.fixture.setUp()
        fixture = self.fixture
        state = fixture.state()
        environment = {'QODERWAKE_HOME': str(fixture.home),
                       'QODERWAKE_HOT_DEPLOY': '0', 'QODER_MEMORY_DISABLE_EMBEDDING': '1'}
        state.update(name='daemon-cn', profile='daemon', mode='direct', script=None,
                     environment=environment,
                     cmdline=[str(fixture.launch), 'start', '--foreground'])
        (fixture.process / 'cmdline').write_bytes(
            b'\0'.join(os.fsencode(value) for value in state['cmdline']) + b'\0')
        self.environment_bytes = b'\0'.join(
            os.fsencode(key + '=' + value) for key, value in environment.items()) + b'\0'
        (fixture.process / 'environ').write_bytes(self.environment_bytes)
        fixture.controller.directory.mkdir(parents=True)
        pc.atomic(fixture.controller.directory / 'daemon-cn.json', json.dumps(state).encode())
        self.original_identity = pc.identity_status
        self.original_port = pc.port_status
        self.patches = [
            patch.multiple(app, ROOT=fixture.root, HOME=fixture.home,
                           DAEMON_BIN=str(fixture.launch),
                           DAEMON='http://127.0.0.1:%d' % fixture.port),
            patch.object(app, 'process_control', return_value=pc),
            patch.object(pc, 'identity_status', side_effect=lambda value:
                         self.original_identity(value, fixture.proc)),
            patch.object(pc, 'port_status', side_effect=lambda value:
                         self.original_port(value, fixture.proc))
        ]
        for change in self.patches:
            change.start()

    def tearDown(self):
        for change in reversed(self.patches):
            change.stop()
        self.fixture.tearDown()

    def test_state_requires_full_identity_and_owned_socket(self):
        self.assertIsNotNone(app.daemon_process_state())
        self.fixture.set_listener('999999')
        self.assertIsNone(app.daemon_process_state())
        self.fixture.set_listener(self.fixture.socket_inode)
        (self.fixture.process / 'environ').write_bytes(b'QODERWAKE_HOME=/another-home\0')
        self.assertIsNone(app.daemon_process_state())
        value = app.runtime_state()
        self.assertEqual(value['observed'], [])
        self.assertTrue(value['pendingRestart'])

    def test_pid_reuse_is_unknown_not_applied(self):
        path = self.fixture.process / 'stat'
        path.write_text(path.read_text().replace('987654', '987655'))
        self.assertIsNone(app.daemon_process_state())
        self.assertTrue(app.runtime_state()['pendingRestart'])

    def test_environment_read_is_bracketed_by_identity_checks(self):
        state = app.daemon_process_state()
        original = Path.read_bytes
        def read_bytes(path):
            if str(path) == '/proc/%s/environ' % state['pid']:
                return self.environment_bytes
            return original(path)
        with patch.object(Path, 'read_bytes', read_bytes), \
                patch.object(app, 'daemon_process_state', side_effect=[state, None]):
            self.assertEqual(app.runtime_state()['observed'], [])
        with patch.object(Path, 'read_bytes', read_bytes):
            value = app.runtime_state()
        self.assertEqual(value['observed'], [{'hotDeploy': False, 'embeddingDisabled': True}])
        self.assertFalse(value['pendingRestart'])


class HealthHandler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        body = b'{"ok":true,"version":"0.12.1"}'
        self.send_response(200)
        self.send_header('Content-Length', str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *args):
        pass


class TLSHealthTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.root = Path(self.tmp.name).resolve()
        self.cert, self.key = self.root / 'cert.pem', self.root / 'key.pem'
        subprocess.run(['openssl', 'req', '-x509', '-newkey', 'rsa:2048', '-nodes',
                        '-keyout', str(self.key), '-out', str(self.cert), '-days', '1',
                        '-subj', '/CN=panel.example.test'], check=True, capture_output=True)
        self.server = http.server.ThreadingHTTPServer(('127.0.0.1', 0), HealthHandler)
        context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
        context.load_cert_chain(self.cert, self.key)
        self.server.socket = context.wrap_socket(self.server.socket, server_side=True)
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()
        self.url = 'https://127.0.0.1:%d/api/health' % self.server.server_port

    def tearDown(self):
        self.server.shutdown()
        self.server.server_close()
        self.thread.join()
        self.tmp.cleanup()

    def test_tls_health_requires_trusted_certificate_and_matching_name(self):
        self.assertEqual(pc.health_version(self.url, ca_file=str(self.cert),
                                          server_name='panel.example.test'), '0.12.1')
        self.assertIsNone(pc.health_version(self.url, server_name='panel.example.test'))
        self.assertIsNone(pc.health_version(self.url, ca_file=str(self.cert),
                                           server_name='wrong.example.test'))
        self.assertIsNone(pc.health_version(self.url.replace('https:', 'http:')))

    def test_controller_accepts_only_loopback_health_and_preserves_tls_options(self):
        controller = pc.Controller(
            self.root, 'panel', 'panel', sys.executable, self.root / 'home',
            self.server.server_port, 'panel', self.root / 'panel.log', self.url,
            script=ROOT / 'panel/qoderwake-panel.py', health_ca_file=str(self.cert),
            health_server_name='panel.example.test')
        self.assertEqual(controller.health(), '0.12.1')
        for url in (self.url + '?extra=1', self.url.replace('127.0.0.1', 'example.test')):
            with self.assertRaisesRegex(ValueError, 'invalid_process_health_url'):
                pc.Controller(self.root, 'panel', 'panel', sys.executable,
                              self.root / 'home', self.server.server_port, 'panel',
                              self.root / 'panel.log', url,
                              script=ROOT / 'panel/qoderwake-panel.py')

    @unittest.skipUnless(Path('/proc').is_dir(), 'Linux managed TLS integration')
    def test_real_managed_tls_panel_start_status_stop(self):
        with socket.socket() as sock:
            sock.bind(('127.0.0.1', 0))
            port = sock.getsockname()[1]
        data = self.root / 'managed-data'
        data.mkdir()
        home = self.root / 'isolated-home'
        home.mkdir()
        source = self.root / 'source'
        shutil.copytree(ROOT / 'panel', source / 'panel', ignore=shutil.ignore_patterns('__pycache__'))
        (source / 'ops').mkdir()
        shutil.copyfile(ROOT / 'ops/process-control.py', source / 'ops/process-control.py')
        controller = pc.Controller(
            data, 'panel', 'panel', sys.executable, home, port, 'panel', data / 'panel.log',
            'https://127.0.0.1:%d/api/health' % port,
            script=source / 'panel/qoderwake-panel.py', expected_version='0.12.1',
            health_ca_file=str(self.cert), health_server_name='panel.example.test')
        environment = {'QW_BIND': '127.0.0.1', 'QW_TLS_CERT': str(self.cert),
                       'QW_TLS_KEY': str(self.key), 'QW_DAEMON_URL': 'http://127.0.0.1:1'}
        with patch.dict(os.environ, environment, clear=True), controller.locked():
            try:
                self.assertTrue(controller.start()['ok'])
                self.assertTrue(controller.status()['healthy'])
            finally:
                if controller.read_state() is not None:
                    controller.stop()
        self.assertIsNone(controller.read_state())


if __name__ == '__main__':
    unittest.main()
