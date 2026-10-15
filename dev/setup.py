"""Generate local-only TLS certificates, credentials, and Teleport resources."""
import json
from pathlib import Path
import secrets
import subprocess
import sys
import os
from urllib.parse import urlparse


def run(*args):
    subprocess.run(args, check=True, stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)


def write(path, data, mode=0o600):
    path.write_text(data)
    path.chmod(mode)


def main():
    root = Path(sys.argv[1])
    root.mkdir(parents=True, exist_ok=True)
    os.umask(0o077)
    if not (root / 'ca.pem').exists():
        run('openssl', 'req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-days', '30',
            '-subj', '/CN=Teleport Zitadel Local Lab', '-keyout', str(root / 'ca.key'), '-out', str(root / 'ca.pem'))
    for name in ('idp', 'teleport'):
        if (root / f'{name}.pem').exists():
            continue
        write(root / f'{name}.ext', 'subjectAltName=DNS:localhost,IP:127.0.0.1,IP:::1\nextendedKeyUsage=serverAuth\n')
        run('openssl', 'req', '-new', '-newkey', 'rsa:2048', '-nodes', '-subj', '/CN=localhost',
            '-keyout', str(root / f'{name}.key'), '-out', str(root / f'{name}.csr'))
        run('openssl', 'x509', '-req', '-days', '30', '-in', str(root / f'{name}.csr'),
            '-CA', str(root / 'ca.pem'), '-CAkey', str(root / 'ca.key'), '-CAcreateserial',
            '-extfile', str(root / f'{name}.ext'), '-out', str(root / f'{name}.pem'))
    # Keep public issuer trust when adding the private localhost lab CA.
    # SSL_CERT_FILE replaces OpenSSL's default certificate file.
    system_roots = next((Path(path) for path in (
        '/etc/pki/tls/certs/ca-bundle.crt', '/etc/ssl/certs/ca-certificates.crt', '/etc/ssl/cert.pem'
    ) if Path(path).is_file()), None)
    bundle = system_roots.read_text() if system_roots else ''
    write(root / 'trust.pem', bundle.rstrip() + '\n' + (root / 'ca.pem').read_text(), mode=0o644)
    if not (root / 'client-secret').exists():
        write(root / 'client-secret', secrets.token_urlsafe(32) + '\n')
    secret = (root / 'client-secret').read_text().strip()
    config = {
        'issuer_url': 'https://localhost:9443', 'client_id': 'teleport-lab',
        'redirect_url': 'https://localhost:3080/v1/webapi/oidc/callback',
        'project_id': '123', 'organization_id': '456', 'transaction_ttl': '5m',
        'role_mappings': {'operator': ['lab-access'], 'administrator': ['lab-access', 'editor']},
    }
    # Preserve explicitly edited real-IdP configuration across lab restarts.
    if not (root / 'adapter.json').exists():
        write(root / 'adapter.json', json.dumps(config, indent=2) + '\n')
    config = json.loads((root / 'adapter.json').read_text())
    display = 'Zitadel Local Lab' if config['issuer_url'] == 'https://localhost:9443' else 'Zitadel (' + urlparse(config['issuer_url']).hostname + ')'
    write(root / 'teleport.yaml', '''version: v3
teleport:
  nodename: local-zitadel-lab
  data_dir: /var/lib/teleport
  log:
    output: stderr
    severity: INFO
auth_service:
  enabled: true
  cluster_name: zitadel-lab
  listen_addr: 127.0.0.1:3025
  proxy_listener_mode: multiplex
proxy_service:
  enabled: true
  web_listen_addr: 0.0.0.0:3080
  public_addr: localhost:3080
  https_keypairs:
    - key_file: /lab/generated/teleport.key
      cert_file: /lab/generated/teleport.pem
ssh_service:
  enabled: true
  listen_addr: 127.0.0.1:3022
  labels:
    environment: local-lab
''')
    mappings = '\n'.join(
        '    - claim: ' + json.dumps('urn:zitadel:iam:org:project:' + config['project_id'] + ':roles') +
        '\n      value: ' + json.dumps(role) + '\n      roles: ' + json.dumps(targets)
        for role, targets in config['role_mappings'].items()
    )
    labels = {
        'zitadel/project-id': config['project_id'],
        'zitadel/organization-id': config['organization_id'],
        'zitadel/transaction-ttl': config.get('transaction_ttl', '5m'),
    }
    if config.get('required_amr'):
        labels['zitadel/required-amr'] = json.dumps(config['required_amr'])
    label_yaml = '\n'.join('    ' + key + ': ' + json.dumps(value) for key, value in labels.items())
    max_age = '\n  max_age: ' + json.dumps(config['max_authentication_age']) if config.get('max_authentication_age') else ''
    write(root / 'bootstrap.yaml', '''kind: role
version: v7
metadata:
  name: lab-access
spec:
  options:
    max_session_ttl: 1h
  allow:
    logins: [root]
    node_labels:
      environment: local-lab
---
kind: oidc
version: v3
metadata:
  name: zitadel
  labels:
''' + label_yaml + '''
spec:
  provider: zitadel
  display: ''' + json.dumps(display) + '''
  issuer_url: ''' + json.dumps(config['issuer_url']) + '''
  client_id: ''' + json.dumps(config['client_id']) + '''
  client_secret: ''' + json.dumps(secret) + '''
  redirect_url: ''' + json.dumps(config['redirect_url']) + '''
  pkce_mode: enabled''' + max_age + '''
  claims_to_roles:
''' + mappings + '''
---
kind: cluster_auth_preference
version: v2
metadata:
  name: cluster-auth-preference
  labels:
    teleport.dev/origin: dynamic
spec:
  type: oidc
  connector_name: zitadel
  second_factor: webauthn
  webauthn:
    rp_id: localhost
''')
    print('Local lab configuration prepared. UI: https://localhost:3080; issuer: ' + config['issuer_url'])


if __name__ == '__main__':
    main()
