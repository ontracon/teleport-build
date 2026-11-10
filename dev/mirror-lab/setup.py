"""Generate isolated CA, service certificates and Teleport lab configuration."""
import os
from pathlib import Path
import subprocess

ROOT = Path(__file__).resolve().parent / 'generated'


def run(*args):
    subprocess.run(args, check=True, stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)


def main():
    ROOT.mkdir(parents=True, exist_ok=True)
    os.umask(0o077)
    (ROOT / 'mirror').mkdir(exist_ok=True)
    (ROOT / 'mirror').chmod(0o755)
    if not (ROOT / 'ca.pem').exists():
        run('openssl', 'req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-days', '30',
            '-subj', '/CN=Teleport Mirror Lab CA', '-keyout', str(ROOT / 'ca.key'),
            '-out', str(ROOT / 'ca.pem'))
    for name, sans in (('teleport', 'DNS:teleport,DNS:localhost,IP:127.0.0.1'),
                       ('mirror', 'DNS:mirror,DNS:localhost,IP:127.0.0.1')):
        if (ROOT / f'{name}.pem').exists():
            continue
        (ROOT / f'{name}.ext').write_text(f'subjectAltName={sans}\nextendedKeyUsage=serverAuth\n')
        run('openssl', 'req', '-new', '-newkey', 'rsa:2048', '-nodes', '-subj', f'/CN={name}',
            '-keyout', str(ROOT / f'{name}.key'), '-out', str(ROOT / f'{name}.csr'))
        run('openssl', 'x509', '-req', '-days', '30', '-in', str(ROOT / f'{name}.csr'),
            '-CA', str(ROOT / 'ca.pem'), '-CAkey', str(ROOT / 'ca.key'), '-CAcreateserial',
            '-extfile', str(ROOT / f'{name}.ext'), '-out', str(ROOT / f'{name}.pem'))
    (ROOT / 'teleport.yaml').write_text('''version: v3
teleport:
  nodename: mirror-lab-control
  data_dir: /var/lib/teleport
  log:
    output: stderr
    severity: INFO
auth_service:
  enabled: true
  cluster_name: mirror-lab
  listen_addr: 127.0.0.1:3025
  proxy_listener_mode: multiplex
proxy_service:
  enabled: true
  web_listen_addr: 0.0.0.0:3080
  public_addr: teleport:3080
  https_keypairs:
    - key_file: /lab/teleport.key
      cert_file: /lab/teleport.pem
ssh_service:
  enabled: false
''')
    # Certificates are public; secrets are readable only by lab root.
    for name in ('ca.pem', 'mirror.pem', 'teleport.pem'):
        (ROOT / name).chmod(0o644)
    print(f'Prepared mirror lab files in {ROOT}')


if __name__ == '__main__':
    main()
