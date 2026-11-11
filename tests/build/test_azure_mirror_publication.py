"""Exercise mirror verification and retry behavior with a local Azure CLI stand-in."""

import hashlib
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import textwrap
import unittest


SCRIPT = Path(__file__).resolve().parents[2] / 'build/publish-azure-mirror.py'
TAG = 'v18.10.0-42.1'


class AzureMirrorPublicationTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.assets = self.root / 'assets'
        self.blobs = self.root / 'blobs'
        tools = self.root / 'tools'
        for path in (self.assets, self.blobs, tools):
            path.mkdir()
        az = tools / 'az'
        az.write_text(f'#!{sys.executable}\n' + textwrap.dedent('''
            import os
            from pathlib import Path
            import shutil
            import sys

            args = sys.argv[1:]
            assert args[:2] == ['storage', 'blob']
            mode = args[args.index('--auth-mode') + 1]
            assert mode == os.environ.get('EXPECTED_AUTH_MODE', 'login')
            if mode == 'key':
                assert os.environ.get('AZURE_STORAGE_KEY') == 'test-account-key'
                assert 'test-account-key' not in args
            name = args[args.index('--name') + 1]
            blob = Path(os.environ['MOCK_BLOBS']) / name
            path = Path(args[args.index('--file') + 1])
            if args[2] == 'upload':
                assert args[args.index('--overwrite') + 1] == 'false'
                if blob.exists() or name == os.environ.get('FAIL_BLOB'):
                    sys.exit(1)
                shutil.copyfile(path, blob)
            elif args[2] == 'download' and blob.exists():
                shutil.copyfile(blob, path)
            else:
                sys.exit(1)
        '''))
        az.chmod(0o755)
        self.env = dict(os.environ, PATH=str(tools) + os.pathsep + os.environ['PATH'],
                        MOCK_BLOBS=str(self.blobs))
        self.env.pop('AZURE_STORAGE_KEY', None)
        version = TAG[1:]
        self.names = [f'teleport-v{version}-linux-amd64-bin.tar.gz',
                      f'teleport-v{version}-windows-amd64-bin.zip',
                      f'teleport-v{version}-darwin-amd64-bin.tar.gz',
                      f'teleport-v{version}-darwin-arm64-bin.tar.gz',
                      f'teleport-tools-{version}.pkg']
        for name in self.names:
            data = name.encode()
            (self.assets / name).write_bytes(data)
            (self.assets / f'{name}.sha256').write_text(f'{hashlib.sha256(data).hexdigest()}  {name}\n')

    def run_upload(self, auth_mode='login', **extra_env):
        return subprocess.run([sys.executable, str(SCRIPT), 'upload', '--directory', str(self.assets),
                               '--tag', TAG, '--account', 'testaccount', '--container', 'teleport-cdn',
                               '--auth-mode', auth_mode],
                              env=dict(self.env, **extra_env), capture_output=True, text=True)

    def test_account_key_authentication_and_retry(self):
        for _ in range(2):
            result = self.run_upload(auth_mode='key', AZURE_STORAGE_KEY='test-account-key',
                                     EXPECTED_AUTH_MODE='key')
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertNotIn('test-account-key', result.stdout + result.stderr)
        self.assertEqual(len(list(self.blobs.iterdir())), 10)

    def test_missing_account_key_fails_before_upload(self):
        result = self.run_upload(auth_mode='key')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('AZURE_STORAGE_KEY is required', result.stderr)
        self.assertFalse(list(self.blobs.iterdir()))

    def test_upload_and_repeat_are_idempotent(self):
        for _ in range(2):
            result = self.run_upload()
            self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(len(list(self.blobs.iterdir())), 10)
        for asset in self.assets.iterdir():
            self.assertEqual(asset.read_bytes(), (self.blobs / asset.name).read_bytes())

    def test_retry_finishes_interrupted_upload(self):
        result = self.run_upload(FAIL_BLOB=self.names[1])
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(len(list(self.blobs.iterdir())), 2)
        result = self.run_upload()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(len(list(self.blobs.iterdir())), 10)

    def test_different_existing_blob_is_never_overwritten(self):
        blob = self.blobs / self.names[0]
        blob.write_bytes(b'different content')
        result = self.run_upload()
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(blob.read_bytes(), b'different content')
        self.assertEqual(len(list(self.blobs.iterdir())), 1)

    def test_corrupt_or_missing_asset_prevents_any_upload(self):
        (self.assets / self.names[-1]).write_bytes(b'corrupt')
        result = self.run_upload()
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(list(self.blobs.iterdir()))
        (self.assets / self.names[-1]).unlink()
        result = self.run_upload()
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(list(self.blobs.iterdir()))

    def test_checksum_must_reference_its_own_archive(self):
        checksum = self.assets / f'{self.names[-1]}.sha256'
        checksum.write_text('0' * 64 + '  another-file\n')
        result = self.run_upload()
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(list(self.blobs.iterdir()))


if __name__ == '__main__':
    unittest.main()
