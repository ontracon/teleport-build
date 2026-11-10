"""Exercise mirror enrollment and managed updates against running lab services.

Run through `make mirror-lab-test`; the lab must contain two real build archives.
This script provisions resources in the isolated mirror-lab cluster only.
"""

import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys
import tarfile
import time

ROOT = Path(__file__).resolve().parent
COMPOSE = ['docker', 'compose', '-f', str(ROOT / 'compose.yaml')]
START = os.environ.get('LAB_START_VERSION', '18.10.0-10.1')
TARGET = os.environ.get('LAB_TARGET_VERSION', '18.10.0-11.1')
MIRROR = ROOT / 'generated/mirror'


def compose(*args, input=None, check=True, timeout=180):
    result = subprocess.run(COMPOSE + list(args), input=input, text=True,
                            capture_output=True, timeout=timeout)
    if check and result.returncode:
        raise RuntimeError(f"compose {' '.join(args[:4])} failed ({result.returncode}):\n"
                           f'{result.stdout}\n{result.stderr}')
    return result.stdout.strip()


def exec_host(service, script, *args, timeout=180):
    return compose('exec', '-T', service, 'bash', '-se', '--', *args,
                   input=script, timeout=timeout)


def wait_for(description, fn, timeout=180):
    deadline = time.monotonic() + timeout
    last = None
    while time.monotonic() < deadline:
        try:
            if fn():
                print('PASS:', description, flush=True)
                return
        except (RuntimeError, ValueError, json.JSONDecodeError) as exc:
            last = exc
        time.sleep(3)
    raise RuntimeError(f'timed out waiting for {description}: {last}')


def archive_hash(version, binary_name='teleport'):
    archive = MIRROR / f'teleport-v{version}-linux-amd64-bin.tar.gz'
    checksum = archive.with_name(archive.name + '.sha256')
    expected, name = checksum.read_text().split()
    digest = hashlib.sha256()
    with archive.open('rb') as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b''):
            digest.update(chunk)
    if name != archive.name or digest.hexdigest() != expected:
        raise RuntimeError(f'invalid archive checksum: {archive}')
    with tarfile.open(archive, 'r:gz') as tar:
        binary_digest = hashlib.sha256()
        with tar.extractfile('teleport/' + binary_name) as binary:
            for chunk in iter(lambda: binary.read(1024 * 1024), b''):
                binary_digest.update(chunk)
        return binary_digest.hexdigest()


def tctl(*args):
    return compose('exec', '-T', 'teleport', 'tctl', '--config=/lab/teleport.yaml', *args)


def new_token():
    token = tctl('tokens', 'add', '--type=node', '--ttl=1h', '--format=text')
    if len(token) != 32 or any(c not in '0123456789abcdef' for c in token):
        raise RuntimeError('unexpected node token format')
    return token


def create_bot(service, bot_name):
    output = tctl('bots', 'add', bot_name, '--format=json')
    response = json.loads(output[output.find('{'):])
    token = response['token_id']
    if not token or any(c not in '0123456789abcdef' for c in token):
        raise RuntimeError('unexpected bot token format')
    exec_host(service, '''
set -eu
mkdir -p /var/lib/teleport/bot
printf 'version: v2\nproxy_server: teleport:3080\nonboarding:\n  join_method: token\n  token: %s\nstorage:\n  type: directory\n  path: /var/lib/teleport/bot\nservices: []\n' "$1" > /etc/tbot.yaml
systemctl enable --now tbot
''', token, timeout=180)
    wait_for(f'{service} bot joined and received credentials', lambda: exec_host(service,
        'systemctl is-active --quiet tbot && test -s /var/lib/teleport/bot/tlscert && echo ready') == 'ready')


def running(service):
    result = compose('exec', '-T', service, 'systemctl', 'is-active', 'teleport', check=False)
    return result == 'active'


def node_count_at_least(count):
    return len(json.loads(tctl('nodes', 'ls', '--format=json'))) >= count


def version_and_hash(service, version, expected_hash, binary='/usr/local/bin/teleport'):
    result = exec_host(service, '''
set -eu
"$1" version
sha256sum "$1"
''', binary)
    return f'v{version} ' in result and expected_hash in result


def file_hash(service, path):
    return exec_host(service, 'sha256sum "$1"', path).split()[0]


def running_binary_hash(service, unit='teleport'):
    return exec_host(service, r'''
set -eu
pid=$(systemctl show "$1" --property=MainPID --value)
test "$pid" -gt 0
case "$(readlink "/proc/$pid/exe")" in
  */rosetta)
    # On Apple Silicon the kernel exposes the translator as exe. Check the
    # mapped binary's inode as well as its hash, so a replaced path cannot pass.
    mapping=$(awk '$2 ~ /x/ && $6 ~ /\/bin\/(teleport|tbot)$/ {print $5, $6; exit}' "/proc/$pid/maps")
    test -n "$mapping"
    inode=${mapping%% *}
    binary=${mapping#* }
    test "$(stat -c %i "$binary")" = "$inode"
    sha256sum "$binary"
    ;;
  *) sha256sum "/proc/$pid/exe" ;;
esac
''', unit).split()[0]


def set_resource(name, content):
    path = ROOT / 'generated' / name
    path.write_text(content)
    tctl('create', '-f', f'/lab/{name}')


def set_version(start, target):
    set_resource('autoupdate-version.yaml', f'''kind: autoupdate_version
version: v1
metadata:
  name: autoupdate-version
spec:
  agents:
    start_version: {start}
    target_version: {target}
    schedule: immediate
    mode: enabled
''')


def advertised(version):
    payload = exec_host('agent-direct', 'curl -fsS https://teleport:3080/webapi/find')
    return json.loads(payload).get('auto_update', {}).get('agent_version', '').lstrip('v') == version


def main():
    start_hash, target_hash = archive_hash(START), archive_hash(TARGET)
    target_bot_hash = archive_hash(TARGET, 'tbot')
    if start_hash == target_hash:
        raise RuntimeError('lab requires two different real Teleport binaries')
    services = ['mirror', 'teleport', 'agent-direct', 'agent-web', 'agent-discovery',
                'agent-existing', 'agent-managed', 'agent-suffix', 'agent-bot']
    compose('up', '-d', '--no-build', *services, timeout=180)
    wait_for('trusted cluster endpoint', lambda: json.loads(exec_host(
        'agent-direct', 'curl -fsS https://teleport:3080/webapi/ping'))['server_version'] == TARGET)
    for host in services[2:]:
        wait_for(f'{host} systemd', lambda host=host: compose(
            'exec', '-T', host, 'systemctl', 'is-system-running', check=False) in ('running', 'degraded'))

    # The lab requires an isolated, freshly created cluster: do not alter an
    # existing managed-update rollout or host installation silently.
    current = subprocess.run(COMPOSE + ['exec', '-T', 'teleport', 'tctl',
        '--config=/lab/teleport.yaml', 'get', 'autoupdate_version', '--format=json'],
        capture_output=True, text=True, timeout=30)
    if current.returncode == 0:
        raise RuntimeError('lab already has update resources; start with a fresh isolated lab')
    if 'does not exist' not in current.stderr:
        raise RuntimeError(f'could not inspect update resources: {current.stderr}')
    for host in services[2:]:
        if exec_host(host, 'if test -e /usr/local/bin/teleport; then echo exists; else echo clean; fi') != 'clean':
            raise RuntimeError(f'{host} is not clean')

    # Download the cluster's script first, then test a missing archive and an
    # unavailable mirror without allowing either failure to install anything.
    exec_host('agent-direct', 'curl -fsS https://teleport:3080/scripts/install.sh -o /tmp/install.sh')
    archive = MIRROR / f'teleport-v{TARGET}-linux-amd64-bin.tar.gz'
    hidden_archive = archive.with_name(archive.name + '.lab-hidden')
    archive.rename(hidden_archive)
    try:
        failed = subprocess.run(COMPOSE + ['exec', '-T', 'agent-direct', 'bash', '/tmp/install.sh'],
                                capture_output=True, text=True, timeout=180)
        if failed.returncode == 0 or '404' not in (failed.stdout + failed.stderr):
            raise RuntimeError('missing mirror archive was not rejected with a 404')
        if exec_host('agent-direct', 'test ! -e /usr/local/bin/teleport && echo clean') != 'clean':
            raise RuntimeError('missing mirror archive installed teleport')
        print('PASS: missing mirror artifact rejected without installation', flush=True)
    finally:
        hidden_archive.rename(archive)

    try:
        compose('stop', 'mirror', timeout=60)
        failed = subprocess.run(COMPOSE + ['exec', '-T', 'agent-direct', 'bash', '/tmp/install.sh'],
                                capture_output=True, text=True, timeout=180)
        if failed.returncode == 0:
            raise RuntimeError('installer succeeded while the mirror was unavailable')
        if exec_host('agent-direct', 'test ! -e /usr/local/bin/teleport && echo clean') != 'clean':
            raise RuntimeError('mirror outage installed teleport')
        print('PASS: mirror outage rejected without installation', flush=True)
    finally:
        compose('start', 'mirror', timeout=60)
    wait_for('mirror recovered', lambda: exec_host('agent-direct',
        'curl -fsS -o /dev/null "https://mirror:8443/teleport-v$1-linux-amd64-bin.tar.gz.sha256"', TARGET) == '')

    # Verify a failed integrity check cannot install a binary.
    checksum = MIRROR / f'teleport-v{TARGET}-linux-amd64-bin.tar.gz.sha256'
    original_checksum = checksum.read_bytes()
    try:
        checksum.write_text('0' * 64 + f'  teleport-v{TARGET}-linux-amd64-bin.tar.gz\n')
        failure = subprocess.run(COMPOSE + ['exec', '-T', 'agent-direct', 'bash', '-se'], input='''
curl -fsS https://teleport:3080/scripts/install.sh -o /tmp/install.sh
bash /tmp/install.sh
''', capture_output=True, text=True, timeout=180)
        if failure.returncode == 0:
            raise RuntimeError('installer accepted a corrupt checksum')
        if 'checksum mismatch' not in (failure.stdout + failure.stderr).lower():
            raise RuntimeError(f'installer failed for a reason other than the checksum: {failure.stderr}')
        if exec_host('agent-direct', 'test ! -e /usr/local/bin/teleport && echo clean') != 'clean':
            raise RuntimeError('checksum failure installed teleport')
        print('PASS: corrupt checksum rejected before installation', flush=True)
    finally:
        checksum.write_bytes(original_checksum)

    direct_token = new_token()
    exec_host('agent-direct', '''
set -eu
curl -fsS https://teleport:3080/scripts/install.sh -o /tmp/install.sh
bash /tmp/install.sh
teleport node configure --output=file --token="$1" --proxy=teleport:3080
systemctl enable --now teleport
''', direct_token, timeout=300)
    wait_for('direct installation and join', lambda: running('agent-direct'), timeout=180)
    wait_for('direct agent registered with cluster', lambda: node_count_at_least(1))
    if not version_and_hash('agent-direct', TARGET, target_hash):
        raise RuntimeError('direct installer selected the wrong artifact')

    client_output = exec_host('agent-direct', '''
set -eu
mkdir -p /tmp/lab-client
curl -fsS "https://mirror:8443/teleport-v$1-linux-amd64-bin.tar.gz" -o /tmp/lab-client/start.tar.gz
tar -xzf /tmp/lab-client/start.tar.gz -C /tmp/lab-client teleport/tsh
TELEPORT_HOME=/tmp/lab-client/home TELEPORT_CDN_BASE_URL=https://mirror:8443 \
  TELEPORT_TOOLS_VERSION="$2" /tmp/lab-client/teleport/tsh version
''', START, TARGET, timeout=300)
    if f'v{TARGET}' not in client_output:
        raise RuntimeError(f'Linux client tool did not re-execute with target version: {client_output}')
    client_hashes = exec_host('agent-direct',
        'find /tmp/lab-client/home -type f -name tsh -exec sha256sum {} +')
    if archive_hash(TARGET, 'tsh') not in client_hashes:
        raise RuntimeError('cached client binary does not match the mirror archive')
    print('PASS: Linux client tool updated from the local mirror', flush=True)

    web_token = new_token()
    exec_host('agent-web', '''
set -eu
curl -fsS "https://teleport:3080/scripts/$1/install-node.sh?method=token" -o /tmp/join.sh
bash /tmp/join.sh
''', web_token, timeout=300)
    wait_for('web join script enrollment', lambda: running('agent-web'), timeout=180)
    wait_for('web agent registered with cluster', lambda: node_count_at_least(2))
    if not version_and_hash('agent-web', TARGET, target_hash):
        raise RuntimeError('web installer selected the wrong artifact')

    # Execute the rendered discovery installer and its real installation step.
    # A sudo wrapper intercepts only the final cloud-specific configuration
    # command; actual provider identity requires a real cloud environment.
    discovery = exec_host('agent-discovery',
        'curl -fsS https://teleport:3080/webapi/scripts/installer/default-installer')
    if '/scripts/install.sh' not in discovery or 'install autodiscover-node' not in discovery:
        raise RuntimeError('discovery installer bypasses the cluster installer')
    exec_host('agent-discovery', '''
set -eu
mkdir -p /tmp/discovery-bin
cat > /tmp/discovery-bin/sudo <<'WRAPPER'
#!/bin/sh
case " $* " in
  *" /usr/local/bin/teleport install autodiscover-node "*)
    printf '%s\n' "$@" > /tmp/discovery-configure-args
    exit 0
    ;;
esac
exec /usr/bin/sudo "$@"
WRAPPER
chmod 0755 /tmp/discovery-bin/sudo
curl -fsS https://teleport:3080/webapi/scripts/installer/default-installer -o /tmp/discovery.sh
PATH="/tmp/discovery-bin:$PATH" sh /tmp/discovery.sh simulated-token
grep -Fx -- '--public-proxy-addr=teleport:3080' /tmp/discovery-configure-args
grep -Fx -- 'simulated-token' /tmp/discovery-configure-args
''', timeout=300)
    if not version_and_hash('agent-discovery', TARGET, target_hash):
        raise RuntimeError('discovery installation selected the wrong artifact')
    print('PASS: discovery installer selection and installation (cloud delivery simulated)', flush=True)

    existing_token = new_token()
    exec_host('agent-existing', '''
set -eu
mkdir /tmp/existing
cd /tmp/existing
archive="teleport-v$1-linux-amd64-bin.tar.gz"
curl -fsS "https://mirror:8443/$archive" -o "$archive"
curl -fsS "https://mirror:8443/$archive.sha256" -o "$archive.sha256"
sha256sum --check "$archive.sha256"
tar xzf "$archive"
teleport/install
teleport node configure --output=file --token="$2" --proxy=teleport:3080
teleport install systemd -o /etc/systemd/system/teleport.service
systemctl daemon-reload
systemctl enable --now teleport
''', START, existing_token, timeout=300)
    wait_for('existing agent joined', lambda: running('agent-existing'))
    wait_for('existing agent registered with cluster', lambda: node_count_at_least(3))
    if not version_and_hash('agent-existing', START, start_hash):
        raise RuntimeError('existing agent is not the expected initial build')

    set_resource('autoupdate-config.yaml', '''kind: autoupdate_config
version: v1
metadata:
  name: autoupdate-config
spec:
  agents:
    mode: enabled
    strategy: halt-on-error
    schedules:
      regular:
        - name: blue
          days: [Mon, Tue, Wed, Thu, Fri, Sat, Sun]
          start_hour: 0
        - name: default
          days: [Mon, Tue, Wed, Thu, Fri, Sat, Sun]
          start_hour: 0
''')
    set_version(START, START)
    wait_for('cluster advertises initial update version', lambda: advertised(START), timeout=180)
    exec_host('agent-existing', '''
set -eu
teleport-update enable --proxy=teleport:3080 --base-url=https://mirror:8443 --overwrite
systemctl is-enabled teleport-update.timer
''', timeout=300)
    print('PASS: existing agent enrolled into managed updates', flush=True)
    try:
        compose('stop', 'mirror', timeout=60)
        if not running('agent-existing') or not version_and_hash('agent-existing', START, start_hash):
            raise RuntimeError('mirror outage disrupted the existing agent')
        if not node_count_at_least(3):
            raise RuntimeError('existing agent is no longer registered during mirror outage')
        print('PASS: existing agent remains running during mirror outage', flush=True)
    finally:
        compose('start', 'mirror', timeout=60)
    wait_for('mirror recovered for managed enrollment', lambda: exec_host('agent-existing',
        'curl -fsS -o /dev/null "https://mirror:8443/teleport-v$1-linux-amd64-bin.tar.gz.sha256"', START) == '')

    managed_token = new_token()
    exec_host('agent-managed', '''
set -eu
curl -fsS 'https://teleport:3080/scripts/install.sh?group=blue' -o /tmp/install.sh
bash /tmp/install.sh
teleport node configure --output=file --token="$1" --proxy=teleport:3080
systemctl enable --now teleport
systemctl is-enabled teleport-update.timer
grep -F 'https://mirror:8443' /opt/teleport/default/update.yaml
grep -F 'group: blue' /opt/teleport/default/update.yaml
''', managed_token, timeout=300)
    wait_for('new managed agent registered', lambda: running('agent-managed') and node_count_at_least(4))
    if not version_and_hash('agent-managed', START, start_hash):
        raise RuntimeError('new managed agent selected the wrong initial artifact')
    create_bot('agent-managed', 'mirror-lab-colocated-bot')

    suffix_token = new_token()
    exec_host('agent-suffix', '''
set -eu
curl -fsS 'https://teleport:3080/scripts/install.sh?group=blue' -o /tmp/install.sh
TELEPORT_INSTALL_SUFFIX=lab bash /tmp/install.sh
/opt/teleport/lab/bin/teleport node configure --output=file:///etc/teleport_lab.yaml \
  --data-dir=/var/lib/teleport_lab --token="$1" --proxy=teleport:3080
systemctl enable --now teleport_lab
systemctl is-enabled teleport-update_lab.timer
grep -F 'https://mirror:8443' /opt/teleport/lab/update.yaml
grep -F 'group: blue' /opt/teleport/lab/update.yaml
''', suffix_token, timeout=300)
    wait_for('suffixed agent registered', lambda: compose('exec', '-T', 'agent-suffix',
        'systemctl', 'is-active', 'teleport_lab', check=False) == 'active' and node_count_at_least(5))
    if not version_and_hash('agent-suffix', START, start_hash, '/opt/teleport/lab/bin/teleport'):
        raise RuntimeError('suffixed agent selected the wrong initial artifact')

    exec_host('agent-bot', '''
set -eu
curl -fsS 'https://teleport:3080/scripts/install.sh?group=default' -o /tmp/install.sh
bash /tmp/install.sh
systemctl is-enabled teleport-update.timer
''', timeout=300)
    create_bot('agent-bot', 'mirror-lab-standalone-bot')

    # While the cluster advertises the new version, make its checksum invalid
    # once and verify that the updater leaves the working agent untouched.
    target_checksum = MIRROR / f'teleport-v{TARGET}-linux-amd64-bin.tar.gz.sha256'
    correct_checksum = target_checksum.read_bytes()
    try:
        checksum_path = f'teleport-v{TARGET}-linux-amd64-bin.tar.gz.sha256'
        previous_requests = compose('logs', '--no-color', 'mirror').count(checksum_path)
        target_checksum.write_text('0' * 64 + f'  teleport-v{TARGET}-linux-amd64-bin.tar.gz\n')
        set_version(START, TARGET)
        wait_for('cluster advertises target update version', lambda: advertised(TARGET), timeout=180)
        compose('exec', '-T', 'agent-existing', 'systemctl', 'start',
                'teleport-update.service', check=False, timeout=180)
        if compose('logs', '--no-color', 'mirror').count(checksum_path) <= previous_requests:
            raise RuntimeError('updater did not request the target checksum')
        if not version_and_hash('agent-existing', START, start_hash) or not running('agent-existing'):
            raise RuntimeError('bad update checksum changed or stopped the existing agent')
        print('PASS: failed update leaves the existing agent running', flush=True)
    finally:
        target_checksum.write_bytes(correct_checksum)
    # Fail only starts of the new binary. The old revision remains startable,
    # so this probes the updater's actual revert and host recovery behavior.
    exec_host('agent-existing', '''
set -eu
mkdir -p /etc/systemd/system/teleport.service.d
cat > /usr/local/bin/lab-check-version <<'CHECK'
#!/bin/sh
case "$(readlink -f /usr/local/bin/teleport)" in
  */versions/"$LAB_BAD_VERSION"/*) exit 1 ;;
esac
exit 0
CHECK
chmod +x /usr/local/bin/lab-check-version
cat > /etc/systemd/system/teleport.service.d/zz-lab-fail.conf <<EOF
[Service]
Environment=LAB_BAD_VERSION=$1
ExecStartPre=/usr/local/bin/lab-check-version
ExecReload=
ExecReload=/bin/false
Restart=no
EOF
systemctl daemon-reload
''', TARGET)
    try:
        compose('exec', '-T', 'agent-existing', 'systemctl', 'start',
                'teleport-update.service', check=False, timeout=180)
        logs = compose('exec', '-T', 'agent-existing', 'journalctl', '-u',
                       'teleport-update.service', '-n', '200', '--no-pager')
        if 'Reverting symlinks due to failed restart' not in logs:
            raise RuntimeError('updater did not report a failed restart and revert')
        if not version_and_hash('agent-existing', START, start_hash):
            raise RuntimeError('failed service start did not revert the installed version')
        print('PASS: failed service start reverted the installed version', flush=True)
    finally:
        exec_host('agent-existing', '''
set -eu
rm -f /etc/systemd/system/teleport.service.d/zz-lab-fail.conf /usr/local/bin/lab-check-version
systemctl daemon-reload
systemctl reset-failed teleport || true
systemctl start teleport
''')
    wait_for('agent recovered after failed service start', lambda: running('agent-existing'))
    # A failed restart marks the target as skipped. Upstream's explicit enable
    # command retries that revision and clears the marker after success.
    exec_host('agent-existing', 'teleport-update enable', timeout=300)
    print('PASS: operator retry cleared the broken-version marker', flush=True)
    # Trigger the normal systemd updater service to avoid waiting for its
    # five-minute timer. Do not use --now, which overrides rollout decisions.
    wait_for('rollout to target build', lambda: (
        compose('exec', '-T', 'agent-existing', 'systemctl', 'start', 'teleport-update.service', check=False, timeout=180)
        is not None and version_and_hash('agent-existing', TARGET, target_hash)), timeout=600)
    wait_for('updated agent is healthy and running the target binary', lambda:
        running('agent-existing') and running_binary_hash('agent-existing') == target_hash)
    wait_for('new managed agent updated', lambda: (
        compose('exec', '-T', 'agent-managed', 'systemctl', 'start', 'teleport-update.service',
                check=False, timeout=180) is not None
        and version_and_hash('agent-managed', TARGET, target_hash) and running('agent-managed')
        and running_binary_hash('agent-managed') == target_hash), timeout=600)
    wait_for('colocated bot remains active', lambda: compose('exec', '-T', 'agent-managed',
        'systemctl', 'is-active', 'tbot', check=False) == 'active'
        and running_binary_hash('agent-managed', 'tbot') == target_bot_hash)
    if file_hash('agent-managed', '/usr/local/bin/tbot') != target_bot_hash:
        raise RuntimeError('colocated bot binary does not match the updated release')
    wait_for('suffixed agent updated', lambda: (
        compose('exec', '-T', 'agent-suffix', 'systemctl', 'start', 'teleport-update_lab.service',
                check=False, timeout=180) is not None
        and version_and_hash('agent-suffix', TARGET, target_hash, '/opt/teleport/lab/bin/teleport')
        and compose('exec', '-T', 'agent-suffix', 'systemctl', 'is-active',
                    'teleport_lab', check=False) == 'active'
        and running_binary_hash('agent-suffix', 'teleport_lab') == target_hash), timeout=600)
    wait_for('standalone bot installation updated', lambda: (
        compose('exec', '-T', 'agent-bot', 'systemctl', 'start', 'teleport-update.service',
                check=False, timeout=180) is not None
        and version_and_hash('agent-bot', TARGET, target_hash)
        and compose('exec', '-T', 'agent-bot', 'systemctl', 'is-active', 'tbot', check=False) == 'active'
        and running_binary_hash('agent-bot', 'tbot') == target_bot_hash), timeout=600)
    if file_hash('agent-bot', '/usr/local/bin/tbot') != target_bot_hash:
        raise RuntimeError('standalone bot binary does not match the updated release')
    print('PASS: local mirror enrollment and managed-update flow', flush=True)


if __name__ == '__main__':
    try:
        main()
    except Exception as error:
        print(f'FAIL: {error}', file=sys.stderr)
        sys.exit(1)
