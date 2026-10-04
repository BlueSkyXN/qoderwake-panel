import importlib.util
import json
import os
from pathlib import Path
import socket
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location(
    'process_control', ROOT / 'ops/process-control.py')
pc = importlib.util.module_from_spec(spec)
spec.loader.exec_module(pc)


class ProcessControlTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.root = Path(self.tmp.name).resolve()
        self.proc = self.root / 'proc'
        (self.proc / 'sys/kernel/random').mkdir(parents=True)
        (self.proc / 'sys/kernel/random/boot_id').write_text(
            '00000000-0000-4000-8000-000000000001\n')
        (self.proc / 'net').mkdir()
        (self.proc / 'net/tcp').write_text(
            '  sl  local_address rem_address st tx_queue rx_queue tr tm->when retrnsmt uid timeout inode\n')
        (self.proc / 'net/tcp6').write_text(
            '  sl  local_address rem_address st tx_queue rx_queue tr tm->when retrnsmt uid timeout inode\n')
        self.launch = self.root / 'python3'
        self.launch.write_bytes(b'python-fixture')
        self.launch.chmod(0o755)
        self.script = self.root / 'panel.py'
        self.script.write_bytes(b'print("fixture")')
        self.script.chmod(0o644)
        self.home = self.root / 'home'
        self.home.mkdir()
        self.port = 19831
        self.controller = pc.Controller(
            self.root, 'panel', 'panel', self.launch, self.home,
            self.port, 'panel', self.root / 'logs/panel.log',
            'http://127.0.0.1:%d/api/health' % self.port,
            script=self.script, proc_root=self.proc,
            expected_version='0.12.0')
        self.pid = 4321
        self.process = self.proc / str(self.pid)
        (self.process / 'fd').mkdir(parents=True)
        (self.process / 'exe').symlink_to(self.launch)
        cmdline = [str(self.launch), str(self.script)]
        (self.process / 'cmdline').write_bytes(
            b'\0'.join(os.fsencode(item) for item in cmdline) + b'\0')
        (self.process / 'stat').write_text(
            '%d (panel worker) %s\n' % (
                self.pid, ' '.join(['S'] + ['0'] * 18 + ['987654'] + ['0'] * 5)))
        (self.process / 'status').write_text(
            'Name:\tpanel\nUid:\t%d\t%d\t%d\t%d\n' % ((os.geteuid(),) * 4))
        environment = self.controller.environment()
        (self.process / 'environ').write_bytes(
            b'\0'.join(os.fsencode(key + '=' + value)
                       for key, value in environment.items()) + b'\0')
        self.socket_inode = '123456'
        (self.process / 'fd/7').symlink_to(
            'socket:[%s]' % self.socket_inode)
        self.set_listener(self.socket_inode)

    def tearDown(self):
        self.tmp.cleanup()

    def set_listener(self, inode):
        line = ('   0: 0100007F:%04X 00000000:0000 0A '
                '00000000:00000000 00:00000000 00000000 0 0 %s\n') % (
                    self.port, inode)
        (self.proc / 'net/tcp').write_text(
            '  sl  local_address rem_address st tx_queue rx_queue tr tm->when retrnsmt uid timeout inode\n' + line)

    def state(self, status='running', version='0.12.0'):
        return self.controller.capture(
            self.pid, status=status, version=version)

    def test_exact_identity_and_owned_listener_are_required(self):
        state = self.state()
        self.assertEqual(pc.identity_status(state, self.proc), 'match')
        self.assertEqual(pc.port_status(state, self.proc), 'owned')
        self.set_listener('999999')
        self.assertEqual(pc.port_status(state, self.proc), 'other')
        (self.proc / 'net/tcp').unlink()
        self.assertEqual(pc.port_status(state, self.proc), 'unknown')

    def test_pid_reuse_home_and_command_mismatch_are_detected(self):
        state = self.state()
        (self.process / 'stat').write_text(
            '%d (panel worker) %s\n' % (
                self.pid, ' '.join(['S'] + ['0'] * 18 + ['987655'] + ['0'] * 5)))
        self.assertEqual(pc.identity_status(state, self.proc), 'mismatch')
        (self.process / 'stat').unlink()
        self.assertEqual(pc.identity_status(state, self.proc), 'unknown')
        __import__('shutil').rmtree(self.process)
        self.assertEqual(pc.identity_status(state, self.proc), 'exited')

    def test_stop_waits_on_pidfd_and_rechecks_port_before_removing_state(self):
        state = self.state()
        with patch.object(self.controller, 'read_state', return_value=state), \
                patch.object(pc, 'identity_status', side_effect=['match', 'match']) as identity, \
                patch.object(pc, 'port_status', return_value='owned'), \
                patch.object(pc, 'listening_socket_inodes', return_value=set()), \
                patch.object(pc.os, 'pidfd_open', return_value=99, create=True), \
                patch.object(pc.signal, 'pidfd_send_signal', create=True), \
                patch.object(pc.select, 'select', return_value=([99], [], [])), \
                patch.object(pc.os, 'close'), \
                patch.object(self.controller, 'remove_state') as remove:
            self.assertTrue(self.controller.stop()['stopped'])
        self.assertEqual(identity.call_count, 2)
        remove.assert_called_once()

    def test_missing_proc_root_is_unknown_not_exited(self):
        self.assertEqual(pc.identity_status(self.state(), self.root / 'missing-proc'), 'unknown')

    def test_script_replacement_invalidates_saved_identity(self):
        state = self.state()
        replacement = self.root / 'replacement.py'
        replacement.write_bytes(b'changed')
        os.replace(replacement, self.script)
        self.assertEqual(pc.identity_status(state, self.proc), 'mismatch')

    def test_state_schema_binds_profile_mode_home_and_environment(self):
        state = self.state()
        for key, value in (
                ('home', '/other'), ('mode', 'gateway'),
                ('environment', dict(state['environment'], SECRET='private'))):
            invalid = dict(state, **{key: value})
            with self.assertRaises(ValueError):
                pc.validate_state(invalid)
        invalid = dict(state, cmdline=[str(self.launch), 'other.py'])
        with self.assertRaisesRegex(ValueError, 'invalid_process_command'):
            pc.validate_state(invalid)

    def test_status_requires_version_and_owned_port(self):
        state = self.state()
        self.controller.directory.mkdir(parents=True)
        self.controller.write_state(state)
        with patch.object(pc, 'health_version', return_value='0.12.0'):
            result = self.controller.status()
        self.assertTrue(result['healthy'])
        with patch.object(pc, 'health_version', return_value='0.11.0'):
            self.assertFalse(self.controller.status()['healthy'])
        self.set_listener('999999')
        with patch.object(pc, 'health_version') as health:
            self.assertFalse(self.controller.status()['healthy'])
        health.assert_not_called()

    def test_stop_never_signals_mismatch_or_unknown_identity(self):
        self.controller.directory.mkdir(parents=True)
        state = self.state()
        self.controller.write_state(state)
        (self.process / 'stat').write_text(
            '%d (panel worker) %s\n' % (
                self.pid, ' '.join(['S'] + ['0'] * 18 + ['1'] + ['0'] * 5)))
        with patch.object(pc.os, 'kill') as kill, \
                self.assertRaisesRegex(ValueError, 'identity_unknown'):
            self.controller.stop()
        kill.assert_not_called()
        (self.process / 'stat').unlink()
        with patch.object(pc.os, 'kill') as kill, \
                self.assertRaisesRegex(ValueError, 'identity_unknown'):
            self.controller.stop()
        kill.assert_not_called()

    def test_missing_state_with_foreign_listener_fails_closed(self):
        with self.assertRaisesRegex(ValueError, 'unmanaged_process_port_occupied'):
            self.controller.stop()
        with self.assertRaisesRegex(ValueError, 'unmanaged_process_port_occupied'):
            self.controller.start()

    def test_stop_requires_pidfd_after_exact_identity_match(self):
        self.controller.directory.mkdir(parents=True)
        self.controller.write_state(self.state())
        with patch.object(pc.os, 'pidfd_open', side_effect=OSError('fixture'),
                          create=True), \
                patch.object(pc.signal, 'pidfd_send_signal', create=True), \
                patch.object(pc.os, 'kill') as kill, \
                self.assertRaisesRegex(ValueError, 'pidfd_required'):
            self.controller.stop()
        kill.assert_not_called()

    def test_child_environment_does_not_inherit_proxy_or_secrets(self):
        with patch.dict(os.environ, {
                'HTTPS_PROXY': 'http://proxy.invalid',
                'PRIVATE_TOKEN': 'secret',
                'QW_ROOT': str(self.root),
                'QW_HOME': str(self.home)}, clear=False):
            environment = self.controller.child_environment()
        self.assertNotIn('HTTPS_PROXY', environment)
        self.assertNotIn('PRIVATE_TOKEN', environment)
        self.assertEqual(environment['QW_ROOT'], str(self.root))
        self.assertEqual(environment['QW_HOME'], str(self.home))

    def test_gateway_daemon_requires_complete_loopback_endpoint_family(self):
        daemon = self.root / 'qoderwake-cn'
        daemon.write_bytes(b'daemon')
        daemon.chmod(0o755)
        controller = pc.Controller(
            self.root, 'daemon-cn', 'daemon', daemon, self.home,
            19830, 'gateway', self.root / 'logs/daemon.log',
            'http://127.0.0.1:19830/api/health',
            endpoint='http://127.0.0.1:19840', proc_root=self.proc)
        with patch.dict(os.environ, {}, clear=True):
            environment = controller.environment()
        self.assertEqual(
            {environment[key] for key in pc.GATEWAY_ENV},
            {'http://127.0.0.1:19840'})
        invalid = dict(self.state(), profile='daemon', name='daemon-cn',
                       script=None, mode='gateway',
                       endpoint='http://127.0.0.1:19840',
                       cmdline=[str(self.launch), 'start', '--foreground'],
                       home=str(self.home), environment={
                           'QODERWAKE_HOME': str(self.home),
                           'QODERWAKE_ENDPOINT_BASE_URL': 'http://127.0.0.1:19840'
                       })
        with self.assertRaisesRegex(ValueError, 'invalid_process_environment'):
            pc.validate_state(invalid)

    def test_read_state_rejects_symlink(self):
        target = self.root / 'state.json'
        target.write_text(json.dumps(self.state()))
        self.controller.directory.mkdir(parents=True)
        self.controller.state_file.symlink_to(target)
        with self.assertRaises(ValueError):
            self.controller.read_state()


if __name__ == '__main__':
    unittest.main()
