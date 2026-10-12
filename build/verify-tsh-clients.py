"""Check and run a native tsh client, then package its release asset."""

import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import struct
import subprocess
import tarfile
import zipfile


def output(*command):
    return subprocess.check_output(command, text=True).strip()


def sha256(path):
    digest = hashlib.sha256()
    with path.open('rb') as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b''):
            digest.update(block)
    return digest.hexdigest()


def check_header(binary, target_os, target_arch):
    with binary.open('rb') as stream:
        header = stream.read(64)
        if target_os == 'windows':
            if len(header) < 64 or header[:2] != b'MZ':
                raise RuntimeError(f'{binary} is not a Windows executable')
            stream.seek(struct.unpack_from('<I', header, 60)[0])
            pe_header = stream.read(6)
            if pe_header != b'PE\0\0\x64\x86':
                raise RuntimeError(f'{binary} is not Windows amd64 PE')
        else:
            cpu = {'amd64': 0x01000007, 'arm64': 0x0100000c}[target_arch]
            if len(header) < 8 or header[:4] != b'\xcf\xfa\xed\xfe' or struct.unpack_from('<I', header, 4)[0] != cpu:
                raise RuntimeError(f'{binary} is not darwin/{target_arch} Mach-O')


def main():
    root = Path(os.environ.get('CLIENT_OUT_DIR', '/out/tsh-clients'))
    version = os.environ['TELEPORT_VERSION']
    commit = os.environ['TELEPORT_COMMIT']
    if not re.fullmatch(r'[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z]+(?:[.-][0-9A-Za-z]+)*)?(?:\+[0-9A-Za-z]+(?:[.-][0-9A-Za-z]+)*)?', version):
        raise RuntimeError('Invalid release version')
    if output('git', 'rev-parse', 'HEAD') != commit:
        raise RuntimeError('Client source does not match the selected commit')

    target_os = os.environ['TARGET_OS']
    target_arch = os.environ['TARGET_ARCH']
    if (target_os, target_arch) not in (('windows', 'amd64'), ('darwin', 'arm64'), ('darwin', 'amd64')):
        raise RuntimeError('Unsupported client target')
    directory = root / f'{target_os}-{target_arch}'
    name = 'tsh.exe' if target_os == 'windows' else 'tsh'
    binary = directory / 'bin' / name
    check_header(binary, target_os, target_arch)
    build_info = output('go', 'version', '-m', str(binary))
    settings = dict(re.findall(r'^\s*build\s+([^=\s]+)=(.*)$', build_info, re.MULTILINE))
    for key, expected in (('GOOS', target_os), ('GOARCH', target_arch), ('CGO_ENABLED', '1')):
        if settings.get(key) != expected:
            raise RuntimeError(f'{binary}: expected {key}={expected}')
    tags = set(re.split(r'[ ,]+', settings.get('-tags', '').strip('"')))
    required_tags = {'rust_rdp_decoder', 'piv'}
    if target_os == 'darwin':
        required_tags.add('touchid')
    if not required_tags.issubset(tags):
        raise RuntimeError(f'{binary} is missing client features: {required_tags - tags}')
    if tags.intersection(('libfido2', 'libfido2static')):
        raise RuntimeError(f'{binary} unexpectedly includes FIDO2')
    reported = output(str(binary), 'version')
    if not re.search(r'(?<![0-9A-Za-z.+-])v?' + re.escape(version) + r'(?![0-9A-Za-z.+-])', reported):
        raise RuntimeError(f'{binary} reports the wrong version: {reported}')
    # Execute on the target host: missing shared libraries fail these checks.
    subprocess.run([str(binary), '--help'], check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)

    checksum = sha256(binary)
    (directory / 'SHA256SUMS').write_text(f'{checksum}  bin/{name}\n')
    shutil.copyfile('LICENSE', directory / 'LICENSE')
    manifest = {
        'teleport_version': version,
        'source_commit': commit,
        'os': target_os,
        'architecture': target_arch,
        'cgo_enabled': True,
        'fido2': 'off',
        'rust_rdp_decoder': 'on',
        'touchid': 'on' if target_os == 'darwin' else 'not-applicable',
        'piv': 'on',
        'vnetdaemon': 'off',
        'runtime_checks': ['version', '--help'],
        'distribution_signing': 'none',
        'notarized': False,
        'toolchains': {'go': output('go', 'version'), 'rust': output('rustc', '--version')},
        'binaries': {name: {'sha256': checksum, 'go_build_info': build_info, 'version': reported}},
    }
    (directory / 'manifest.json').write_text(json.dumps(manifest, indent=2) + '\n')

    basename = f'tsh-{version}-{target_os}-{target_arch}'
    if target_os == 'windows':
        archive = root.parent / f'{basename}.zip'
        with zipfile.ZipFile(archive, 'w', zipfile.ZIP_DEFLATED) as package:
            for relative in (f'bin/{name}', 'LICENSE', 'SHA256SUMS', 'manifest.json'):
                package.write(directory / relative, relative)
    else:
        archive = root.parent / f'{basename}.tar.gz'
        with tarfile.open(archive, 'w:gz') as package:
            for relative in ('bin', 'LICENSE', 'SHA256SUMS', 'manifest.json'):
                package.add(directory / relative, arcname=relative)
    archive.with_name(archive.name + '.sha256').write_text(f'{sha256(archive)}  {archive.name}\n')
    print(f'PASS: native {target_os}/{target_arch} headers, features, version/help; packaged {archive.name}')


if __name__ == '__main__':
    main()
