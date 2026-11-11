"""Validate and publish a numbered release's mirror assets to Azure Blob Storage."""

import argparse
import hashlib
import os
from pathlib import Path
import re
import subprocess
import tempfile


TAG = re.compile(r'v(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)-(0|[1-9]\d*)\.(0|[1-9]\d*)')


def digest(path):
    value = hashlib.sha256()
    with path.open('rb') as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b''):
            value.update(block)
    return value.hexdigest()


def verify(directory, tag):
    if not TAG.fullmatch(tag):
        raise ValueError('Expected a numeric release tag such as v18.10.0-42.1')
    version = tag[1:]
    names = [f'teleport-v{version}-linux-amd64-bin.tar.gz',
             f'teleport-v{version}-windows-amd64-bin.zip',
             f'teleport-v{version}-darwin-amd64-bin.tar.gz',
             f'teleport-v{version}-darwin-arm64-bin.tar.gz',
             f'teleport-tools-{version}.pkg']
    assets = []
    for name in names:
        archive = directory / name
        checksum = directory / f'{name}.sha256'
        fields = checksum.read_text(encoding='ascii').split()
        if len(fields) != 2 or not re.fullmatch(r'[0-9a-f]{64}', fields[0]) or fields[1] != name:
            raise ValueError(f'Invalid checksum file: {checksum.name}')
        if digest(archive) != fields[0]:
            raise ValueError(f'Checksum mismatch: {name}')
        assets.extend((archive, checksum))
    return assets


def publish(assets, account, container, auth_mode):
    if not re.fullmatch(r'[a-z0-9]{3,24}', account):
        raise ValueError('Invalid Azure storage account name')
    if not re.fullmatch(r'[a-z0-9](?:[a-z0-9-]{1,61}[a-z0-9])', container) or '--' in container:
        raise ValueError('Invalid Azure storage container name')
    storage_key = os.environ.get('AZURE_STORAGE_KEY', '')
    if auth_mode == 'key' and not storage_key:
        raise ValueError('AZURE_STORAGE_KEY is required for key authentication')
    common = ['--account-name', account, '--container-name', container,
              '--auth-mode', auth_mode, '--only-show-errors', '--output', 'none']
    for asset in assets:
        print(f'Publishing {asset.name}', flush=True)
        result = subprocess.run(['az', 'storage', 'blob', 'upload', *common,
                                 '--name', asset.name, '--file', str(asset),
                                 '--overwrite', 'false'], capture_output=True, text=True)
        if result.returncode == 0:
            continue
        # A previous attempt may have uploaded this file before it was interrupted.
        # Compare the actual bytes and never replace a different existing blob.
        with tempfile.TemporaryDirectory(prefix='teleport-azure-') as tmp:
            remote = Path(tmp) / asset.name
            downloaded = subprocess.run(['az', 'storage', 'blob', 'download', *common,
                                         '--name', asset.name, '--file', str(remote)],
                                        capture_output=True, text=True)
            if downloaded.returncode == 0 and remote.is_file() and digest(remote) == digest(asset):
                print(f'Already published with identical content: {asset.name}', flush=True)
                continue
        error = result.stderr.strip()
        if storage_key:
            error = error.replace(storage_key, '[redacted]')
        raise RuntimeError(f'Could not upload {asset.name} without overwriting. '
                           f'Azure upload error: {error}')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('action', choices=('verify', 'upload'))
    parser.add_argument('--directory', type=Path, required=True)
    parser.add_argument('--tag', required=True)
    parser.add_argument('--account', default='')
    parser.add_argument('--container', default='')
    parser.add_argument('--auth-mode', choices=('login', 'key'), default='login')
    args = parser.parse_args()
    assets = verify(args.directory, args.tag)
    if args.action == 'upload':
        publish(assets, args.account, args.container, args.auth_mode)
    print(f'Validated {len(assets)} mirror files for {args.tag}', flush=True)


if __name__ == '__main__':
    main()
