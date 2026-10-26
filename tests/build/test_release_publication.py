"""Exercise the actual publication step with a local GitHub CLI stand-in."""

import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import textwrap
import unittest


WORKFLOW = Path(__file__).resolve().parents[2] / '.github/workflows/build-linux-amd64.yml'
PUBLISH_STEP = textwrap.dedent(WORKFLOW.read_text().split(
    '      - name: Create release and upload tested artifacts\n', 1)[1].split(
    '        run: |\n', 1)[1])


class ReleasePublicationTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        (self.root / 'tools').mkdir()
        (self.root / 'releases').mkdir()
        gh = self.root / 'tools/gh'
        gh.write_text(f'#!{sys.executable}\n' + textwrap.dedent("""
            import json
            import os
            from pathlib import Path
            import shutil
            import sys

            args = sys.argv[1:]
            assert args[:2] == ['release', 'create'], args
            release = Path(os.environ['RELEASES_DIR']) / args[2]
            # Like gh release create, refuse to modify an existing release.
            release.mkdir()
            (release / 'arguments.json').write_text(json.dumps(args))
            notes = Path(args[args.index('--notes-file') + 1])
            shutil.copy2(notes, release / 'notes.md')
            for asset in args[3:args.index('--target')]:
                shutil.copy2(asset, release / Path(asset).name)
        """))
        gh.chmod(0o755)

    def publish(self, build='42.1', version='18.10.0', corrupt=False):
        dist = self.root / 'dist'
        if dist.exists():
            shutil.rmtree(dist)
        dist.mkdir()
        names = [f'teleport-{version}-linux-amd64.tar.gz']
        names.extend(f'tsh-{version}-{suffix}' for suffix in (
            'windows-amd64.zip', 'darwin-arm64.tar.gz', 'darwin-amd64.tar.gz'))
        for name in names:
            payload = f'{build}: {name}'.encode()
            (dist / name).write_bytes(payload)
            checksum = hashlib.sha256(payload).hexdigest()
            (dist / f'{name}.sha256').write_text(f'{checksum}  {name}\n')
        if corrupt:
            (dist / names[-1]).write_bytes(b'damaged')
        env = dict(os.environ,
                   PATH=str(self.root / 'tools') + os.pathsep + os.environ['PATH'],
                   RELEASES_DIR=str(self.root / 'releases'),
                   ARCHIVE=names[0], TELEPORT_TAG=f'v{version}',
                   TELEPORT_VERSION=version, TELEPORT_COMMIT='a' * 40,
                   GITHUB_SHA='b' * 40, RELEASE_BUILD=build,
                   RUNNER_TEMP=str(self.root),
                   CONTAINER_IMAGE=f'ghcr.io/ontracon/teleport-build:{version.replace("+", "-")}-build.{build}',
                   CONTAINER_DIGEST='sha256:' + 'c' * 64)
        result = subprocess.run(['bash', '-e', '-o', 'pipefail', '-c', PUBLISH_STEP],
                                cwd=self.root, env=env, capture_output=True, text=True)
        return result, self.root / 'releases' / f'v{version}-build.{build}'

    def assert_published(self, result, release, build):
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        archives = [path for path in release.iterdir()
                    if path.name.endswith(('.tar.gz', '.zip'))]
        self.assertEqual(len(archives), 4)
        for archive in archives:
            self.assertIn(f'-build.{build}-', archive.name)
            checksum, name = Path(str(archive) + '.sha256').read_text().split()
            self.assertEqual(name, archive.name)
            self.assertEqual(checksum, hashlib.sha256(archive.read_bytes()).hexdigest())
        args = json.loads((release / 'arguments.json').read_text())
        self.assertEqual(args[args.index('--target') + 1], 'b' * 40)
        self.assertTrue(args[args.index('--title') + 1].endswith(f'(build {build})'))
        notes = (release / 'notes.md').read_text()
        self.assertIn(f'Release build: `{build}`.', notes)
        self.assertIn('ghcr.io/ontracon/teleport-build@sha256:' + 'c' * 64, notes)
        self.assertIn('image: ghcr.io/ontracon/teleport-build', notes)
        self.assertIn(f'-build.{build}"', notes)
        self.assertNotIn('\\\\"', notes)

    def test_repeated_version_preserves_previous_release(self):
        result, first = self.publish()
        self.assert_published(result, first, '42.1')
        snapshot = {path.name: path.read_bytes() for path in first.iterdir()}
        result, second = self.publish(build='43.1')
        self.assert_published(result, second, '43.1')
        self.assertEqual(snapshot, {path.name: path.read_bytes() for path in first.iterdir()})

    def test_rerun_creates_separate_release(self):
        result, first = self.publish()
        self.assert_published(result, first, '42.1')
        result, second = self.publish(build='42.2')
        self.assert_published(result, second, '42.2')
        self.assertNotEqual(first, second)
        self.assertTrue(first.is_dir())

    def test_existing_numbered_release_is_not_modified(self):
        result, release = self.publish()
        self.assert_published(result, release, '42.1')
        snapshot = {path.name: path.read_bytes() for path in release.iterdir()}
        result, _ = self.publish()
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(snapshot, {path.name: path.read_bytes() for path in release.iterdir()})

    def test_prerelease_is_marked(self):
        result, release = self.publish(version='18.10.0-rc.1')
        self.assert_published(result, release, '42.1')
        self.assertIn('--prerelease', json.loads((release / 'arguments.json').read_text()))

    def test_stable_build_metadata_is_not_prerelease(self):
        result, release = self.publish(version='18.10.0+vendor-patch')
        self.assert_published(result, release, '42.1')
        self.assertNotIn('--prerelease', json.loads((release / 'arguments.json').read_text()))

    def test_corrupt_archive_prevents_publication(self):
        result, release = self.publish(corrupt=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(release.exists())


if __name__ == '__main__':
    unittest.main()
