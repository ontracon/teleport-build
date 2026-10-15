"""Release gates must reject baseline, stale, and damaged artifact exports."""

import hashlib
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest


spec = importlib.util.spec_from_file_location(
    'verify_release', Path(__file__).resolve().parents[2] / 'build/verify-release.py')
release = importlib.util.module_from_spec(spec)
spec.loader.exec_module(release)


class VerifyReleaseTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.repo = Path(temporary.name)
        self.root = self.repo / 'dist/linux-amd64'
        self.version = '18.10.0'
        self.commit = 'a' * 40
        sources = {
            'integration/README.md': 'Integration documentation fixture\n',
            'integration/upstream.commit': self.commit + '\n',
            'integration/patches/series': '0001-test.patch\n',
            'integration/patches/0001-test.patch': 'patch fixture\n',
            'integration/overlay/lib/auth/zitadel.go': 'package auth\n',
            'oidc-adapter/config.go': 'package zitadeloidc\n',
            'oidc-adapter/config.example.json': '{}\n',
        }
        fingerprints = {}
        for name, content in sources.items():
            path = self.repo / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text(content)
            key = name.replace('oidc-adapter/', 'lib/zitadeloidc/')
            fingerprints[key] = hashlib.sha256(content.encode()).hexdigest()
        (self.root / 'bin').mkdir(parents=True)
        binaries = {}
        for name in ('teleport', 'tctl', 'tsh', 'tbot', 'fdpass-teleport', 'teleport-update'):
            payload = ('fixture for ' + name).encode()
            (self.root / 'bin' / name).write_bytes(payload)
            binaries[name] = {'sha256': hashlib.sha256(payload).hexdigest()}
        (self.root / 'test-results').mkdir()
        (self.root / 'test-results/PASSED').write_text('Passed\n')
        self.manifest = {
            'os': 'linux', 'architecture': 'amd64',
            'teleport_version': self.version, 'source_commit': self.commit,
            'integration': {'provider': 'zitadel', 'files': fingerprints},
            'binaries': binaries,
        }

    def verify(self):
        (self.root / 'manifest.json').write_text(json.dumps(self.manifest))
        return release.verify(self.root, self.repo, self.version, self.commit)

    def test_valid_release(self):
        self.assertEqual(self.verify(), self.version)

    def test_upstream_build_is_rejected(self):
        del self.manifest['integration']
        with self.assertRaisesRegex(ValueError, 'missing the Zitadel integration'):
            self.verify()

    def test_stale_patch_is_rejected(self):
        (self.repo / 'integration/patches/0001-test.patch').write_text('changed\n')
        with self.assertRaisesRegex(ValueError, 'integration hashes'):
            self.verify()

    def test_missing_adapter_hash_is_rejected(self):
        del self.manifest['integration']['files']['lib/zitadeloidc/config.go']
        with self.assertRaisesRegex(ValueError, 'integration hashes'):
            self.verify()

    def test_corrupt_binary_is_rejected(self):
        (self.root / 'bin/teleport').write_bytes(b'corrupt')
        with self.assertRaisesRegex(ValueError, 'checksum mismatch: teleport'):
            self.verify()

    def test_missing_binary_record_is_rejected(self):
        del self.manifest['binaries']['tctl']
        with self.assertRaisesRegex(ValueError, 'all six binaries'):
            self.verify()

    def test_unsupported_pin_is_rejected(self):
        (self.repo / 'integration/upstream.commit').write_text('b' * 40)
        with self.assertRaisesRegex(ValueError, 'integration/upstream.commit'):
            self.verify()

    def test_missing_test_result_is_rejected(self):
        (self.root / 'test-results/PASSED').rename(self.root / 'test-results/FAILED')
        with self.assertRaisesRegex(ValueError, 'passing test results'):
            self.verify()

    def test_wrong_platform_is_rejected(self):
        self.manifest['architecture'] = 'arm64'
        with self.assertRaisesRegex(ValueError, 'Linux amd64'):
            self.verify()

    def test_wrong_version_is_rejected(self):
        self.manifest['teleport_version'] = '18.9.0'
        with self.assertRaisesRegex(ValueError, 'version or source commit'):
            self.verify()


if __name__ == '__main__':
    unittest.main()
