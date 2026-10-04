import concurrent.futures
import hashlib
import json
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / 'panel'))
import provider_config
from provider_config import ProviderSettings, SettingsConflict


class ProviderConfigTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.root = Path(self.tmp.name)
        self.path = self.root / 'home' / 'settings.json'
        self.recovery = self.root / 'provider-settings.previous.json'

    def tearDown(self):
        self.tmp.cleanup()

    def store(self):
        return ProviderSettings(self.path, self.recovery)

    def test_revision_uses_exact_raw_bytes_and_recovery_preserves_them(self):
        self.path.parent.mkdir(parents=True)
        raw = b'{\n  "providers": {},\n  "spacing": true\n}\n'
        self.path.write_bytes(raw)
        snapshot = self.store().read()
        self.assertEqual(snapshot['revision'], hashlib.sha256(raw).hexdigest())
        changed = self.store().update(
            snapshot['revision'],
            lambda data: dict(data, changed=True)
        )
        self.assertEqual(self.recovery.read_bytes(), raw)
        self.assertEqual(changed['revision'], hashlib.sha256(self.path.read_bytes()).hexdigest())

    def test_two_store_instances_with_same_revision_allow_only_one_writer(self):
        self.path.parent.mkdir(parents=True)
        self.path.write_text('{"providers":{}}')
        revision = self.store().read()['revision']

        def update(value):
            try:
                ProviderSettings(self.path, self.recovery).update(
                    revision,
                    lambda data: dict(data, winner=value)
                )
                return 'ok'
            except SettingsConflict:
                return 'conflict'

        with concurrent.futures.ThreadPoolExecutor(max_workers=2) as pool:
            results = list(pool.map(update, ('one', 'two')))
        self.assertEqual(sorted(results), ['conflict', 'ok'])
        self.assertIn(json.loads(self.path.read_text())['winner'], ('one', 'two'))

    def test_symlink_settings_and_lock_are_rejected(self):
        self.path.parent.mkdir(parents=True)
        target = self.root / 'target.json'
        target.write_text('{}')
        self.path.symlink_to(target)
        with self.assertRaisesRegex(ValueError, 'provider_settings_unavailable'):
            self.store().read()
        self.path.unlink()
        self.path.write_text('{}')
        store = self.store()
        lock_target = self.root / 'lock-target'
        lock_target.write_text('unchanged')
        store.lock_path.symlink_to(lock_target)
        with self.assertRaisesRegex(ValueError, 'symlink_not_allowed'):
            store.update(store.read()['revision'], lambda data: data)
        self.assertEqual(lock_target.read_text(), 'unchanged')

    def test_external_write_during_backup_is_detected_before_replace(self):
        self.path.parent.mkdir(parents=True)
        self.path.write_text('{"providers":{},"original":true}')
        store = self.store()
        base = store.read()['revision']
        original = provider_config._atomic_bytes
        def write(path, raw, **kwargs):
            original(path, raw, **kwargs)
            if Path(path) == self.recovery:
                self.path.write_text('{"providers":{},"external":true}')
        with patch.object(provider_config, '_atomic_bytes', side_effect=write):
            with self.assertRaises(SettingsConflict):
                store.update(base, lambda data: dict(data, panel=True))
        self.assertEqual(json.loads(self.path.read_text()), {'providers': {}, 'external': True})

    def test_null_provider_normalizes_in_memory_and_settings_are_bounded(self):
        self.path.parent.mkdir(parents=True)
        raw = b'{"providers":null}'
        self.path.write_bytes(raw)
        store = self.store()
        self.assertEqual(store.summaries()[0], [])
        self.assertEqual(self.path.read_bytes(), raw)
        with patch.object(provider_config, 'MAX_SETTINGS_BYTES', 4):
            with self.assertRaisesRegex(ValueError, 'provider_settings_too_large'):
                store.read()

    def test_missing_settings_can_be_created_without_fake_recovery(self):
        store = self.store()
        snapshot = store.read()
        self.assertEqual(snapshot['raw'], b'{}')
        store.update(
            snapshot['revision'],
            lambda data: {'providers': {'example': {'models': [{'model': 'm'}]}}}
        )
        self.assertTrue(self.path.is_file())
        self.assertEqual(self.path.stat().st_mode & 0o777, 0o600)
        self.assertFalse(self.recovery.exists())


if __name__ == '__main__':
    unittest.main()
