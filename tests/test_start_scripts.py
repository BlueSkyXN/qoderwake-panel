from pathlib import Path
import subprocess
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
        self.assertIn('--expected-version 0.12.0', restart)
        for path in SCRIPTS[2:]:
            self.assertIn('qw-ctl.sh', path.read_text())

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
