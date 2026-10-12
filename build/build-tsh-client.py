"""Build one native tsh client with CGO, Rust RDP decoding, and PIV support."""

import os
from pathlib import Path
import shutil
import subprocess
import sys


def run(*command):
    subprocess.run(command, check=True)


def output(*command):
    return subprocess.check_output(command, text=True).strip()


def main():
    script_dir = Path(__file__).resolve().parent
    os.chdir(os.environ['SOURCE_DIR'])
    target_os = os.environ['TARGET_OS']
    target_arch = os.environ['TARGET_ARCH']
    commit = os.environ['TELEPORT_COMMIT']
    if output('git', 'rev-parse', 'HEAD') != commit:
        raise RuntimeError('Client source does not match the resolved upstream tag')
    if output('go', 'env', 'GOOS') != target_os or output('go', 'env', 'GOARCH') != target_arch:
        raise RuntimeError('Client runner does not match the requested native target')
    jobs = os.environ.get('BUILD_JOBS', '2')
    if not jobs.isdecimal() or int(jobs) < 1:
        raise RuntimeError('BUILD_JOBS must be a positive integer')
    os.environ.update(GOMAXPROCS=jobs, GOFLAGS=f'-p={jobs}', CARGO_BUILD_JOBS=jobs, CGO_ENABLED='1')
    # Restore the selected tag locally when checkout fetched the resolved SHA.
    run('git', 'update-ref', f"refs/tags/{os.environ['TELEPORT_TAG']}", commit)
    run('make', '--no-print-directory', 'version', f'OS={target_os}', f'ARCH={target_arch}', 'FIDO2=off')
    directory = Path(os.environ['CLIENT_OUT_DIR']) / f'{target_os}-{target_arch}'
    (directory / 'bin').mkdir(parents=True, exist_ok=True)

    if target_os == 'windows':
        # Match upstream's Windows GNU Rust target and native tsh build tags.
        run('cargo', 'build', '-p', 'rdp-decoder', '--release', '--locked')
        binary = directory / 'bin/tsh.exe'
        kubectl_version = output('go', 'list', '-m', '-f', '{{.Version}}', 'k8s.io/kubectl').replace('v0.', 'v1.', 1)
        run('go', 'build', '-tags', 'piv,rust_rdp_decoder,kustomize_disable_go_plugin_support',
            '-trimpath', '-buildvcs=false', '-ldflags',
            f'-w -s -X k8s.io/component-base/version.gitVersion={kubectl_version} -extldflags=-static',
            '-o', str(binary), './tool/tsh')
    elif target_os == 'darwin':
        # Upstream supplies the macOS deployment target, CGO and Rust link flags.
        run('make', '--no-print-directory', 'build/tsh', f'OS={target_os}', f'ARCH={target_arch}',
            'FIDO2=off', 'PIV=dynamic', 'TOUCHID=yes', 'RDPCLIENT_SKIP_BUILD=0')
        binary = directory / 'bin/tsh'
        shutil.copy2('build/tsh', binary)
    else:
        raise RuntimeError(f'Unsupported native client OS: {target_os}')
    binary.chmod(0o755)
    run(sys.executable, str(script_dir / 'verify-tsh-clients.py'))


if __name__ == '__main__':
    main()
