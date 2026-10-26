"""Exercise the production image's launch contract, not the development lab.

Requires Docker, Helm, and Python's standard library. Validates configurations
rendered by the matching official chart, then starts an isolated Auth/Proxy
using the image's default entrypoint and chart-style flag-only arguments.
This is a container smoke test, not a Kubernetes/ingress acceptance deployment.
"""

import argparse
from http.client import HTTPException
import json
from pathlib import Path
import re
import ssl
import subprocess
import tempfile
import time
from urllib.request import urlopen
import uuid


ENTRYPOINT = ['/usr/bin/dumb-init', '/usr/local/bin/teleport', 'start',
              '-c', '/etc/teleport/teleport.yaml']


def run(*args, **kwargs):
    return subprocess.check_output(args, text=True, timeout=60, **kwargs).strip()


def check_image(image):
    config = json.loads(run('docker', 'image', 'inspect', image))[0]['Config']
    if config['Entrypoint'] != ENTRYPOINT or config.get('Cmd'):
        raise RuntimeError(f'Image does not implement the official entrypoint contract: {config}')
    if config.get('StopSignal') != 'SIGQUIT':
        raise RuntimeError('Image must use SIGQUIT for graceful shutdown')
    if 'TELEPORT_TOOLS_VERSION=off' not in config.get('Env', []):
        raise RuntimeError('Image must disable automatic tool downloads')


def configmap_block(document, key):
    # Read only Helm's known, explicitly indented ConfigMap literal format.
    # Fail closed if the chart changes that format; this is not a YAML parser.
    match = re.search(r'^  ' + re.escape(key) + r': \|2?\n((?:    .*\n|\n)+)',
                      document + '\n', re.MULTILINE)
    if not match:
        raise RuntimeError(f'Chart did not render a literal ConfigMap entry for {key}')
    return ''.join(line[4:] if line.startswith('    ') else line
                   for line in match[1].splitlines(keepends=True))


def check_chart(image, version, root, chart=None):
    if chart is None:
        destination = root / 'chart'
        run('helm', 'pull', 'teleport-cluster', '--repo',
            'https://charts.releases.teleport.dev', '--version', version,
            '--untar', '--untardir', str(destination))
        chart = destination / 'teleport-cluster'
    repository = 'ghcr.io/ontracon/teleport-build'
    tag = version.replace('+', '-') + '-build.0.0'
    command = ['helm', 'template', 'runtime-smoke', str(chart), '--namespace', 'runtime-smoke',
               '--set', 'clusterName=runtime-smoke.example', '--set', 'enterprise=false',
               '--set', 'proxyListenerMode=multiplex', '--set', 'proxyProtocol=off',
               '--set', f'image={repository}', '--set', f'teleportVersionOverride={tag}']
    rendered = run(*command)
    images = re.findall(r'^\s+image: [\'\"]?([^\s\'\"]+)', rendered, re.MULTILINE)
    if len(images) != 5 or set(images) != {f'{repository}:{tag}'}:
        raise RuntimeError(f'Auth, Proxy, init, and validation jobs must use our image: {images}')
    for role in ('auth', 'proxy'):
        document = run(*command, '--show-only', f'templates/{role}/predeploy_config.yaml')
        config_dir = root / role
        config_dir.mkdir()
        (config_dir / 'teleport.yaml').write_text(configmap_block(document, 'teleport.yaml'))
        run('docker', 'run', '--rm', '--entrypoint', 'teleport',
            '--mount', f'type=bind,source={config_dir},target=/etc/teleport,readonly',
            image, 'configure', '--test', '/etc/teleport/teleport.yaml')
    auth = run(*command, '--show-only', 'templates/auth/config.yaml')
    print('PASS: official chart image selection and Auth/Proxy configuration validation', flush=True)
    return configmap_block(auth, 'apply-on-startup.yaml')


def fetch(url):
    context = ssl._create_unverified_context() if url.startswith('https:') else None
    with urlopen(url, context=context, timeout=3) as response:
        return response.read()


def wait_ready(name, diag, proxy, version, timeout=120):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        state = json.loads(run('docker', 'inspect', name))[0]['State']
        if not state['Running']:
            raise RuntimeError(f'Teleport exited before readiness: {state}')
        try:
            fetch(diag + '/healthz')
            fetch(diag + '/readyz')
            ping = json.loads(fetch(proxy + '/webapi/ping'))
        except (OSError, HTTPException, json.JSONDecodeError):
            time.sleep(1)
            continue
        if ping.get('server_version') != version:
            raise RuntimeError(f'Wrong server version: {ping}')
        return
    raise RuntimeError(f'Teleport was not ready within {timeout} seconds')


def check_server(image, version, root, startup):
    config_dir = root / 'server'
    config_dir.mkdir()
    (config_dir / 'teleport.yaml').write_text('''version: v3
teleport:
  nodename: runtime-smoke
  data_dir: /var/lib/teleport
auth_service:
  enabled: true
  cluster_name: runtime-smoke.example
  listen_addr: 127.0.0.1:3025
  proxy_listener_mode: multiplex
  authentication:
    type: local
    second_factor: otp
proxy_service:
  enabled: true
  web_listen_addr: 0.0.0.0:3080
  public_addr: runtime-smoke.example:443
ssh_service:
  enabled: false
''')
    (config_dir / 'apply-on-startup.yaml').write_text(startup)
    name = 'teleport-runtime-smoke-' + uuid.uuid4().hex[:12]
    try:
        # No command override: like the chart, pass only flags to ENTRYPOINT.
        run('docker', 'run', '--detach', '--name', name,
            '--publish', '127.0.0.1::3000', '--publish', '127.0.0.1::3080',
            '--mount', f'type=bind,source={config_dir},target=/etc/teleport,readonly',
            '--tmpfs', '/var/lib/teleport', image,
            '--diag-addr=0.0.0.0:3000',
            '--apply-on-startup=/etc/teleport/apply-on-startup.yaml')
        container = json.loads(run('docker', 'inspect', name))[0]
        ports = container['NetworkSettings']['Ports']
        diag = 'http://127.0.0.1:' + ports['3000/tcp'][0]['HostPort']
        proxy = 'https://127.0.0.1:' + ports['3080/tcp'][0]['HostPort']
        wait_ready(name, diag, proxy, version)
        if b'<html' not in fetch(proxy + '/web/login').lower():
            raise RuntimeError('Embedded Web UI did not return HTML')
        run('docker', 'exec', name, 'tctl', '--config=/etc/teleport/teleport.yaml', 'status')
        run('docker', 'exec', name, 'teleport',
            'wait', 'no-resolve', uuid.uuid4().hex + '.invalid')
        run('docker', 'exec', name, 'teleport', 'wait', 'duration', '1ms')
        run('docker', 'stop', '--time', '45', name)
        state = json.loads(run('docker', 'inspect', name))[0]['State']
        if state['ExitCode'] != 0 or state.get('OOMKilled'):
            raise RuntimeError(f'Teleport did not shut down cleanly: {state}')
        print('PASS: flag-only startup, probes, Web UI, tctl, wait commands, and SIGQUIT shutdown',
              flush=True)
    except BaseException:
        subprocess.run(['docker', 'logs', name], check=False, timeout=15)
        raise
    finally:
        subprocess.run(['docker', 'rm', '--force', name], check=False, timeout=15,
                       stdout=subprocess.DEVNULL)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--image', default='teleport-zitadel:18.10.0')
    parser.add_argument('--version', default='18.10.0')
    parser.add_argument('--chart', type=Path, help='Use an already downloaded chart directory')
    args = parser.parse_args()
    check_image(args.image)
    with tempfile.TemporaryDirectory(prefix='teleport-runtime-smoke-') as directory:
        root = Path(directory)
        startup = check_chart(args.image, args.version, root, args.chart)
        check_server(args.image, args.version, root, startup)


if __name__ == '__main__':
    main()
