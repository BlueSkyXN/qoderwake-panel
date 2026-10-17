import importlib.util
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest

SOURCE = Path(__file__).resolve().parents[1] / 'ops/qodercli-provider-bridge.py'
spec = importlib.util.spec_from_file_location('provider_bridge', SOURCE)
bridge = importlib.util.module_from_spec(spec)
spec.loader.exec_module(bridge)


class ProviderBridgeTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.config = self.root / 'user'
        self.config.mkdir()
        (self.root / '.qoder').mkdir()
        self.env = {'QODER_CONFIG_DIR': str(self.config), 'QODER_SDK_CUSTOM_BASE_URL_BYOK': '1'}
        self.settings = {'providers': {'fixture': {'apiKey': 'fixture-secret', 'models': [{'model': 'test'}]}},
                         'permissions': {'allow': ['Bash']}, 'hooks': {'SessionStart': ['user-hook']},
                         'security': {'fixture': True}}
        (self.config / 'settings.json').write_text(json.dumps(self.settings))

    def test_restricted_session_imports_only_provider_and_keeps_explicit_settings(self):
        args = ['--setting-sources', 'local', '--model', 'fixture/test', '--settings',
                '{"permissions":{"additionalDirectories":["fixture-dir"]}}']
        result = bridge.bridged_settings(args, self.env, self.root)
        self.assertEqual(set(result), {'providers', 'permissions'})
        self.assertEqual(result['providers'], self.settings['providers'])
        self.assertEqual(result['permissions'], {'additionalDirectories': ['fixture-dir']})
        self.assertEqual(json.loads((self.config / 'settings.json').read_text()), self.settings)

    def test_native_user_scope_and_disabled_byok_are_pass_through(self):
        for args, env in [([], self.env), (['--setting-sources=user,local'], self.env),
                          (['--setting-sources', 'local'], {**self.env, 'QODER_SDK_CUSTOM_BASE_URL_BYOK': '0'}),
                          (['--setting-sources', 'local'], {})]:
            self.assertIsNone(bridge.bridged_settings(args, env, self.root))

    def test_local_and_explicit_provider_precedence(self):
        (self.root / '.qoder/settings.local.json').write_text(json.dumps(
            {'providers': {'fixture': {'apiKey': 'local-key', 'baseUrl': 'https://example.test/v1'}}}))
        flag = self.root / 'flag.json'
        flag.write_text(json.dumps({'providers': {'fixture': {'apiKey': 'flag-key'}}, 'permissions': {}}))
        result = bridge.bridged_settings(['--setting-sources=local', '--settings', str(flag)], self.env, self.root)
        self.assertEqual(result['providers']['fixture']['apiKey'], 'flag-key')
        self.assertEqual(result['providers']['fixture']['baseUrl'], 'https://example.test/v1')
        self.assertEqual(result['providers']['fixture']['models'], [{'model': 'test'}])

    def test_reload_on_next_launch_and_no_provider_no_rewrite(self):
        path = self.config / 'settings.json'
        path.write_text('{}')
        self.assertIsNone(bridge.bridged_settings(['--setting-sources=local'], self.env, self.root))
        path.write_text(json.dumps(self.settings))
        self.assertIn('fixture', bridge.bridged_settings(['--setting-sources=local'], self.env, self.root)['providers'])

    def test_invalid_settings_do_not_leak_contents_or_silently_fallback(self):
        (self.config / 'settings.json').write_text('{"apiKey":"private-fixture", invalid}')
        with self.assertRaisesRegex(bridge.BridgeError, '^settings_json_invalid$'):
            bridge.bridged_settings(['--setting-sources=local'], self.env, self.root)

    def test_replace_removes_old_settings_and_preserves_prompt_after_separator(self):
        args = ['--setting-sources', 'local', '--settings', '{}', '--model', 'fixture/test',
                '--settings={}', '--', '--settings=prompt-text']
        self.assertEqual(bridge.replace_settings(args, '/proc/self/fd/3'),
                         ['--setting-sources', 'local', '--model', 'fixture/test', '--settings',
                          '/proc/self/fd/3', '--', '--settings=prompt-text'])

    @unittest.skipUnless(hasattr(os, 'memfd_create'), 'Linux anonymous settings descriptor')
    def test_exec_handoff_preserves_process_and_keeps_secrets_out_of_argv(self):
        executable = self.root / 'qodercli'
        shutil.copyfile(SOURCE, executable)
        executable.chmod(0o700)
        original = Path(str(executable) + '.original')
        original.write_text('#!/usr/bin/env python3\nimport json,os,sys\n'
                            'p=sys.argv[sys.argv.index("--settings")+1]\n'
                            'data=json.load(open(p))\n'
                            'assert data["providers"]["fixture"]["apiKey"]=="fixture-secret"\n'
                            'assert "fixture-secret" not in " ".join(sys.argv)\n'
                            'assert os.readlink(p).startswith("/memfd:")\n'
                            'print(json.dumps({"pid":os.getpid(),"keys":list(data)}))\n')
        original.chmod(0o700)
        process = subprocess.Popen([str(executable), '--setting-sources', 'local'],
                                   env={**os.environ, **self.env}, cwd=self.root,
                                   stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
        stdout, stderr = process.communicate(timeout=10)
        self.assertEqual(process.returncode, 0, stderr)
        self.assertEqual(json.loads(stdout), {'pid': process.pid, 'keys': ['providers']})
        self.assertEqual(list(self.config.iterdir()), [self.config / 'settings.json'])


if __name__ == '__main__':
    unittest.main()
