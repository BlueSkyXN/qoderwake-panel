import importlib.util
import json
import os
from pathlib import Path
import socket
import subprocess
import sys
import tempfile
import time
import unittest
import urllib.request
from unittest.mock import patch
from types import SimpleNamespace

PANEL=Path(__file__).resolve().parents[1]/'panel'
sys.path.insert(0,str(PANEL))
from panel_security import Security
spec=importlib.util.spec_from_file_location('panel_launch',PANEL/'run-panel.py')
launch=importlib.util.module_from_spec(spec);spec.loader.exec_module(launch)


class PanelLaunchTests(unittest.TestCase):
    def test_launchagent_preserves_explicit_mode_without_copying_unrelated_secrets(self):
        with tempfile.TemporaryDirectory() as tmp:
            root=Path(tmp)
            args=SimpleNamespace(root=root,home=root/'home',bind='127.0.0.1',port=19831,legacy=True)
            value=launch.launchagent(args,{'QW_DAEMON_URL':'http://127.0.0.1:19820',
                'QW_DAEMON_BIN':'/fixture/qoderwake','QW_UNRELATED_SECRET':'fixture-sensitive',
                'PYTHONHOME':'/fixture/python','QW_DAEMON_CREDENTIAL_SERVICE':'qoderwake.auth.v1'})
            self.assertIn('--legacy',value['ProgramArguments'])
            self.assertEqual(value['EnvironmentVariables']['PYTHONHOME'],'/fixture/python')
            self.assertNotIn('QW_UNRELATED_SECRET',value['EnvironmentVariables'])
            self.assertTrue(value['RunAtLoad'])

    def test_bind_does_not_depend_on_reverse_dns(self):
        import test_panel
        with patch.object(socket, 'getfqdn', side_effect=AssertionError('DNS must not run')):
            server=test_panel.app.PanelHTTPServer(('127.0.0.1',0),test_panel.app.H)
            try:
                self.assertEqual(server.server_name,'127.0.0.1')
                self.assertGreater(server.server_port,0)
            finally:server.server_close()

    def test_port_discovery_and_explicit_endpoint(self):
        with tempfile.TemporaryDirectory() as tmp:
            home=Path(tmp);(home/'run').mkdir()
            self.assertEqual(launch.daemon_url(home),'http://127.0.0.1:19830')
            (home/'run/daemon.port').write_text('19820\n')
            self.assertEqual(launch.daemon_url(home),'http://127.0.0.1:19820')
            self.assertEqual(launch.daemon_url(home,'http://127.0.0.1:1234'),'http://127.0.0.1:1234')
            (home/'run/daemon.port').write_text('99999')
            self.assertEqual(launch.daemon_url(home),'http://127.0.0.1:19830')

    def test_instance_lock_releases_without_removing_shared_lock_file(self):
        with tempfile.TemporaryDirectory() as tmp:
            root=Path(tmp)
            with launch.acquire_instance(root):
                with self.assertRaises(RuntimeError):launch.acquire_instance(root)
            with launch.acquire_instance(root):pass
            self.assertTrue((root/'panel-service.lock').is_file())

    def test_real_service_starts_once_restarts_and_keeps_accounts(self):
        with tempfile.TemporaryDirectory() as tmp:
            root=Path(tmp)
            user=Security(root).bootstrap_admin('owner','Owner','fixture-launch-password')
            with socket.socket() as probe:
                probe.bind(('127.0.0.1',0));port=probe.getsockname()[1]
            env={k:v for k,v in os.environ.items() if not k.startswith('QW_')}
            env['PYTHONDONTWRITEBYTECODE']='1'
            command=[sys.executable,str(PANEL/'run-panel.py'),'--root',str(root),'--home',str(root/'home'),
                     '--port',str(port),'--daemon-url','http://127.0.0.1:1']
            for attempt in range(2):
                process=subprocess.Popen(command,env=env,stdout=subprocess.PIPE,stderr=subprocess.PIPE,text=True)
                try:
                    deadline=time.monotonic()+8
                    while True:
                        try:
                            with urllib.request.urlopen('http://127.0.0.1:%d/api/health'%port,timeout=.3) as response:
                                self.assertTrue(json.load(response)['ok']);break
                        except OSError:
                            if process.poll() is not None or time.monotonic()>deadline:
                                self.fail('Panel did not start')
                            time.sleep(.05)
                    duplicate=subprocess.run(command,env=env,capture_output=True,text=True,timeout=5)
                    self.assertNotEqual(duplicate.returncode,0)
                    self.assertIn('已有 Panel',duplicate.stderr)
                finally:
                    process.terminate();process.communicate(timeout=5)
                self.assertEqual(Security(root).users()[0]['id'],user['id'])


if __name__=='__main__':unittest.main()
