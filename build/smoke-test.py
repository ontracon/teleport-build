"""Start an isolated Auth/Proxy and verify the API, Web UI, and admin CLI."""

from html.parser import HTMLParser
import json
import os
from pathlib import Path
import subprocess
import tempfile
import time
from urllib.parse import urljoin, urlparse


class Scripts(HTMLParser):
    def __init__(self):
        super().__init__()
        self.sources = []

    def handle_starttag(self, tag, attrs):
        if tag == 'script':
            src = dict(attrs).get('src')
            if src:
                self.sources.append(src)


def fetch(url, javascript=False):
    # The isolated test server generates its own TLS certificate.
    with tempfile.NamedTemporaryFile() as headers:
        command = [
            'curl', '--insecure', '--fail', '--silent', '--show-error',
            '--location', '--connect-timeout', '2', '--max-time', '5',
            '--dump-header', headers.name,
        ]
        if javascript:
            command += ['--header', 'Accept-Encoding: br']
        body = subprocess.check_output(command + [url], stderr=subprocess.PIPE)
        block = headers.read().decode().replace('\r\n', '\n').strip().split('\n\n')[-1]
        fields = {}
        for line in block.splitlines():
            if ':' in line:
                name, value = line.split(':', 1)
                fields[name.lower()] = value.strip().lower()
        if javascript and not any(kind in fields.get('content-type', '')
                                  for kind in ('javascript', 'ecmascript')):
            raise RuntimeError(f'Web UI asset has the wrong content type: {url}')
        if fields.get('content-encoding') == 'br':
            # AlmaLinux's curl-minimal has no Brotli decoder. Node is already
            # part of the builder and provides one without extra dependencies.
            body = subprocess.check_output([
                'node', '-e',
                "process.stdout.write(require('node:zlib').brotliDecompressSync(require('node:fs').readFileSync(0)))",
            ], input=body)
        return body


def main():
    binaries = Path(os.environ.get('OUT_DIR', '/out')) / 'bin'
    version = os.environ.get('TELEPORT_VERSION', '18.10.0')
    with tempfile.TemporaryDirectory(prefix='teleport-build-smoke-') as directory:
        root = Path(directory)
        config = root / 'teleport.yaml'
        config.write_text(f'''version: v3
teleport:
  nodename: build-smoke
  data_dir: {root / 'data'}
  log:
    output: stderr
    severity: INFO
auth_service:
  enabled: true
  cluster_name: build-smoke.local
  listen_addr: 127.0.0.1:3025
  authentication:
    type: local
    second_factor: otp
proxy_service:
  enabled: true
  web_listen_addr: 127.0.0.1:3080
  public_addr: 127.0.0.1:3080
  listen_addr: 127.0.0.1:3023
  tunnel_listen_addr: 127.0.0.1:3024
ssh_service:
  enabled: false
''')
        with (root / 'server.log').open('w+') as log:
            server = subprocess.Popen([
                str(binaries / 'teleport'), 'start', '--config', str(config),
            ], stdout=log, stderr=subprocess.STDOUT)
            try:
                base = 'https://127.0.0.1:3080'
                deadline = time.monotonic() + 120
                while True:
                    if server.poll() is not None:
                        raise RuntimeError('Teleport exited before becoming ready')
                    try:
                        ping = json.loads(fetch(base + '/webapi/ping'))
                        break
                    except (subprocess.CalledProcessError, json.JSONDecodeError):
                        if time.monotonic() >= deadline:
                            raise RuntimeError('Teleport did not become ready in 120 seconds')
                        time.sleep(1)
                if ping.get('server_version') != version:
                    raise RuntimeError(f'Wrong Proxy API version: {ping}')

                page = fetch(base + '/web/login').decode()
                scripts = Scripts()
                scripts.feed(page)
                assets = [urljoin(base + '/web/', src) for src in scripts.sources]
                assets = [url for url in assets if urlparse(url).netloc == '127.0.0.1:3080']
                if not assets:
                    raise RuntimeError('Web UI contains no local JavaScript assets')
                for url in assets:
                    javascript = fetch(url, javascript=True)
                    if not javascript or javascript.lstrip().lower().startswith(b'<!doctype'):
                        raise RuntimeError(f'Web UI asset is missing: {url}')

                subprocess.run([
                    str(binaries / 'tctl'), '--config', str(config), 'status',
                ], check=True, timeout=30)
                print('PASS: Auth/Proxy startup, version API, embedded Web UI assets, and tctl status')
            except BaseException:
                log.flush()
                log.seek(0)
                print(log.read(), flush=True)
                raise
            finally:
                if server.poll() is None:
                    server.terminate()
                    try:
                        server.wait(timeout=15)
                    except subprocess.TimeoutExpired:
                        server.kill()
                        server.wait()


if __name__ == '__main__':
    main()
