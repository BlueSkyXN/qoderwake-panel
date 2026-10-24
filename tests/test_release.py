import importlib.util
import os
from pathlib import Path
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('release_check', ROOT/'scripts/check_release.py')
release = importlib.util.module_from_spec(spec)
spec.loader.exec_module(release)


class ReleaseTests(unittest.TestCase):
    def test_private_path_rule_does_not_reject_user_management_routes(self):
        route='/api/users/create'
        private='/'+'Users'+'/person/source'
        self.assertFalse(any(pattern.search(route) for pattern in release.PATTERNS))
        self.assertTrue(any(pattern.search(private) for pattern in release.PATTERNS))

    def test_go_patch_source_allowed_but_patch_runtime_data_rejected(self):
        with tempfile.TemporaryDirectory() as tmp:
            root=Path(tmp)
            for name in release.REQUIRED:
                path=root/name;path.parent.mkdir(parents=True,exist_ok=True);path.write_text('fixture')
            path=root/'internal/patches/patches.go'
            path.parent.mkdir(parents=True);path.write_text('package patches')
            self.assertEqual(release.issues(root),[])
            runtime=root/'internal/patches/current.json';runtime.write_text('{}')
            self.assertIn((str(runtime.relative_to(root)),'local/generated artifact must not be published'),release.issues(root))

    def test_selected_source_does_not_scan_ignored_build_outputs(self):
        with tempfile.TemporaryDirectory() as tmp:
            root=Path(tmp)
            for name in release.REQUIRED:
                path=root/name;path.parent.mkdir(parents=True,exist_ok=True);path.write_text('fixture')
            path=root/'web/node_modules/example/file';path.parent.mkdir(parents=True);path.write_text('fixture')
            self.assertEqual(release.issues(root,release.REQUIRED),[])
            self.assertIn((str(path.relative_to(root)),'local/generated artifact must not be published'),release.issues(root))

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

    def test_all_symlink_types_and_special_files_are_rejected(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp) / 'candidate'
            root.mkdir()
            for name in release.REQUIRED:
                path = root / name
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_text('fixture')
            outside = Path(tmp) / 'outside'
            outside.mkdir()
            (outside / 'note.txt').write_text('synthetic-private-fixture')
            (root / 'directory-link').symlink_to(outside, target_is_directory=True)
            (root / 'broken-link').symlink_to(Path(tmp) / 'missing')
            (root / 'file-link').symlink_to(outside / 'note.txt')
            os.mkfifo(root / 'named-pipe')
            found = dict(release.issues(root))
            for name in ('directory-link', 'broken-link', 'file-link'):
                self.assertEqual(found[name], 'symlink excluded from release')
            self.assertEqual(found['named-pipe'], 'nonregular entry excluded from release')

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
