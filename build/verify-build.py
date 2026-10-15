"""Verify the compiled artifacts and record their source/toolchain metadata."""

import hashlib
import json
import os
from pathlib import Path
import re
import subprocess


def output(*command):
    return subprocess.check_output(command, text=True).strip()


def main():
    root = Path(os.environ.get('OUT_DIR', '/out'))
    binaries = root / 'bin'
    version = os.environ.get('TELEPORT_VERSION', '18.10.0')
    commit = os.environ.get('TELEPORT_COMMIT', 'ddaa46b8f4ee579d43480cd2d3b6a14b18e3ef7d')
    if output('git', 'rev-parse', 'HEAD') != commit:
        raise RuntimeError(f'The checked out source does not match the selected v{version} commit')

    metadata = {}
    checksums = []
    for binary in sorted(binaries.iterdir()):
        with binary.open('rb') as stream:
            if stream.read(4) != b'\x7fELF':
                raise RuntimeError(f'{binary.name} is not a Linux ELF executable')
            stream.seek(0)
            checksum = hashlib.sha256()
            for block in iter(lambda: stream.read(1024 * 1024), b''):
                checksum.update(block)
        digest = checksum.hexdigest()
        checksums.append(f'{digest}  bin/{binary.name}')
        metadata[binary.name] = {'sha256': digest}
        if binary.name in ('teleport', 'tctl', 'tsh', 'tbot', 'teleport-update'):
            reported = output(str(binary), 'version')
            if not re.search(r'(?<![0-9A-Za-z.+-])v?' + re.escape(version) + r'(?![0-9A-Za-z.+-])', reported):
                raise RuntimeError(f'{binary.name} reports the wrong version: {reported}')
            build_info = output('go', 'version', '-m', str(binary))
            if 'libfido2' in build_info:
                raise RuntimeError(f'{binary.name} was built with libfido2')
            metadata[binary.name].update(version=reported, go_build_info=build_info)

    # Verify the features that make this a full build, including native Rust RDP.
    teleport_info = metadata['teleport']['go_build_info']
    for tag in ('webassets_embed', 'desktop_access_rdp', 'sessionhelper_embed', 'pam', 'bpf'):
        if tag not in teleport_info:
            raise RuntimeError(f'Teleport build is missing {tag}')
    if 'rust_rdp_decoder' not in metadata['tsh']['go_build_info']:
        raise RuntimeError('tsh build is missing the Rust RDP decoder')
    if not Path('webassets/teleport/index.html').is_file():
        raise RuntimeError('Web UI entry point was not built')

    manifest = {
        'teleport_version': version,
        'source_commit': commit,
        'os': output('go', 'env', 'GOOS'),
        'architecture': output('go', 'env', 'GOARCH'),
        'fido2': 'off',
        'toolchains': {name: output(*command) for name, command in {
            'go': ('go', 'version'),
            'rust': ('rustc', '--version'),
            'node': ('node', '--version'),
            'pnpm': ('pnpm', '--version'),
        }.items()},
        'binaries': metadata,
    }
    integration_dir = os.environ.get('TELEPORT_INTEGRATION_DIR')
    if integration_dir:
        integration = Path(integration_dir)
        files = {}
        for file in sorted(integration.rglob('*')):
            if file.is_file():
                files['integration/' + str(file.relative_to(integration))] = hashlib.sha256(file.read_bytes()).hexdigest()
        for file in sorted(Path('lib/zitadeloidc').glob('*')):
            if file.is_file():
                files[str(file)] = hashlib.sha256(file.read_bytes()).hexdigest()
        manifest['integration'] = {'provider': 'zitadel', 'files': files}
    (root / 'manifest.json').write_text(json.dumps(manifest, indent=2) + '\n')
    (root / 'SHA256SUMS').write_text('\n'.join(checksums) + '\n')
    print(json.dumps(manifest, indent=2))


if __name__ == '__main__':
    main()
