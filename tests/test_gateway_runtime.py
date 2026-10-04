import json
import os
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch

PANEL = Path(__file__).resolve().parents[1] / 'panel'
sys.path.insert(0, str(PANEL))
import gateway_runtime as runtime


class GatewayRuntimeTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.root = Path(self.tmp.name).resolve()
        self.store = self.root / 'gateway-runtime'
        self.store.mkdir()
        self.generation_dir = self.store / ('generation-' + '1' * 24)
        self.generation_dir.mkdir()
        self.port = 19849
        self.generation = runtime.generation_spec(
            self.generation_dir, 'a' * 64, 'b' * 64, 'c' * 64,
            'd' * 64, 'strict', 'openapi.qoder.com.cn', self.port,
            self.root)
        self.proc = self.root / 'proc'
        (self.proc / 'sys/kernel/random').mkdir(parents=True)
        (self.proc / 'sys/kernel/random/boot_id').write_text(
            '00000000-0000-4000-8000-000000000001\n')
        self.pid = 4321
        self.process_dir = self.proc / str(self.pid)
        self.process_dir.mkdir()
        self.interpreter = self.root / 'python3'
        self.interpreter.write_bytes(b'fixture')
        (self.process_dir / 'exe').symlink_to(self.interpreter)
        cmdline = [str(self.interpreter.resolve()), '-B',
                   str(self.generation_dir / 'uplink-gw.py')]
        (self.process_dir / 'cmdline').write_bytes(
            b'\0'.join(os.fsencode(item) for item in cmdline) + b'\0')
        (self.process_dir / 'stat').write_text(
            '%d (gateway worker) %s\n' % (
                self.pid, ' '.join(['S'] + ['0'] * 18 + ['987654'] + ['0'] * 5)))
        (self.process_dir / 'status').write_text(
            'Name:\tgateway\nUid:\t%d\t%d\t%d\t%d\n' %
            ((os.geteuid(),) * 4))
        environment = {
            'QW_GW_CONFIG': str(self.generation_dir / 'config.json'),
            'QW_GW_CONFIG_HASH': self.generation['configHash'],
            'QW_GW_PORT': str(self.port),
            'QW_ROOT': str(self.root),
            'QW_GW_UPSTREAM': self.generation['upstream'],
            'QW_GW_NONCE': '2' * 32
        }
        (self.process_dir / 'environ').write_bytes(
            b'\0'.join(
                os.fsencode(key + '=' + value)
                for key, value in environment.items()) + b'\0')
        (self.process_dir / 'fd').mkdir()
        (self.process_dir / 'fd/7').symlink_to('socket:[123456]')
        (self.proc / 'net').mkdir()
        header = ('  sl  local_address rem_address st tx_queue rx_queue tr '
                  'tm->when retrnsmt uid timeout inode\n')
        line = ('   0: 0100007F:%04X 00000000:0000 0A '
                '00000000:00000000 00:00000000 00000000 0 0 123456\n') % self.port
        (self.proc / 'net/tcp').write_text(header + line)
        (self.proc / 'net/tcp6').write_text(header)

    def tearDown(self):
        self.tmp.cleanup()

    def state(self):
        process = runtime.capture_process(
            self.pid, self.generation, os.geteuid(), self.proc)
        return runtime.validate_state({
            'schemaVersion': runtime.STATE_VERSION,
            'generation': self.generation,
            'process': process,
            'previous': None
        }, self.store, self.root, self.port)

    def test_fake_proc_matches_every_identity_field(self):
        state = self.state()
        self.assertEqual(runtime.identity_status(state, self.proc), 'match')
        process = state['process']
        self.assertEqual(process['startTicks'], 987654)
        self.assertEqual(process['uid'], os.geteuid())
        info = self.interpreter.stat()
        self.assertEqual(
            (process['executableDevice'], process['executableInode']),
            (info.st_dev, info.st_ino))
        self.assertEqual(process['cmdline'][1:], [
            '-B', str(self.generation_dir / 'uplink-gw.py')])

    def test_pid_reuse_unreadable_and_exited_are_distinct(self):
        state = self.state()
        (self.process_dir / 'stat').write_text(
            '%d (gateway worker) %s\n' % (
                self.pid, ' '.join(['S'] + ['0'] * 18 + ['987655'] + ['0'] * 5)))
        self.assertEqual(runtime.identity_status(state, self.proc), 'mismatch')
        (self.process_dir / 'stat').unlink()
        self.assertEqual(runtime.identity_status(state, self.proc), 'unknown')
        __import__('shutil').rmtree(self.process_dir)
        self.assertEqual(runtime.identity_status(state, self.proc), 'exited')

    def test_gateway_listener_must_be_loopback_and_owned_by_state_pid(self):
        state = self.state()
        self.assertEqual(
            runtime.gateway_port_status(state, self.proc), 'owned')
        (self.process_dir / 'fd/7').unlink()
        self.assertEqual(
            runtime.gateway_port_status(state, self.proc), 'other')
        (self.process_dir / 'fd/7').symlink_to('socket:[123456]')
        path = self.proc / 'net/tcp'
        path.write_text(path.read_text().replace('0100007F', '00000000'))
        self.assertEqual(
            runtime.gateway_port_status(state, self.proc), 'other')

    def test_environment_and_command_mismatch_are_detected(self):
        state = self.state()
        raw = (self.process_dir / 'environ').read_bytes()
        (self.process_dir / 'environ').write_bytes(
            raw.replace(b'QW_GW_PORT=19849', b'QW_GW_PORT=19848'))
        self.assertEqual(runtime.identity_status(state, self.proc), 'mismatch')
        (self.process_dir / 'environ').write_bytes(raw)
        (self.process_dir / 'cmdline').write_bytes(
            os.fsencode(str(self.interpreter.resolve())) + b'\0-B\0other.py\0')
        self.assertEqual(runtime.identity_status(state, self.proc), 'mismatch')

    def test_strict_state_schema_rejects_unknown_keys_and_process_in_previous(self):
        state = self.state()
        invalid = dict(state, unexpected=True)
        with self.assertRaisesRegex(ValueError, 'invalid_gateway_state'):
            runtime.validate_state(
                invalid, self.store, self.root, self.port)
        previous = dict(self.generation, process=state['process'])
        invalid = dict(state, previous=previous)
        with self.assertRaisesRegex(
                ValueError, 'invalid_gateway_generation_state'):
            runtime.validate_state(
                invalid, self.store, self.root, self.port)

    def test_state_file_rejects_symlink_and_oversized_json(self):
        state_file = self.store / 'current.json'
        target = self.root / 'state-target.json'
        target.write_text(json.dumps(self.state()))
        state_file.symlink_to(target)
        with self.assertRaises(ValueError):
            runtime.load_state(
                state_file, self.store, self.root, self.port)
        state_file.unlink()
        state_file.write_bytes(b'{' + b'x' * (1024 * 1024))
        with self.assertRaises(ValueError):
            runtime.load_state(
                state_file, self.store, self.root, self.port)

    def test_proc_generation_scan_reports_incomplete_visibility(self):
        paths, complete = runtime.proc_generation_paths(self.proc)
        self.assertTrue(complete)
        self.assertEqual(paths, {str(self.generation_dir)})
        original = Path.read_bytes
        def read_bytes(path):
            if path.name == 'environ':
                raise PermissionError('fixture')
            return original(path)
        with patch.object(Path, 'read_bytes', read_bytes):
            paths, complete = runtime.proc_generation_paths(self.proc)
        self.assertFalse(complete)
        self.assertEqual(paths, set())

    def test_null_state_and_journal_are_not_missing_files(self):
        for name, loader in (('current.json', runtime.load_state),
                             ('operation.json', runtime.load_journal)):
            path = self.store / name
            self.assertIsNone(loader(path, self.store, self.root, self.port, missing_ok=True))
            path.write_text('null')
            with self.assertRaisesRegex(ValueError, 'invalid_gateway_state_file'):
                loader(path, self.store, self.root, self.port, missing_ok=True)

    def test_missing_proc_root_is_unknown_not_exited(self):
        state = self.state()
        self.assertEqual(runtime.identity_status(state, self.root / 'missing-proc'), 'unknown')

    def test_upstream_is_fixed_allowlist(self):
        for value in ('openapi.qoder.com.cn', 'openapi.qoder.sh'):
            self.assertEqual(runtime.validate_upstream(value), value)
        for value in ('127.0.0.1', 'openapi.qoder.com.cn.',
                      'openapi.qoder.com.cn:443', 'other.example'):
            with self.assertRaisesRegex(
                    ValueError, 'invalid_gateway_upstream'):
                runtime.validate_upstream(value)


if __name__ == '__main__':
    unittest.main()
