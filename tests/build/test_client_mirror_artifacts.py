"""Check the client archives expected by the pinned Teleport updater."""

import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys
import tarfile
import tempfile
import unittest
import zipfile


SCRIPT = Path(__file__).resolve().parents[2] / 'build/package-client-mirror.py'
VERSION = '18.10.0-10.1'
COMMIT = 'a' * 40


class ClientMirrorArtifactsTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.output = self.root / 'out'

    def client(self, platform, arch, binaries):
        directory = self.root / f'{platform}-{arch}'
        (directory / 'bin').mkdir(parents=True)
        hashes = {}
        for name, content in binaries.items():
            path = directory / 'bin' / name
            path.write_bytes(content)
            path.chmod(0o755)
            hashes[name] = hashlib.sha256(content).hexdigest()
        (directory / 'mirror-client.json').write_text(json.dumps({
            'teleport_version': VERSION, 'source_commit': COMMIT, 'os': platform,
            'architecture': arch, 'binaries': hashes}))
        (directory / 'LICENSE').write_text('fixture license\n')
        return directory

    def run_packager(self, *args):
        return subprocess.run([sys.executable, str(SCRIPT), '--version', VERSION,
            '--commit', COMMIT, '--output', str(self.output), *map(str, args)],
            capture_output=True, text=True)

    def test_windows_archive_and_integrity(self):
        binaries = {'tsh.exe': b'client', 'tctl.exe': b'admin'}
        directory = self.client('windows', 'amd64', binaries)
        result = self.run_packager('--windows', directory)
        self.assertEqual(result.returncode, 0, result.stderr)
        archive = self.output / f'teleport-v{VERSION}-windows-amd64-bin.zip'
        with zipfile.ZipFile(archive) as package:
            self.assertEqual(set(package.namelist()), {'teleport/tsh.exe', 'teleport/tctl.exe'})
            for name, content in binaries.items():
                self.assertEqual(package.read('teleport/' + name), content)
        checksum, name = archive.with_name(archive.name + '.sha256').read_text().split()
        self.assertEqual((checksum, name), (hashlib.sha256(archive.read_bytes()).hexdigest(), archive.name))
        self.assertNotEqual(self.run_packager('--windows', directory).returncode, 0)

    def test_rejects_modified_binary_and_wrong_source(self):
        directory = self.client('windows', 'amd64', {'tsh.exe': b'client', 'tctl.exe': b'admin'})
        (directory / 'bin/tctl.exe').write_bytes(b'changed')
        self.assertNotEqual(self.run_packager('--windows', directory).returncode, 0)
        self.assertFalse(self.output.exists())
        (directory / 'bin/tctl.exe').write_bytes(b'admin')
        manifest = directory / 'mirror-client.json'
        data = json.loads(manifest.read_text())
        data['source_commit'] = 'b' * 40
        manifest.write_text(json.dumps(data))
        self.assertNotEqual(self.run_packager('--windows', directory).returncode, 0)
        self.assertFalse(self.output.exists())

    def test_macos_host_archives_contain_the_agent_and_tools(self):
        names = ('tsh', 'tctl', 'teleport', 'tbot', 'fdpass-teleport')
        for arch in ('amd64', 'arm64'):
            client = self.client('darwin', arch, {name: name.encode() for name in names})
            result = self.run_packager('--darwin-host', client, '--arch', arch)
            self.assertEqual(result.returncode, 0, result.stderr)
            archive = self.output / f'teleport-v{VERSION}-darwin-{arch}-bin.tar.gz'
            with tarfile.open(archive) as tar:
                self.assertEqual(tar.extractfile('teleport/teleport').read(), b'teleport')
                self.assertEqual(tar.extractfile('teleport/VERSION').read(), f'v{VERSION}\n'.encode())
                self.assertTrue({f'teleport/{name}' for name in names}.issubset(tar.getnames()))
            checksum, name = archive.with_name(archive.name + '.sha256').read_text().split()
            self.assertEqual((checksum, name), (hashlib.sha256(archive.read_bytes()).hexdigest(), archive.name))

    @unittest.skipUnless(sys.platform == 'darwin', 'macOS packaging requires pkgbuild and pkgutil')
    def test_universal_macos_pkg_can_be_expanded(self):
        clients = []
        for arch, clang_arch in (('amd64', 'x86_64'), ('arm64', 'arm64')):
            program = self.root / f'program-{arch}'
            subprocess.run(['clang', '-arch', clang_arch, '-x', 'c', '-', '-o', str(program)],
                           input='#include <stdio.h>\nint main(void) { puts("Teleport v18.10.0-10.1 test"); return 0; }\n',
                           text=True, check=True)
            clients.append(self.client('darwin', arch, {name: program.read_bytes() for name in
                ('tsh', 'tctl', 'teleport', 'tbot', 'fdpass-teleport')}))
            result = self.run_packager('--darwin-host', clients[-1], '--arch', arch)
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
            host_archive = self.output / f'teleport-v{VERSION}-darwin-{arch}-bin.tar.gz'
            with tarfile.open(host_archive) as tar:
                self.assertTrue({'teleport/tsh', 'teleport/tctl', 'teleport/teleport',
                                 'teleport/tbot', 'teleport/fdpass-teleport',
                                 'teleport/LICENSE', 'teleport/VERSION'}.issubset(tar.getnames()))
        result = self.run_packager('--darwin-amd64', clients[0], '--darwin-arm64', clients[1])
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        archive = self.output / f'teleport-tools-{VERSION}.pkg'
        expanded = self.root / 'expanded'
        subprocess.run(['pkgutil', '--expand-full', str(archive), str(expanded)], check=True)
        for name in ('tsh', 'tctl'):
            paths = [path for path in expanded.rglob(name) if path.is_file()]
            self.assertEqual(len(paths), 1)
            subprocess.run(['lipo', str(paths[0]), '-verify_arch', 'x86_64', 'arm64'], check=True)
        self.assertTrue(archive.with_name(archive.name + '.sha256').is_file())
        if os.environ.get('TELEPORT_SOURCE_DIR'):
            verifier = SCRIPT.with_name('verify-client-mirror.go')
            subprocess.run(['go', 'run', str(verifier), str(archive),
                            str(self.root / 'upstream-extract'), VERSION],
                           cwd=os.environ['TELEPORT_SOURCE_DIR'], check=True)


if __name__ == '__main__':
    unittest.main()
