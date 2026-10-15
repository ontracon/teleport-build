"""Verify Linux release artifacts against the current integration sources."""

import hashlib
import json
import os
from pathlib import Path
import sys


BINARIES = {'teleport', 'tctl', 'tsh', 'tbot', 'fdpass-teleport', 'teleport-update'}


def digest(path):
    checksum = hashlib.sha256()
    with path.open('rb') as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b''):
            checksum.update(block)
    return checksum.hexdigest()


def verify(root, repo, version, commit):
    manifest = json.loads((root / 'manifest.json').read_text())
    if (manifest['os'], manifest['architecture']) != ('linux', 'amd64'):
        raise ValueError('Expected a Linux amd64 release')
    if manifest['teleport_version'] != version or manifest['source_commit'] != commit:
        raise ValueError('Release version or source commit does not match the requested tag')
    if (repo / 'integration/upstream.commit').read_text().strip() != commit:
        raise ValueError('Requested tag does not match integration/upstream.commit')

    integration = manifest.get('integration', {})
    if integration.get('provider') != 'zitadel':
        raise ValueError('Release is missing the Zitadel integration')
    sources = [path for path in (repo / 'integration').rglob('*')
               if path.is_file() and '__pycache__' not in path.parts and path.suffix != '.pyc']
    expected = {str(path.relative_to(repo)): digest(path) for path in sources}
    adapter = repo / 'oidc-adapter'
    for path in [*adapter.glob('*.go'), adapter / 'config.example.json']:
        expected['lib/zitadeloidc/' + path.name] = digest(path)
    if integration.get('files') != expected:
        raise ValueError('Release integration hashes do not match the current adapter and patches')

    if set(manifest['binaries']) != BINARIES:
        raise ValueError('Release must contain all six binaries')
    for name, metadata in manifest['binaries'].items():
        if digest(root / 'bin' / name) != metadata['sha256']:
            raise ValueError(f'Release binary checksum mismatch: {name}')
    if not (root / 'test-results/PASSED').is_file():
        raise ValueError('Release is missing passing test results')
    return version


if __name__ == '__main__':
    print(verify(Path(sys.argv[1]), Path(__file__).resolve().parent.parent,
                 os.environ['TELEPORT_VERSION'], os.environ['TELEPORT_COMMIT']))
