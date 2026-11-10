"""Package verified native clients in Teleport's client-updater CDN layout."""

import argparse
import hashlib
import io
import json
import os
from pathlib import Path
import re
import subprocess
import tarfile
import tempfile
import zipfile


def digest(path):
    h = hashlib.sha256()
    with path.open('rb') as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b''):
            h.update(block)
    return h.hexdigest()


def verified(directory, version, os_name, arch, commit):
    manifest = json.loads((directory / 'mirror-client.json').read_text())
    if (manifest['teleport_version'], manifest['source_commit'], manifest['os'],
            manifest['architecture']) != (version, commit, os_name, arch):
        raise ValueError(f'{directory}: client manifest does not match the requested release')
    names = (('tsh.exe', 'tctl.exe') if os_name == 'windows' else
             ('tsh', 'tctl', 'teleport', 'tbot', 'fdpass-teleport'))
    if set(manifest['binaries']) != set(names):
        raise ValueError(f'{directory}: manifest does not contain the required platform binaries')
    paths = {name: directory / 'bin' / name for name in names}
    for name, path in paths.items():
        if digest(path) != manifest['binaries'][name]:
            raise ValueError(f'{path}: client checksum mismatch')
    return paths


def package(version, commit, output, windows=None, amd64=None, arm64=None, darwin_host=None, arch=None):
    if not re.fullmatch(r'(?:0|[1-9]\d*)\.(?:0|[1-9]\d*)\.(?:0|[1-9]\d*)-(?:0|[1-9]\d*)\.(?:0|[1-9]\d*)', version):
        raise ValueError('Expected a numeric-suffix distribution version')
    if not re.fullmatch(r'[0-9a-f]{40}', commit):
        raise ValueError('Expected a full source commit')
    if windows is not None and amd64 is None and arm64 is None and darwin_host is None:
        inputs = verified(windows, version, 'windows', 'amd64', commit)
        name = f'teleport-v{version}-windows-amd64-bin.zip'
    elif windows is None and amd64 is not None and arm64 is not None and darwin_host is None:
        inputs = [verified(amd64, version, 'darwin', 'amd64', commit),
                  verified(arm64, version, 'darwin', 'arm64', commit)]
        name = f'teleport-tools-{version}.pkg'
    elif windows is None and amd64 is None and arm64 is None and darwin_host is not None and arch in ('amd64', 'arm64'):
        inputs = verified(darwin_host, version, 'darwin', arch, commit)
        name = f'teleport-v{version}-darwin-{arch}-bin.tar.gz'
    else:
        raise ValueError('Provide Windows amd64, one macOS host architecture, or both macOS architectures')
    output.mkdir(parents=True, exist_ok=True)
    target = output / name
    checksum = output / (name + '.sha256')
    if target.exists() or checksum.exists():
        raise FileExistsError(f'Refusing to replace {name}')
    with tempfile.TemporaryDirectory(dir=output) as temp:
        temp = Path(temp)
        archive = temp / name
        if windows is not None:
            with zipfile.ZipFile(archive, 'w', zipfile.ZIP_DEFLATED) as package_file:
                for binary_name, path in inputs.items():
                    package_file.write(path, f'teleport/{binary_name}')
        elif darwin_host is not None:
            with tarfile.open(archive, 'w:gz') as package_file:
                for binary_name, path in inputs.items():
                    package_file.add(path, arcname=f'teleport/{binary_name}')
                package_file.add(darwin_host / 'LICENSE', arcname='teleport/LICENSE')
                version_data = f'v{version}\n'.encode()
                info = tarfile.TarInfo('teleport/VERSION')
                info.size = len(version_data)
                package_file.addfile(info, io.BytesIO(version_data))
        else:
            root = temp / 'root' / 'usr' / 'local' / 'bin'
            root.mkdir(parents=True)
            for binary_name in ('tsh', 'tctl'):
                universal = root / binary_name
                subprocess.run(['lipo', '-create', str(inputs[0][binary_name]), str(inputs[1][binary_name]),
                                '-output', str(universal)], check=True)
                universal.chmod(0o755)
                subprocess.run(['lipo', str(universal), '-verify_arch', 'x86_64', 'arm64'], check=True)
            subprocess.run(['pkgbuild', '--root', str(temp / 'root'), '--identifier',
                            'dev.ontracon.teleport-tools', '--version', version,
                            '--install-location', '/', str(archive)], check=True)
            expanded = temp / 'expanded'
            subprocess.run(['pkgutil', '--expand-full', str(archive), str(expanded)], check=True)
            for binary_name in ('tsh', 'tctl'):
                found = [p for p in expanded.rglob(binary_name) if p.is_file()]
                if len(found) != 1 or digest(found[0]) != digest(root / binary_name):
                    raise ValueError(f'{name}: missing or incorrect {binary_name}')
                subprocess.run([str(found[0]), 'version'], check=True, stdout=subprocess.DEVNULL,
                               env=dict(os.environ, TELEPORT_TOOLS_VERSION='off'))
        checksum.write_text(f'{digest(archive)}  {name}\n')
        archive.replace(target)
    return target


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--version', required=True)
    parser.add_argument('--commit', required=True)
    parser.add_argument('--output', required=True, type=Path)
    parser.add_argument('--windows', type=Path)
    parser.add_argument('--darwin-amd64', type=Path)
    parser.add_argument('--darwin-arm64', type=Path)
    parser.add_argument('--darwin-host', type=Path)
    parser.add_argument('--arch', choices=('amd64', 'arm64'))
    args = parser.parse_args()
    print(package(args.version, args.commit, args.output, args.windows, args.darwin_amd64,
                  args.darwin_arm64, args.darwin_host, args.arch))


if __name__ == '__main__':
    main()
