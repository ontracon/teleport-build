"""Copy two verified, versioned updater archives into the local mirror."""

import argparse
import hashlib
import json
from pathlib import Path
import shutil
import tarfile
import tempfile

ROOT = Path(__file__).resolve().parent
MIRROR = ROOT / 'generated/mirror'


def publish(artifacts):
    manifest = json.loads((artifacts / 'manifest.json').read_text())
    version = manifest['teleport_version']
    if not (artifacts / 'test-results/PASSED').is_file():
        raise ValueError(f'{artifacts} has not passed verification')
    name = f'teleport-v{version}-linux-amd64-bin.tar.gz'
    source = artifacts / 'mirror' / name
    checksum = source.with_name(name + '.sha256')
    digest, checksum_name = checksum.read_text().split()
    hasher = hashlib.sha256()
    with source.open('rb') as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b''):
            hasher.update(chunk)
    if checksum_name != name or hasher.hexdigest() != digest:
        raise ValueError(f'Checksum failed: {source}')
    with tarfile.open(source) as archive:
        binary = archive.extractfile('teleport/teleport')
        binary_hash = hashlib.sha256()
        for chunk in iter(lambda: binary.read(1024 * 1024), b''):
            binary_hash.update(chunk)
    if binary_hash.hexdigest() != manifest['binaries']['teleport']['sha256']:
        raise ValueError(f'Archive binary does not match manifest: {source}')
    MIRROR.mkdir(parents=True, exist_ok=True)
    target = MIRROR / name
    target_checksum = MIRROR / checksum.name
    if target.exists() or target_checksum.exists():
        if target.is_file() and target_checksum.is_file():
            existing = hashlib.sha256()
            with target.open('rb') as stream:
                for chunk in iter(lambda: stream.read(1024 * 1024), b''):
                    existing.update(chunk)
            if existing.hexdigest() == digest and target_checksum.read_text() == checksum.read_text():
                print(f'Already published: {version}')
                return
        raise FileExistsError(f'Refusing to replace an existing mirror asset: {target}')
    with tempfile.TemporaryDirectory(dir=MIRROR) as temporary:
        staged = Path(temporary) / name
        shutil.copyfile(source, staged)
        staged.chmod(0o644)
        temp_checksum = Path(temporary) / checksum.name
        shutil.copyfile(checksum, temp_checksum)
        temp_checksum.chmod(0o644)
        staged.rename(target)
        temp_checksum.rename(target_checksum)
    print(f'Published {version}: {target}')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('artifacts', nargs='+', type=Path)
    args = parser.parse_args()
    for item in args.artifacts:
        publish(item)


if __name__ == '__main__':
    main()
