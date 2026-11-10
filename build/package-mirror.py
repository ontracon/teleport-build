"""Package verified binaries in the upstream Linux updater archive layout."""

import argparse
import hashlib
import json
from pathlib import Path
import re
import tarfile
import tempfile


BINARIES = ('teleport', 'teleport-update', 'tbot', 'tctl', 'tsh', 'fdpass-teleport')
VERSION = re.compile(r'^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)-(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$')


def package(source, artifacts, output, version):
    if not VERSION.fullmatch(version):
        raise ValueError('Expected a numeric-suffix Teleport distribution version')
    manifest = json.loads((artifacts / 'manifest.json').read_text())
    if (manifest.get('teleport_version'), manifest.get('os'), manifest.get('architecture')) != (version, 'linux', 'amd64'):
        raise ValueError('Artifact version or platform does not match the requested archive')
    if not (artifacts / 'test-results/PASSED').is_file():
        raise ValueError('Artifacts have not passed build verification')
    files = {f'teleport/{name}': artifacts / 'bin' / name for name in BINARIES}
    files.update({'teleport/install': source / 'build.assets/install',
                  'teleport/install-selinux.sh': source / 'assets/install-scripts/install-selinux.sh',
                  'teleport/examples/systemd/teleport.service': source / 'examples/systemd/teleport.service',
                  'teleport/README.md': source / 'README.md',
                  'teleport/CHANGELOG.md': source / 'CHANGELOG.md',
                  'teleport/LICENSE-community': source / 'build.assets/LICENSE-community'})
    for name, path in files.items():
        if not path.is_file():
            raise FileNotFoundError(f'{name}: {path}')
    for name in BINARIES:
        checksum = hashlib.sha256()
        with (artifacts / 'bin' / name).open('rb') as stream:
            for block in iter(lambda: stream.read(1024 * 1024), b''):
                checksum.update(block)
        if checksum.hexdigest() != manifest.get('binaries', {}).get(name, {}).get('sha256'):
            raise ValueError(f'Binary checksum mismatch: {name}')
    output.mkdir(parents=True, exist_ok=True)
    name = f'teleport-v{version}-linux-amd64-bin.tar.gz'
    archive = output / name
    checksum_file = output / (name + '.sha256')
    if archive.exists() or checksum_file.exists():
        raise FileExistsError(f'Refusing to replace immutable artifact {name}')
    with tempfile.TemporaryDirectory(dir=output) as temp:
        temp_archive = Path(temp) / name
        with tarfile.open(temp_archive, 'w:gz') as tar:
            for name_in_archive, path in sorted(files.items()):
                tar.add(path, arcname=name_in_archive, recursive=False)
            data = f'v{version}\n'.encode()
            import io
            info = tarfile.TarInfo('teleport/VERSION')
            info.size = len(data)
            info.mode = 0o644
            tar.addfile(info, io.BytesIO(data))
        digest = hashlib.sha256()
        with temp_archive.open('rb') as stream:
            for block in iter(lambda: stream.read(1024 * 1024), b''):
                digest.update(block)
        checksum_file.write_text(f'{digest.hexdigest()}  {name}\n')
        temp_archive.rename(archive)
    return archive


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--source', required=True, type=Path)
    parser.add_argument('--artifacts', required=True, type=Path)
    parser.add_argument('--output', required=True, type=Path)
    parser.add_argument('--version', required=True)
    args = parser.parse_args()
    print(package(args.source, args.artifacts, args.output, args.version))


if __name__ == '__main__':
    main()
