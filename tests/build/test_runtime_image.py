"""Guard the production launch contract without changing development labs."""

from http.client import RemoteDisconnected
import importlib.util
import json
from pathlib import Path
import re
import unittest
from unittest.mock import patch


ROOT = Path(__file__).resolve().parents[2]
SPEC = importlib.util.spec_from_file_location('runtime_smoke', ROOT / 'build/test-runtime.py')
SMOKE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(SMOKE)


class RuntimeImageTests(unittest.TestCase):
    def test_production_overrides_do_not_leak_into_lab(self):
        dockerfile = (ROOT / 'build/Dockerfile').read_text()
        base = dockerfile.split(' AS runtime-base\n', 1)[1].split('\nFROM ', 1)[0]
        runtime = dockerfile.split('FROM runtime-base AS runtime\n', 1)[1]
        self.assertIn('FROM runtime-base AS lab\n', dockerfile)
        self.assertIn('ENTRYPOINT ["/usr/local/bin/teleport"]', base)
        self.assertIn('CMD ["start", "--config=/etc/teleport.yaml"]', base)
        self.assertNotIn('dumb-init', base)
        self.assertNotIn('TELEPORT_TOOLS_VERSION', base)
        self.assertNotIn('STOPSIGNAL', base)
        self.assertIn('STOPSIGNAL SIGQUIT', runtime)
        self.assertIn('ENV TELEPORT_TOOLS_VERSION=off', runtime)
        entrypoint = re.search(r'^ENTRYPOINT (.+)$', runtime, re.MULTILINE)[1]
        self.assertEqual(json.loads(entrypoint), SMOKE.ENTRYPOINT)
        self.assertIn('CMD []', runtime)

    def test_image_contract_rejects_incompatible_metadata(self):
        config = dict(Entrypoint=SMOKE.ENTRYPOINT, Cmd=[], StopSignal='SIGQUIT',
                      Env=['TELEPORT_TOOLS_VERSION=off'])
        with patch.object(SMOKE, 'run', return_value=json.dumps([dict(Config=config)])):
            SMOKE.check_image('fixture')
        for setting in (dict(Entrypoint=['teleport']), dict(Cmd=['start']),
                        dict(StopSignal='SIGTERM'), dict(Env=[])):
            with self.subTest(setting=setting), patch.object(
                    SMOKE, 'run', return_value=json.dumps([dict(Config=config | setting)])):
                with self.assertRaises(RuntimeError):
                    SMOKE.check_image('fixture')

    def test_readiness_retries_connections_closed_during_startup(self):
        state = json.dumps([dict(State=dict(Running=True))])
        responses = [RemoteDisconnected('not listening yet'), b'ok', b'ok',
                     json.dumps(dict(server_version='18.10.0')).encode()]
        with patch.object(SMOKE, 'run', return_value=state), \
                patch.object(SMOKE, 'fetch', side_effect=responses), \
                patch.object(SMOKE.time, 'sleep') as sleep:
            SMOKE.wait_ready('fixture', 'http://diag', 'https://proxy', '18.10.0')
        sleep.assert_called_once_with(1)

    def test_readiness_rejects_wrong_version_or_exited_container(self):
        state = json.dumps([dict(State=dict(Running=True))])
        responses = [b'ok', b'ok', json.dumps(dict(server_version='wrong')).encode()]
        with patch.object(SMOKE, 'run', return_value=state), \
                patch.object(SMOKE, 'fetch', side_effect=responses), \
                self.assertRaisesRegex(RuntimeError, 'Wrong server version'):
            SMOKE.wait_ready('fixture', 'http://diag', 'https://proxy', '18.10.0')
        with patch.object(SMOKE, 'run', return_value=json.dumps([dict(State=dict(Running=False))])), \
                self.assertRaisesRegex(RuntimeError, 'exited before readiness'):
            SMOKE.wait_ready('fixture', 'http://diag', 'https://proxy', '18.10.0')

    def test_read_chart_literal_blocks(self):
        document = '''apiVersion: v1
kind: ConfigMap
data:
  apply-on-startup.yaml: |2
    ---
    kind: token
    spec:
      roles: [Proxy]
  teleport.yaml: |2
    auth_service:
      enabled: true
    version: v3
'''
        self.assertEqual(SMOKE.configmap_block(document, 'apply-on-startup.yaml'),
                         '---\nkind: token\nspec:\n  roles: [Proxy]\n')
        self.assertEqual(SMOKE.configmap_block(document, 'teleport.yaml'),
                         'auth_service:\n  enabled: true\nversion: v3\n\n')
        for document in ('data: {}', 'data:\n  teleport.yaml: "not a literal"'):
            with self.subTest(document=document), self.assertRaises(RuntimeError):
                SMOKE.configmap_block(document, 'teleport.yaml')


if __name__ == '__main__':
    unittest.main()
