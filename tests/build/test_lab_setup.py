"""Check mock and real lab client authentication settings."""

import importlib.util
import json
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch


ROOT = Path(__file__).resolve().parents[2]
SPEC = importlib.util.spec_from_file_location('lab_setup', ROOT / 'dev/setup.py')
SETUP = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(SETUP)


class LabAuthenticationTests(unittest.TestCase):
    def test_client_secret_selection(self):
        for mock in (False, True):
            for existing in (False, True):
                with self.subTest(mock=mock, existing=existing), tempfile.TemporaryDirectory() as directory:
                    root = Path(directory)
                    for name in ('ca.pem', 'idp.pem', 'teleport.pem'):
                        (root / name).write_text('test certificate\n')
                    config = json.loads((ROOT / 'oidc-adapter/config.example.json').read_text())
                    config['issuer_url'] = 'https://localhost:9443' if mock else 'https://idp.example'
                    original_config = json.dumps(config)
                    (root / 'adapter.json').write_text(original_config)
                    secret_path = root / 'client-secret'
                    if existing:
                        secret_path.write_text('existing-secret\n')
                    with patch.object(sys, 'argv', ['setup.py', directory]), patch.object(SETUP, 'run') as run:
                        SETUP.main()
                    run.assert_not_called()
                    bootstrap = (root / 'bootstrap.yaml').read_text()
                    if existing:
                        self.assertEqual(secret_path.read_text(), 'existing-secret\n')
                        self.assertIn('client_secret: "existing-secret"', bootstrap)
                    elif mock:
                        secret = secret_path.read_text().strip()
                        self.assertTrue(secret)
                        self.assertIn('client_secret: ' + json.dumps(secret), bootstrap)
                    else:
                        self.assertFalse(secret_path.exists())
                        self.assertIn('client_secret: ""', bootstrap)
                    self.assertEqual((root / 'adapter.json').read_text(), original_config)


if __name__ == '__main__':
    unittest.main()
