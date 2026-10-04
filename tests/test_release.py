import importlib.util
from pathlib import Path
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('release_check', ROOT/'scripts/check_release.py')
release = importlib.util.module_from_spec(spec)
spec.loader.exec_module(release)


class ReleaseTests(unittest.TestCase):
    def test_runtime_generations_are_never_public_files(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            for name in release.REQUIRED:
                path = root/name
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_text('fixture')
            self.assertEqual(release.issues(root), [])
            path = root/'gateway-runtime/generation-fixture/config.json'
            path.parent.mkdir(parents=True)
            path.write_text('{}')
            self.assertEqual(release.issues(root), [(str(path.relative_to(root)), 'local/generated artifact must not be published')])
            path.unlink()
            path = root/'process-state/daemon-cn.json'
            path.parent.mkdir(parents=True)
            path.write_text('{}')
            self.assertEqual(release.issues(root), [(str(path.relative_to(root)), 'local/generated artifact must not be published')])

    def test_gateway_dependencies_are_required(self):
        for name in ('panel/gateway_policy.py', 'panel/gateway_runtime.py', 'ops/gateway-manager.py', 'ops/process-control.py', 'ops/uplink-gw.py'):
            self.assertIn(name, release.REQUIRED)

    def test_provider_transaction_module_and_recovery_are_guarded(self):
        self.assertIn('panel/provider_config.py', release.REQUIRED)
        self.assertIn('panel/deletion_preflight.py', release.REQUIRED)
        self.assertIn('panel/daemon_transport.py', release.REQUIRED)
        self.assertIn('panel/gateway_runtime.py', release.REQUIRED)
        self.assertIn('ops/process-control.py', release.REQUIRED)
        self.assertIn('docs/development-012.md', release.REQUIRED)
        self.assertIn('provider-settings.previous.json', release.FORBIDDEN)
        self.assertIn('operation.json', release.FORBIDDEN)
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            for name in release.REQUIRED:
                path = root/name
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_text('fixture')
            legacy = root/'settings.panel-bak-fixture.json'
            legacy.write_text('{"providers":{"fixture":{"apiKey":"synthetic-value"}}}')
            self.assertIn(
                (legacy.name, 'local/generated artifact must not be published'),
                release.issues(root)
            )
            recovery = root/'provider-settings.previous.json'
            recovery.write_text('{"providers":{"private":{"apiKey":"redacted"}}}')
            self.assertIn(
                ('provider-settings.previous.json', 'local/generated artifact must not be published'),
                release.issues(root)
            )


if __name__ == '__main__':
    unittest.main()
