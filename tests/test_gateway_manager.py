import importlib.util
import json
import os
from pathlib import Path
import pwd
import socket
import sys
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / 'panel'))
spec = importlib.util.spec_from_file_location(
    'gateway_manager', ROOT / 'ops/gateway-manager.py')
gm = importlib.util.module_from_spec(spec)
spec.loader.exec_module(gm)


class ManagerTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.root = Path(self.tmp.name)
        self.account = pwd.getpwuid(os.geteuid())
        self.proc = self.root / 'proc'
        (self.proc / 'sys/kernel/random').mkdir(parents=True)
        (self.proc / 'sys/kernel/random/boot_id').write_text(
            '00000000-0000-4000-8000-000000000001\n')
        self.manager = gm.Manager(
            self.root, ROOT / 'ops/uplink-gw.py',
            self.root / 'config/uplink-gw.json', 19849, self.account,
            proc_root=self.proc)
        self.cfg = {
            'mode': 'enforce',
            'sink_post_prefixes': ['/algo/api/v1/tracking'],
            'token_allow_prefixes': []
        }

    def tearDown(self):
        self.tmp.cleanup()

    def state(self, generation, previous=None, pid=123):
        return gm.validate_state({
            'schemaVersion': gm.STATE_VERSION,
            'generation': generation,
            'process': {
                'pid': pid,
                'startTicks': 123456,
                'bootId': '00000000-0000-4000-8000-000000000001',
                'uid': self.account.pw_uid,
                'executableDevice': 1,
                'executableInode': 2,
                'cmdline': [
                    os.path.realpath(sys.executable), '-B',
                    str(Path(generation['path']) / 'uplink-gw.py')
                ],
                'configPath': str(Path(generation['path']) / 'config.json'),
                'configHash': generation['configHash'],
                'port': generation['port'],
                'root': generation['root'],
                'upstream': generation['upstream'],
                'nonce': '%032x' % pid
            },
            'previous': previous
        }, self.manager.store, self.root, self.manager.port)

    def snapshot(self, cfg=None):
        self.manager.store.mkdir(parents=True, exist_ok=True)
        return self.manager.snapshot(cfg or self.cfg)

    def test_stop_waits_on_bound_pidfd_not_transient_proc_state(self):
        generation = self.snapshot()
        state = self.state(generation)
        with patch.object(self.manager, 'process_status', side_effect=['match', 'match']) as identity, \
                patch.object(gm, 'gateway_port_status', return_value='owned'), \
                patch.object(gm.os, 'pidfd_open', return_value=99, create=True), \
                patch.object(gm.signal, 'pidfd_send_signal', create=True) as send, \
                patch.object(gm.select, 'select', return_value=([99], [], [])) as wait, \
                patch.object(gm.os, 'close') as close, \
                patch.object(self.manager, 'busy', return_value=False):
            self.manager.stop(state)
        self.assertEqual(identity.call_count, 2)
        send.assert_called_once_with(99, gm.signal.SIGTERM)
        wait.assert_called_once_with([99], [], [], 5)
        close.assert_called_once_with(99)

    def test_null_state_or_journal_preserves_generations(self):
        for name in ('current.json', 'operation.json'):
            generation = self.snapshot()
            path = self.manager.store / name
            path.write_text('null')
            self.assertEqual(self.manager.gc(), [])
            self.assertTrue(Path(generation['path']).is_dir())
            with self.assertRaisesRegex(ValueError, 'manual_recovery_required'):
                self.manager.recover()
            path.unlink()

    def test_real_user_preflight_does_not_start_server(self):
        with self.manager.locked():
            generation = self.manager.snapshot(self.cfg)
            self.manager.preflight(generation)
        self.assertFalse(self.manager.state_file.exists())
        self.assertFalse(self.manager.config.exists())
        self.assertEqual(
            (Path(generation['path']) / 'config.json').stat().st_mode & 0o777,
            0o640)
        self.assertTrue((Path(generation['path']) / 'gateway_runtime.py').is_file())

    def test_log_directory_is_private_and_legacy_log_untouched(self):
        self.manager.root = self.root.resolve()
        directory = self.manager.root / 'logs'
        directory.mkdir()
        legacy = directory / 'uplink-gw.jsonl'
        legacy.write_bytes(b'legacy-evidence')
        self.manager.prepare_log()
        self.assertEqual(legacy.read_bytes(), b'legacy-evidence')
        self.assertEqual((directory / 'gateway').stat().st_mode & 0o777, 0o700)
        self.assertEqual((directory / 'gateway/uplink-gw.jsonl').stat().st_mode & 0o777, 0o600)

    def test_log_preparation_rejects_fifo_and_hardlink_without_chmod(self):
        self.manager.root = self.root.resolve()
        audit = self.manager.root / 'logs/gateway'
        audit.mkdir(parents=True)
        log = audit / 'uplink-gw.jsonl'
        os.mkfifo(log)
        with self.assertRaises((ValueError, OSError)):
            self.manager.prepare_log()
        log.unlink()
        target = self.manager.root / 'target'
        target.write_bytes(b'private')
        target.chmod(0o640)
        os.link(target, log)
        with self.assertRaises(ValueError):
            self.manager.prepare_log()
        self.assertEqual(target.stat().st_mode & 0o777, 0o640)
        self.assertEqual(target.read_bytes(), b'private')

    def test_launch_environment_is_positive_allowlist(self):
        generation = self.snapshot()
        with patch.dict(os.environ, {
                'HTTPS_PROXY': 'http://proxy.invalid',
                'QODERWAKE_PRIVATE_TOKEN': 'secret',
                'PYTHONPATH': '/private/injection'}, clear=False):
            env = self.manager.launch_environment(generation, 'a' * 32)
        self.assertEqual(set(env), {
            'PYTHONDONTWRITEBYTECODE', 'QW_ROOT', 'QW_GW_PORT',
            'QW_GW_CONFIG', 'QW_GW_CONFIG_HASH', 'QW_GW_NONCE',
            'QW_GW_UPSTREAM'
        })
        self.assertNotIn('HTTPS_PROXY', env)
        self.assertNotIn('QODERWAKE_PRIVATE_TOKEN', env)
        self.assertNotIn('PYTHONPATH', env)

    def test_preflight_candidate_always_cleans_generation(self):
        with self.manager.locked():
            result = self.manager.preflight_candidate({'mode': 'strict'})
            self.assertTrue(result['ok'])
            self.assertEqual(list(self.manager.store.glob('generation-*')), [])
            with patch.object(self.manager, 'preflight',
                              side_effect=ValueError('fixture')):
                with self.assertRaisesRegex(ValueError, 'fixture'):
                    self.manager.preflight_candidate({'mode': 'strict'})
            self.assertEqual(list(self.manager.store.glob('generation-*')), [])

    def test_preflight_failure_keeps_old_process_and_config(self):
        self.manager.config.parent.mkdir()
        self.manager.config.write_text(json.dumps(self.cfg))
        generation = self.snapshot({'mode': 'strict'})
        with patch.object(self.manager, 'snapshot', return_value=generation), \
                patch.object(self.manager, 'preflight',
                             side_effect=ValueError('unreadable')), \
                patch.object(self.manager, 'stop') as stop:
            with self.assertRaises(ValueError):
                self.manager.apply(self.cfg)
            stop.assert_not_called()
        self.assertEqual(json.loads(self.manager.config.read_text()), self.cfg)
        self.assertFalse(Path(generation['path']).exists())

    def test_unmanaged_process_never_stopped(self):
        generation = self.snapshot()
        with patch.object(self.manager, 'snapshot', return_value=generation), \
                patch.object(self.manager, 'preflight'), \
                patch.object(self.manager, 'busy', return_value=True), \
                patch.object(self.manager, 'state', return_value=None), \
                patch.object(self.manager, 'stop') as stop:
            with self.assertRaisesRegex(ValueError, 'unmanaged_gateway'):
                self.manager.apply(self.cfg)
            stop.assert_not_called()
        self.assertFalse(Path(generation['path']).exists())

    def test_start_identity_unknown_does_not_start_previous_generation(self):
        old_generation = self.snapshot({'mode': 'strict'})
        new_generation = self.snapshot(self.cfg)
        old = self.state(old_generation, pid=441)
        with patch.object(self.manager, 'snapshot',
                          return_value=new_generation), \
                patch.object(self.manager, 'preflight'), \
                patch.object(self.manager, 'state', return_value=old), \
                patch.object(self.manager, 'busy', return_value=True), \
                patch.object(self.manager, 'health', return_value=True), \
                patch.object(self.manager, 'stop'), \
                patch.object(self.manager, 'start', side_effect=ValueError(
                    'gateway_start_identity_unknown')) as start:
            with self.assertRaisesRegex(
                    ValueError, 'gateway_start_identity_unknown'):
                self.manager.apply(self.cfg)
        start.assert_called_once_with(new_generation, old_generation)
        self.assertTrue(self.manager.journal_file.exists())
        self.assertTrue(Path(new_generation['path']).exists())

    def test_start_failure_restores_previous_generation(self):
        old_generation = self.snapshot({'mode': 'strict'})
        new_generation = self.snapshot(self.cfg)
        old = self.state(old_generation, pid=451)
        restored = self.state(old_generation, pid=456)
        with patch.object(self.manager, 'snapshot',
                          return_value=new_generation), \
                patch.object(self.manager, 'preflight') as check, \
                patch.object(self.manager, 'state', return_value=old), \
                patch.object(self.manager, 'busy', return_value=True), \
                patch.object(self.manager, 'health', return_value=True), \
                patch.object(self.manager, 'stop') as stop, \
                patch.object(self.manager, 'start', side_effect=[
                    ValueError('gateway_start_failed'), restored]) as start, \
                patch.object(self.manager, 'publish') as publish:
            with self.assertRaisesRegex(ValueError, 'previous_restored'):
                self.manager.apply(self.cfg)
            self.assertEqual(check.call_count, 2)
            stop.assert_called_once_with(old)
            self.assertEqual(start.call_args_list[1].args,
                             (old_generation, None))
            publish.assert_called_once_with(restored)
        self.assertFalse(self.manager.journal_file.exists())
        self.assertFalse(Path(new_generation['path']).exists())

    @unittest.skipUnless(Path('/proc').is_dir(),
                         'Linux process identity integration')
    def test_real_process_switch_failure_restores_old_generation(self):
        self.manager.proc_root = Path('/proc')
        with socket.socket() as sock:
            sock.bind(('127.0.0.1', 0))
            self.manager.port = sock.getsockname()[1]
        with self.manager.locked():
            try:
                self.manager.apply({'mode': 'strict', 'rules': []})
                self.assertTrue(self.manager.health(self.manager.state()))
                self.manager.apply(self.cfg)
                self.assertEqual(
                    self.manager.state()['generation']['mode'], 'enforce')
                self.manager.rollback()
                self.assertEqual(
                    self.manager.state()['generation']['mode'], 'strict')
                source = (ROOT / 'ops/uplink-gw.py').read_text()
                failing = self.root / 'failing.py'
                source = source.replace(
                    'args = parser.parse_args()',
                    "args = parser.parse_args()\n    if not args.check:\n        raise RuntimeError('fixture_start_failure')")
                failing.write_text(source)
                self.manager.script = failing
                with self.assertRaisesRegex(ValueError, 'previous_restored'):
                    self.manager.apply(self.cfg)
                self.assertTrue(self.manager.health(self.manager.state()))
                self.assertEqual(
                    self.manager.state()['generation']['mode'], 'strict')
            finally:
                self.manager.stop(self.manager.state())

    def test_publish_failure_restores_saved_config(self):
        self.manager.config.parent.mkdir()
        self.manager.config.write_text(json.dumps(self.cfg))
        self.manager.config.chmod(0o600)
        original = self.manager.config.read_bytes()
        with self.manager.locked():
            generation = self.manager.snapshot({'mode': 'strict'})
            state = self.state(generation)
            write = gm.atomic
            def atomic(path, *args, **kwargs):
                if path == self.manager.state_file:
                    raise OSError('fixture_write_failure')
                return write(path, *args, **kwargs)
            with patch.object(gm, 'atomic', side_effect=atomic), \
                    self.assertRaises(OSError):
                self.manager.publish(state)
        self.assertEqual(self.manager.config.read_bytes(), original)
        self.assertEqual(self.manager.config.stat().st_mode & 0o777, 0o600)
        self.assertFalse(self.manager.state_file.exists())

    def test_rollback_publish_failure_recovers_current(self):
        previous_generation = self.snapshot({'mode': 'strict'})
        current_generation = self.snapshot(self.cfg)
        current = self.state(
            current_generation, previous_generation, pid=501)
        previous = self.state(
            previous_generation, current_generation, pid=502)
        recovered = self.state(
            current_generation, previous_generation, pid=503)
        with patch.object(self.manager, 'state', return_value=current), \
                patch.object(self.manager, 'preflight'), \
                patch.object(self.manager, 'health', return_value=True), \
                patch.object(self.manager, 'stop') as stop, \
                patch.object(self.manager, 'start',
                             side_effect=[previous, recovered]) as start, \
                patch.object(self.manager, 'publish',
                             side_effect=[OSError('fixture'), None]):
            with self.assertRaisesRegex(ValueError, 'current_restored'):
                self.manager.rollback()
            self.assertEqual(
                [call.args for call in start.call_args_list],
                [(previous_generation, current_generation),
                 (current_generation, previous_generation)])
            self.assertEqual(
                [call.args[0] for call in stop.call_args_list],
                [current, previous])
        self.assertFalse(self.manager.journal_file.exists())

    def test_rollback_publish_commit_unknown_preserves_candidate(self):
        previous_generation = self.snapshot({'mode': 'strict'})
        current_generation = self.snapshot(self.cfg)
        current = self.state(
            current_generation, previous_generation, pid=511)
        restored = self.state(
            previous_generation, current_generation, pid=512)
        with patch.object(self.manager, 'state', return_value=current), \
                patch.object(self.manager, 'preflight'), \
                patch.object(self.manager, 'health', return_value=True), \
                patch.object(self.manager, 'stop') as stop, \
                patch.object(self.manager, 'start', return_value=restored), \
                patch.object(self.manager, 'publish', side_effect=ValueError(
                    'gateway_publish_commit_unknown')), \
                self.assertRaisesRegex(
                    ValueError, 'publish_commit_unknown'):
            self.manager.rollback()
        stop.assert_called_once_with(current)
        self.assertTrue(self.manager.journal_file.exists())
        self.assertTrue(Path(previous_generation['path']).exists())

    def test_first_start_identity_unknown_preserves_journal_and_candidate(self):
        candidate = self.snapshot({'mode': 'strict'})
        with patch.object(self.manager, 'snapshot', return_value=candidate), \
                patch.object(self.manager, 'preflight'), \
                patch.object(self.manager, 'state', return_value=None), \
                patch.object(self.manager, 'busy', return_value=False), \
                patch.object(self.manager, 'start', side_effect=ValueError(
                    'gateway_start_identity_unknown')):
            with self.assertRaisesRegex(
                    ValueError, 'gateway_start_identity_unknown'):
                self.manager.apply({'mode': 'strict'})
        self.assertTrue(self.manager.journal_file.exists())
        self.assertTrue(Path(candidate['path']).exists())

    def test_first_start_failure_without_listener_cleans_journal_and_candidate(self):
        candidate = self.snapshot({'mode': 'strict'})
        with patch.object(self.manager, 'snapshot', return_value=candidate), \
                patch.object(self.manager, 'preflight'), \
                patch.object(self.manager, 'state', return_value=None), \
                patch.object(self.manager, 'busy', return_value=False), \
                patch.object(self.manager, 'start',
                             side_effect=ValueError('gateway_start_failed')):
            with self.assertRaisesRegex(ValueError, 'gateway_start_failed'):
                self.manager.apply({'mode': 'strict'})
        self.assertFalse(self.manager.journal_file.exists())
        self.assertFalse(Path(candidate['path']).exists())

    def test_prepared_journal_without_previous_aborts_and_cleans_candidate(self):
        candidate = self.snapshot({'mode': 'strict'})
        self.manager.write_journal('apply', 'prepared', candidate)
        with patch.object(self.manager, 'state', return_value=None), \
                patch.object(self.manager, 'busy', return_value=False):
            self.assertEqual(self.manager.recover(), 'aborted_before_start')
        self.assertFalse(self.manager.journal_file.exists())
        self.assertFalse(Path(candidate['path']).exists())

    def test_started_journal_commits_healthy_candidate(self):
        candidate = self.snapshot({'mode': 'strict'})
        started = self.state(candidate, pid=601)
        journal = self.manager.write_journal(
            'apply', 'new-started', candidate, started=started)
        publish = patch.object(self.manager, 'publish')
        with patch.object(self.manager, 'state', return_value=None), \
                patch.object(self.manager, 'health',
                             side_effect=lambda value: value == started), \
                publish as publish_call:
            self.assertEqual(self.manager.recover(), 'candidate_committed')
        publish_call.assert_called_once_with(started)
        self.assertFalse(self.manager.journal_file.exists())
        self.assertEqual(journal['started'], started)

    def test_rollback_prepared_recovery_never_deletes_target_generation(self):
        target = self.snapshot({'mode': 'strict'})
        current_generation = self.snapshot(self.cfg)
        current = self.state(current_generation, target, pid=701)
        self.manager.write_journal(
            'rollback', 'prepared', target, previous=current)
        with patch.object(self.manager, 'state', return_value=current), \
                patch.object(self.manager, 'health', return_value=True), \
                patch.object(self.manager, 'publish'):
            self.assertEqual(self.manager.recover(), 'previous_active')
        self.assertTrue(Path(target['path']).is_dir())
        self.assertFalse(self.manager.journal_file.exists())

    def test_gc_stops_when_proc_visibility_is_incomplete(self):
        orphan = self.snapshot({'mode': 'strict'})
        with patch.object(gm, 'proc_generation_paths',
                          return_value=(set(), False)), \
                patch.object(self.manager, 'state', return_value=None), \
                patch.object(self.manager, 'journal', return_value=None):
            self.assertEqual(self.manager.gc(), [])
        self.assertTrue(Path(orphan['path']).is_dir())

    def test_gc_quarantine_restores_generation_when_reference_appears(self):
        orphan = self.snapshot({'mode': 'strict'})
        original = orphan['path']
        calls = 0
        def snapshot():
            nonlocal calls
            calls += 1
            return set() if calls == 1 else {original}
        with patch.object(self.manager, 'gc_snapshot', side_effect=snapshot):
            self.assertEqual(self.manager.gc(), [])
        self.assertTrue(Path(original).is_dir())
        self.assertEqual(list(self.manager.store.glob('.gc-*')), [])

    def test_gc_quarantine_deletes_only_after_complete_rescan(self):
        orphan = self.snapshot({'mode': 'strict'})
        with patch.object(self.manager, 'gc_snapshot', side_effect=[set(), set()]):
            self.assertEqual(self.manager.gc(), [orphan['path']])
        self.assertFalse(Path(orphan['path']).exists())
        self.assertEqual(list(self.manager.store.glob('.gc-*')), [])

    def test_lock_recovery_restores_crashed_gc_quarantine(self):
        orphan = self.snapshot({'mode': 'strict'})
        quarantine = self.manager.quarantine_generation(orphan['path'])
        self.assertFalse(Path(orphan['path']).exists())
        self.assertTrue(quarantine.is_dir())
        with self.manager.locked():
            self.assertTrue(Path(orphan['path']).is_dir())
            self.assertFalse(quarantine.exists())

    def test_lock_recovery_refuses_quarantine_name_conflict(self):
        orphan = self.snapshot({'mode': 'strict'})
        quarantine = self.manager.quarantine_generation(orphan['path'])
        Path(orphan['path']).mkdir()
        with self.assertRaisesRegex(
                ValueError, 'gc_manual_recovery_required'):
            with self.manager.locked():
                pass
        self.assertTrue(quarantine.is_dir())
        self.assertTrue(Path(orphan['path']).is_dir())

    def test_prepared_unhealthy_identified_candidate_is_stopped_before_abort(self):
        candidate = self.snapshot({'mode': 'strict'})
        discovered = self.state(candidate, pid=801)
        self.manager.write_journal('apply', 'prepared', candidate)
        with patch.object(self.manager, 'state', return_value=None), \
                patch.object(self.manager, 'discover_candidate_state',
                             return_value=discovered), \
                patch.object(self.manager, 'health', return_value=False), \
                patch.object(self.manager, 'stop') as stop, \
                patch.object(self.manager, 'busy', return_value=False):
            self.assertEqual(self.manager.recover(), 'aborted_before_start')
        stop.assert_called_once_with(discovered)
        self.assertFalse(self.manager.journal_file.exists())
        self.assertFalse(Path(candidate['path']).exists())

    def test_started_unhealthy_candidate_stops_then_restores_previous(self):
        candidate = self.snapshot({'mode': 'strict'})
        previous_generation = self.snapshot(self.cfg)
        previous = self.state(previous_generation, pid=811)
        started = self.state(candidate, previous_generation, pid=812)
        restored = self.state(previous_generation, pid=813)
        self.manager.write_journal(
            'apply', 'new-started', candidate, previous, started)
        with patch.object(self.manager, 'state', return_value=None), \
                patch.object(self.manager, 'health', return_value=False), \
                patch.object(self.manager, 'process_status',
                             side_effect=lambda value: (
                                 'match' if value == started else 'exited')), \
                patch.object(self.manager, 'stop') as stop, \
                patch.object(self.manager, 'busy', return_value=False), \
                patch.object(self.manager, 'start', return_value=restored), \
                patch.object(self.manager, 'publish'):
            self.assertEqual(self.manager.recover(), 'previous_restored')
        stop.assert_called_once_with(started)
        self.assertFalse(self.manager.journal_file.exists())
        self.assertFalse(Path(candidate['path']).exists())

    def test_unknown_listener_preserves_journal_and_generation(self):
        candidate = self.snapshot({'mode': 'strict'})
        self.manager.write_journal('apply', 'prepared', candidate)
        with patch.object(self.manager, 'state', return_value=None), \
                patch.object(self.manager, 'busy', return_value=True), \
                self.assertRaisesRegex(
                    ValueError, 'listener_identity_unknown'):
            self.manager.recover()
        self.assertTrue(self.manager.journal_file.exists())
        self.assertTrue(Path(candidate['path']).exists())

    def test_unknown_started_identity_with_idle_port_never_restores_previous(self):
        candidate = self.snapshot({'mode': 'strict'})
        previous_generation = self.snapshot(self.cfg)
        previous = self.state(previous_generation, pid=901)
        started = self.state(candidate, previous_generation, pid=902)
        self.manager.write_journal(
            'apply', 'new-started', candidate, previous, started)
        with patch.object(self.manager, 'state', return_value=None), \
                patch.object(self.manager, 'health', return_value=False), \
                patch.object(self.manager, 'process_status',
                             return_value='unknown'), \
                patch.object(self.manager, 'busy', return_value=False), \
                patch.object(self.manager, 'start') as start, \
                self.assertRaisesRegex(
                    ValueError, 'process_identity_unknown'):
            self.manager.recover()
        start.assert_not_called()
        self.assertTrue(self.manager.journal_file.exists())
        self.assertTrue(Path(candidate['path']).exists())

    def test_process_discovery_visibility_failure_preserves_journal(self):
        candidate = self.snapshot({'mode': 'strict'})
        self.manager.write_journal('apply', 'prepared', candidate)
        missing_proc = self.root / 'missing-proc'
        self.manager.proc_root = missing_proc
        with self.assertRaisesRegex(
                ValueError, 'process_visibility_unknown'):
            self.manager.recover()
        self.assertTrue(self.manager.journal_file.exists())
        self.assertTrue(Path(candidate['path']).exists())

    def test_publish_then_journal_cleanup_failure_preserves_commit(self):
        candidate = self.snapshot({'mode': 'strict'})
        current = self.state(candidate, pid=911)
        with patch.object(self.manager, 'snapshot', return_value=candidate), \
                patch.object(self.manager, 'preflight'), \
                patch.object(self.manager, 'state', return_value=None), \
                patch.object(self.manager, 'busy', return_value=False), \
                patch.object(self.manager, 'start', return_value=current), \
                patch.object(self.manager, 'publish') as publish, \
                patch.object(self.manager, 'clear_journal',
                             side_effect=OSError('fixture')), \
                self.assertRaisesRegex(
                    ValueError, 'commit_cleanup_failed'):
            self.manager.apply({'mode': 'strict'})
        publish.assert_called_once_with(current)
        self.assertTrue(self.manager.journal_file.exists())
        self.assertTrue(Path(candidate['path']).exists())

    def test_publish_commit_unknown_preserves_journal_process_and_generation(self):
        candidate = self.snapshot({'mode': 'strict'})
        current = self.state(candidate, pid=916)
        with patch.object(self.manager, 'snapshot', return_value=candidate), \
                patch.object(self.manager, 'preflight'), \
                patch.object(self.manager, 'state', return_value=None), \
                patch.object(self.manager, 'busy', return_value=False), \
                patch.object(self.manager, 'start', return_value=current), \
                patch.object(self.manager, 'publish', side_effect=ValueError(
                    'gateway_publish_commit_unknown')), \
                patch.object(self.manager, 'stop') as stop, \
                self.assertRaisesRegex(
                    ValueError, 'publish_commit_unknown'):
            self.manager.apply({'mode': 'strict'})
        stop.assert_not_called()
        self.assertTrue(self.manager.journal_file.exists())
        self.assertTrue(Path(candidate['path']).exists())

    def test_gc_rejects_dangling_state_and_journal_symlinks(self):
        orphan = self.snapshot({'mode': 'strict'})
        for path in (self.manager.state_file, self.manager.journal_file):
            path.symlink_to(self.root / 'missing-target')
            self.assertEqual(self.manager.gc(), [])
            self.assertTrue(Path(orphan['path']).is_dir())
            path.unlink()

    def test_publish_rejects_symlinked_config_parent(self):
        generation = self.snapshot({'mode': 'strict'})
        state = self.state(generation, pid=921)
        outside = self.root / 'outside'
        outside.mkdir()
        config = self.root / 'config'
        config.symlink_to(outside, target_is_directory=True)
        with self.assertRaisesRegex(
                ValueError, 'config_directory_unavailable'):
            self.manager.publish(state)
        self.assertEqual(list(outside.iterdir()), [])

    def test_invalid_candidate_rejected_without_process_changes(self):
        with self.manager.locked(), \
                patch.object(self.manager, 'stop') as stop:
            with self.assertRaises(ValueError):
                self.manager.apply({'mdoe': 'strict'})
            stop.assert_not_called()


if __name__ == '__main__':
    unittest.main()
