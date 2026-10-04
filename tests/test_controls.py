import concurrent.futures
import hashlib
import json
from pathlib import Path
import sys
import tempfile
import threading
import time
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1]/'panel'))
from panel_security import Security, AccessError, AdmissionController
from panel_patches import PatchManager, ORIGINAL, REPLACEMENT


class CallerTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.sec = Security(self.tmp.name)

    def tearDown(self):
        self.tmp.cleanup()

    def test_scopes_revocation_and_no_plaintext_storage(self):
        caller = self.sec.create_caller('automation', ['w1'], 3, 1)
        identity = self.sec.authenticate({'Authorization': 'Bearer '+caller['token']})
        self.assertEqual(identity['role'], 'caller')
        with self.assertRaises(AccessError) as error:
            self.sec.consume_caller(identity, 'w2')
        self.assertEqual(error.exception.status, 403)
        self.assertEqual(self.sec.callers()[0]['used'], 0)
        self.assertNotIn(caller['token'], json.dumps(self.sec.callers()))
        self.assertNotIn(caller['token'].encode(), self.sec.path.read_bytes())
        with self.assertRaises(AccessError):
            self.sec.login(caller['token'])
        self.sec.revoke_caller(caller['id'])
        with self.assertRaises(AccessError):
            self.sec.authenticate({'Authorization': 'Bearer '+caller['token']})
        with self.assertRaises(AccessError):
            self.sec.consume_caller(identity, 'w1')

    def test_quota_atomic_persistent_and_expiring(self):
        caller = self.sec.create_caller('automation', ['w1'], 5, 1)
        identity = self.sec.authenticate({'Authorization': 'Bearer '+caller['token']})
        def consume(_):
            try:
                self.sec.consume_caller(identity, 'w1')
                return True
            except AccessError:
                return False
        with concurrent.futures.ThreadPoolExecutor(max_workers=8) as pool:
            self.assertEqual(sum(pool.map(consume, range(20))), 5)
        self.sec = Security(self.tmp.name)
        self.assertEqual(self.sec.callers()[0]['used'], 5)
        with self.assertRaises(AccessError):
            self.sec.consume_caller(identity, 'w1')
        with self.sec.db() as con:
            con.execute('UPDATE callers SET expires=0')
        with self.assertRaises(AccessError):
            self.sec.authenticate({'Authorization': 'Bearer '+caller['token']})


class AdmissionTests(unittest.TestCase):
    def test_drain_handoff_blocks_new_requests_and_waits_existing(self):
        gate = AdmissionController()
        existing = gate.acquire('existing')
        owner = gate.acquire('owner')
        epoch = gate.begin_drain('owner', 'test drain', lease=owner)
        with self.assertRaises(AccessError) as error:
            gate.acquire('late')
        self.assertEqual(error.exception.code, 'maintenance_mode')
        finished = threading.Event()
        thread = threading.Thread(target=lambda: (gate.wait_clear(epoch, 2), finished.set()))
        thread.start()
        self.assertFalse(finished.wait(.05))
        existing.release()
        self.assertTrue(finished.wait(1))
        thread.join()
        gate.transition(epoch, 'maintenance')
        self.assertEqual(gate.status()['mode'], 'maintenance')
        gate.exit()
        lease = gate.acquire('after')
        lease.release()

    def test_call_limit_and_double_release_are_explicit(self):
        gate = AdmissionController(call_limit=2)
        first = gate.acquire('one', call=True)
        second = gate.acquire('two', call=True)
        with self.assertRaises(AccessError) as error:
            gate.acquire('three', call=True)
        self.assertEqual(error.exception.code, 'concurrency_limited')
        first.release()
        with self.assertRaisesRegex(RuntimeError, 'already_released'):
            first.release()
        second.release()
        self.assertEqual(gate.status()['activeCalls'], 0)

    def test_ttl_starts_at_manual_maintenance_transition(self):
        gate = AdmissionController()
        epoch = gate.begin_drain('owner', 'test', ttl=30)
        self.assertIsNone(gate.status()['expires'])
        gate.transition(epoch, 'maintenance')
        remaining = gate.status()['expires'] - time.time()
        self.assertGreater(remaining, 29)
        gate.expires = time.time() - 1
        self.assertEqual(gate.status()['mode'], 'normal')

    def test_restarting_cannot_exit_or_expire_early(self):
        gate = AdmissionController()
        epoch = gate.begin_drain('owner', 'restart', ttl=30)
        gate.transition(epoch, 'restarting')
        gate.expires = time.time() - 1
        self.assertEqual(gate.status()['mode'], 'restarting')
        with self.assertRaises(AccessError) as error:
            gate.exit()
        self.assertEqual(error.exception.code, 'maintenance_exit_rejected')
        with self.assertRaises(AccessError) as error:
            gate.acquire('late')
        self.assertEqual(error.exception.code, 'maintenance_mode')
        gate.complete(epoch)
        self.assertEqual(gate.status()['mode'], 'normal')


class DeletionSecurityTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.sec = Security(self.tmp.name)
        self.principal = 'session:admin'
        self.preview = {
            'kind': 'waker', 'target': 'w1', 'revision': '',
            'impactDigest': 'a' * 64,
            'warningsRequired': ['history']
        }

    def tearDown(self):
        self.tmp.cleanup()

    def test_preview_principal_warning_and_one_use_binding(self):
        raw, _ = self.sec.create_deletion_preview(self.principal, self.preview)
        with self.assertRaises(AccessError):
            self.sec.deletion_preview(raw, 'session:other', 'waker', 'w1')
        with self.assertRaises(AccessError):
            self.sec.begin_deletion(raw, self.principal, 'waker', 'w1',
                                    'a' * 64, [], 'request-0001')
        operation = self.sec.begin_deletion(
            raw, self.principal, 'waker', 'w1', 'a' * 64,
            ['history'], 'request-0001')
        self.assertFalse(operation['replay'])
        with self.assertRaises(AccessError):
            self.sec.begin_deletion(raw, self.principal, 'waker', 'w1',
                                    'a' * 64, ['history'], 'request-0002')

    def test_idempotent_replay_returns_unknown_without_new_preview(self):
        raw, _ = self.sec.create_deletion_preview(self.principal, self.preview)
        self.sec.begin_deletion(raw, self.principal, 'waker', 'w1',
                                'a' * 64, ['history'], 'request-0001')
        result = {'ok': False, 'status': 'unknown', 'error': 'deletion_result_unknown'}
        self.sec.finish_deletion(self.principal, 'request-0001', 'unknown', result)
        replay = self.sec.replay_deletion(
            self.principal, 'request-0001', 'waker', 'w1',
            'a' * 64, ['history'])
        self.assertTrue(replay['replay'])
        self.assertEqual(replay['result'], result)
        with self.assertRaises(AccessError) as error:
            self.sec.replay_deletion(
                self.principal, 'request-0001', 'provider', 'p',
                'a' * 64, ['history'])
        self.assertEqual(error.exception.code, 'idempotency_key_conflict')
        self.assertEqual(self.sec.deletion_operation(self.principal, 'request-0001')['status'], 'unknown')

    def test_unresolved_target_blocks_new_key_across_principals(self):
        raw, _ = self.sec.create_deletion_preview(self.principal, self.preview)
        self.sec.begin_deletion(raw, self.principal, 'waker', 'w1',
                                'a' * 64, ['history'], 'request-0001')
        self.sec.finish_deletion(
            self.principal, 'request-0001', 'unknown',
            {'ok': False, 'status': 'unknown', 'error': 'deletion_result_unknown'})
        other = dict(self.preview, impactDigest='b' * 64)
        raw, _ = self.sec.create_deletion_preview('session:other', other)
        with self.assertRaises(AccessError) as error:
            self.sec.begin_deletion(raw, 'session:other', 'waker', 'w1',
                                    'b' * 64, ['history'], 'request-0002')
        self.assertEqual(error.exception.code, 'deletion_operation_unresolved')
        separate = dict(other, target='w2')
        raw, _ = self.sec.create_deletion_preview('session:other', separate)
        operation = self.sec.begin_deletion(
            raw, 'session:other', 'waker', 'w2', 'b' * 64,
            ['history'], 'request-0003')
        self.assertFalse(operation['replay'])


class PatchTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.root = Path(self.tmp.name)
        self.binary = self.root/'daemon'
        self.original = b'fixture prefix '+ORIGINAL+b' suffix'
        self.output = self.original.replace(ORIGINAL, REPLACEMENT)
        self.binary.write_bytes(self.original)
        self.binary.chmod(0o755)
        self.registry = self.root/'registry.json'
        self.profile = {'module':'qcs-endpoint','version':'fixture-1',
                        'original_sha256':hashlib.sha256(self.original).hexdigest(),
                        'patched_sha256':hashlib.sha256(self.output).hexdigest()}
        self.registry.write_text(json.dumps({'profiles':[self.profile]}))
        self.manager = PatchManager(
            self.root, self.binary, self.registry, gateway_port=19840)
        self.stopped = patch.object(self.manager, 'require_stopped')
        self.stopped.start()

    def tearDown(self):
        self.stopped.stop()
        self.tmp.cleanup()

    def test_apply_restore_and_one_use_confirm(self):
        self.assertTrue(self.manager.status()['verified'])
        plan = self.manager.plan('apply', 'admin')
        with self.assertRaises(ValueError):
            self.manager.execute(plan['id'], 'other')
        self.manager.execute(plan['id'], 'admin')
        self.assertEqual(self.binary.read_bytes(), self.output)
        self.assertEqual(self.binary.stat().st_mode & 0o777, 0o755)
        with self.assertRaises(ValueError):
            self.manager.execute(plan['id'], 'admin')
        backup = self.manager.root/(self.profile['original_sha256']+'.bin')
        self.assertEqual(backup.stat().st_mode & 0o777, 0o600)
        plan = self.manager.plan('restore', 'admin')
        self.manager.execute(plan['id'], 'admin')
        self.assertEqual(self.binary.read_bytes(), self.original)

    def test_nondefault_gateway_port_rejects_fixed_port_patch(self):
        manager = PatchManager(
            self.root, self.binary, self.registry, gateway_port=19841)
        with self.assertRaisesRegex(
                ValueError, 'patch_requires_gateway_port_19840'):
            manager.plan('apply', 'admin')

    def test_patch_root_symlink_is_rejected(self):
        outside = self.root / 'outside'
        outside.mkdir()
        patches = self.root / 'patches'
        patches.symlink_to(outside, target_is_directory=True)
        plan = self.manager.plan('apply', 'admin')
        with self.assertRaisesRegex(ValueError, 'symlink_rejected'):
            self.manager.execute(plan['id'], 'admin')
        self.assertEqual(list(outside.iterdir()), [])

    def test_replace_rechecks_stopped_state_immediately_before_swap(self):
        plan = self.manager.plan('apply', 'admin')
        self.manager.require_stopped.side_effect = [
            None, None, ValueError('stop_daemon_before_patch')]
        with self.assertRaisesRegex(ValueError, 'stop_daemon_before_patch'):
            self.manager.execute(plan['id'], 'admin')
        self.assertEqual(self.binary.read_bytes(), self.original)

    def test_unknown_changed_binary_and_bad_output_fail_closed(self):
        plan = self.manager.plan('apply', 'admin')
        self.binary.write_bytes(self.original+b'new-version')
        with self.assertRaises(ValueError):
            self.manager.execute(plan['id'], 'admin')
        self.assertEqual(self.manager.status()['state'], 'unsupported')
        self.binary.write_bytes(self.original)
        self.profile['patched_sha256'] = '0'*64
        self.registry.write_text(json.dumps({'profiles':[self.profile]}))
        plan = self.manager.plan('apply', 'admin')
        with self.assertRaises(ValueError):
            self.manager.execute(plan['id'], 'admin')
        self.assertEqual(self.binary.read_bytes(), self.original)

    def test_legacy_backup_requires_full_original_hash(self):
        self.binary.write_bytes(self.output)
        legacy = self.binary.with_name(self.binary.name+'.bak-p1-old')
        legacy.write_bytes(b'untrusted')
        plan = self.manager.plan('restore', 'admin')
        with self.assertRaises(ValueError):
            self.manager.execute(plan['id'], 'admin')
        legacy.write_bytes(self.original)
        plan = self.manager.plan('restore', 'admin')
        self.manager.execute(plan['id'], 'admin')
        self.assertEqual(self.binary.read_bytes(), self.original)

    def test_tampered_restore_and_expired_plan(self):
        plan = self.manager.plan('apply', 'admin')
        self.manager.plans[plan['id']]['expires'] = 0
        with self.assertRaises(ValueError):
            self.manager.execute(plan['id'], 'admin')
        plan = self.manager.plan('apply', 'admin')
        self.manager.execute(plan['id'], 'admin')
        (self.manager.root/(self.profile['original_sha256']+'.bin')).write_bytes(b'changed')
        plan = self.manager.plan('restore', 'admin')
        with self.assertRaises(ValueError):
            self.manager.execute(plan['id'], 'admin')
        self.assertEqual(self.binary.read_bytes(), self.output)


if __name__ == '__main__':
    unittest.main()
