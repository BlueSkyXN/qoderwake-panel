from pathlib import Path
import json
import os
import shutil
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]
SCRIPTS = [
    ROOT / 'panel/restart-panel.sh',
    ROOT / 'ops/qw-ctl.sh',
    ROOT / 'ops/start-cn-daemon-gw.sh',
    ROOT / 'ops/start-cn-daemon-real.sh',
    ROOT / 'ops/start-cn-daemon-sdkflag.sh'
]


class StartScriptTests(unittest.TestCase):
    def test_shell_syntax(self):
        subprocess.run(['bash', '-n', *map(str, SCRIPTS)], check=True)

    def test_no_heuristic_process_control(self):
        forbidden = (
            'pkill', 'pgrep', 'connect_ex', 'ss -tlnp',
            'daemon-start-mode.json', 'panel.pid'
        )
        for path in SCRIPTS:
            content = path.read_text()
            for token in forbidden:
                self.assertNotIn(token, content, '%s contains %s' % (path, token))

    def test_scripts_delegate_to_process_controller(self):
        restart = (ROOT / 'panel/restart-panel.sh').read_text()
        control = (ROOT / 'ops/qw-ctl.sh').read_text()
        self.assertIn('process-control.py', restart)
        self.assertIn('process-control.py', control)
        self.assertIn('--expected-version 0.12.1', restart)
        for path in SCRIPTS[2:]:
            self.assertIn('qw-ctl.sh', path.read_text())

    def test_restart_selects_tls_health_without_disabling_verification(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / 'panel').mkdir()
            (root / 'ops').mkdir()
            shutil.copyfile(ROOT / 'panel/restart-panel.sh', root / 'panel/restart-panel.sh')
            (root / 'ops/process-control.py').write_text(
                'import json,sys;print(json.dumps(sys.argv[1:]))')
            env = {key: value for key, value in os.environ.items() if not key.startswith('QW_')}
            env.update(QW_ROOT=str(root / 'data'), PYTHONDONTWRITEBYTECODE='1')
            plain = subprocess.run(['bash', str(root / 'panel/restart-panel.sh')],
                                   env=env, check=True, capture_output=True, text=True)
            plain_args = json.loads(plain.stdout)
            self.assertEqual(plain_args[plain_args.index('--health-url') + 1],
                             'http://127.0.0.1:19831/api/health')
            self.assertNotIn('--health-ca-file', plain_args)
            env.update(QW_ROOT=str(root / 'data'), QW_TLS_CERT='fixture.pem',
                       QW_TLS_KEY='fixture.key', QW_HEALTH_CA_FILE='fixture-ca.pem',
                       QW_HEALTH_SERVER_NAME='panel.example.test', PYTHONDONTWRITEBYTECODE='1')
            result = subprocess.run(['bash', str(root / 'panel/restart-panel.sh')],
                                    env=env, check=True, capture_output=True, text=True)
            args = json.loads(result.stdout)
            self.assertEqual(args[args.index('--health-url') + 1],
                             'https://127.0.0.1:19831/api/health')
            self.assertEqual(args[args.index('--health-ca-file') + 1], 'fixture-ca.pem')
            self.assertEqual(args[args.index('--health-server-name') + 1], 'panel.example.test')
            env.pop('QW_TLS_KEY')
            result = subprocess.run(['bash', str(root / 'panel/restart-panel.sh')],
                                    env=env, capture_output=True, text=True)
            self.assertNotEqual(result.returncode, 0)
            self.assertEqual(json.loads(result.stdout)['error'], 'both_tls_files_required')

    def test_direct_and_gateway_wrappers_are_explicit(self):
        direct = (ROOT / 'ops/start-cn-daemon-real.sh').read_text()
        sdk = (ROOT / 'ops/start-cn-daemon-sdkflag.sh').read_text()
        gateway = (ROOT / 'ops/start-cn-daemon-gw.sh').read_text()
        self.assertIn('QW_DAEMON_MODE=direct', direct)
        self.assertIn('QW_DAEMON_MODE=direct', sdk)
        self.assertIn('QW_DAEMON_MODE=gateway', gateway)
        self.assertNotIn('QODERWAKE_ENDPOINT_BASE_URL=', direct)
        self.assertIn('QW_GW_URL=', gateway)

    def test_patch_cli_passes_explicit_runtime_gateway_port(self):
        script = (ROOT / 'ops/patch-qcs-endpoint.py').read_text()
        self.assertIn("os.environ.get('QW_GW_PORT')", script)
        self.assertIn('args.gateway_port', script)
        self.assertIn("apply/restore requires --gateway-port or QW_GW_PORT",
                      script)
        self.assertNotIn('PatchManager(args.root, args.binary, args.registry)',
                         script)


if __name__ == '__main__':
    unittest.main()
