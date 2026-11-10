"""Verify stamped versions and the upstream mirror archive contract."""

import hashlib
import importlib.util
import json
from pathlib import Path
import tarfile
import tempfile
import unittest


ROOT = Path(__file__).resolve().parents[2]


def module(name, path):
    spec = importlib.util.spec_from_file_location(name, ROOT / path)
    result = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(result)
    return result


stamp = module('stamp_version', 'build/stamp-version.py')
mirror = module('package_mirror', 'build/package-mirror.py')


class MirrorArtifactTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.source = self.root / 'source'
        (self.source / 'api').mkdir(parents=True)
        (self.source / 'api/version.go').write_text('''package api
const Version = "18.10.0"
const VersionMajor = 18
const VersionMinor = 10
const VersionPatch = 0
const VersionPreRelease = ""
const VersionMetadata = ""
''')
        self.artifacts = self.root / 'artifacts'
        for name in mirror.BINARIES:
            path = self.artifacts / 'bin' / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_bytes(name.encode())
            path.chmod(0o755)
        (self.artifacts / 'manifest.json').write_text(json.dumps({
            'teleport_version': '18.10.0-10.1', 'os': 'linux', 'architecture': 'amd64',
            'binaries': {name: {'sha256': hashlib.sha256(name.encode()).hexdigest()}
                         for name in mirror.BINARIES},
        }))
        (self.artifacts / 'test-results').mkdir()
        (self.artifacts / 'test-results/PASSED').write_text('pass')
        for relative in ('build.assets/install', 'assets/install-scripts/install-selinux.sh',
                         'examples/systemd/teleport.service', 'README.md', 'CHANGELOG.md',
                         'build.assets/LICENSE-community'):
            path = self.source / relative
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text(relative)

    def test_stamp_all_semver_constants(self):
        self.assertEqual(stamp.stamp(self.source, '18.10.0', '10.1'), '18.10.0-10.1')
        text = (self.source / 'api/version.go').read_text()
        self.assertIn('const Version = "18.10.0-10.1"', text)
        self.assertIn('const VersionPreRelease = "10.1"', text)
        self.assertIn('const VersionPatch = 0', text)

    def test_reject_invalid_or_mismatched_versions(self):
        for upstream, build in [('18.10.0-rc.1', '10.1'), ('18.10.0', '01.1'),
                                ('18.10.1', '10.1'), ('18.10.0', '1;id')]:
            with self.subTest(upstream=upstream, build=build), self.assertRaises(ValueError):
                stamp.stamp(self.source, upstream, build)

    def test_archive_contains_updater_paths_and_checksum(self):
        archive = mirror.package(self.source, self.artifacts, self.root / 'mirror', '18.10.0-10.1')
        checksum, filename = Path(str(archive) + '.sha256').read_text().split()
        self.assertEqual(filename, archive.name)
        self.assertEqual(checksum, hashlib.sha256(archive.read_bytes()).hexdigest())
        with tarfile.open(archive) as tar:
            for name in mirror.BINARIES:
                self.assertEqual(tar.extractfile(f'teleport/{name}').read(), name.encode())
                self.assertTrue(tar.getmember(f'teleport/{name}').mode & 0o111)
            self.assertEqual(tar.extractfile('teleport/VERSION').read(), b'v18.10.0-10.1\n')
            tar.getmember('teleport/examples/systemd/teleport.service')
        with self.assertRaises(FileExistsError):
            mirror.package(self.source, self.artifacts, self.root / 'mirror', '18.10.0-10.1')

    def test_corrupted_binary_is_not_packaged(self):
        (self.artifacts / 'bin/teleport').write_bytes(b'corrupt')
        with self.assertRaisesRegex(ValueError, 'Binary checksum mismatch'):
            mirror.package(self.source, self.artifacts, self.root / 'mirror', '18.10.0-10.1')


if __name__ == '__main__':
    unittest.main()
