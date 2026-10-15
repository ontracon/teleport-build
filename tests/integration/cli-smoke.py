"""Complete a real tsh OIDC login from inside the local lab's network namespace."""
import os
import html
import re
import subprocess
import threading
import time
import ssl
import urllib.parse
import urllib.request

context = ssl.create_default_context(cafile='/lab/generated/ca.pem')
opener = urllib.request.build_opener(urllib.request.HTTPSHandler(context=context), urllib.request.HTTPCookieProcessor())
for attempt in range(60):
    try:
        with opener.open('https://localhost:3080/webapi/ping', timeout=2) as response:
            response.read()
        break
    except urllib.error.URLError:
        if attempt == 59:
            raise RuntimeError('local Teleport Proxy did not become ready')
        time.sleep(0.5)
env = dict(os.environ, TELEPORT_HOME='/tmp/zitadel-lab-tsh')
process = subprocess.Popen(['/usr/local/bin/tsh', 'login', '--proxy=localhost:3080', '--auth=zitadel', '--browser=none', '--ttl=30'],
                           stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True, env=env, bufsize=1)
done = False
timer = threading.Timer(90, process.kill)
timer.start()
try:
    for line in process.stdout:
        match = re.search(r'http://(?:localhost|127\.0\.0\.1):[0-9]+/[0-9a-f-]{36}', line)
        if match and not done:
            with opener.open(match.group(0), timeout=15) as response:
                form = response.read().decode()
                action_url = response.geturl()
            flow = re.search(r'name="flow" value="([^"]+)"', form)
            if not flow:
                raise RuntimeError('mock login form was not reached')
            payload = urllib.parse.urlencode({'flow': flow.group(1), 'user': 'operator'}).encode()
            with opener.open(urllib.request.Request(action_url, data=payload), timeout=15) as response:
                callback_page = html.unescape(response.read().decode())
            redirect = re.search(r"URL='([^']+)'", callback_page)
            if not redirect:
                raise RuntimeError('Teleport did not return the CLI callback redirect')
            target = urllib.parse.urlparse(redirect.group(1))
            if target.scheme != 'http' or target.hostname not in ('localhost', '127.0.0.1') or target.path != '/callback':
                raise RuntimeError('Teleport rejected the CLI authentication')
            with opener.open(redirect.group(1), timeout=15) as response:
                response.read()
            done = True
        elif 'Logged in as' in line or 'Roles:' in line or 'Valid until:' in line:
            print(line.strip())
        elif 'ERROR' in line:
            print(re.sub(r'https?://\S+', '[URL]', line.strip()))
    if process.wait() != 0 or not done:
        raise RuntimeError('tsh login did not complete')
    subprocess.run(['/usr/local/bin/tsh', 'status'], check=True, env=env, timeout=30)
    subprocess.run(['/usr/local/bin/tsh', 'ls'], check=True, env=env, timeout=30)
    subprocess.run(['/usr/local/bin/tsh', 'ssh', 'root@local-zitadel-lab', 'true'], check=True, env=env, timeout=30)
    print('PASS tsh OIDC login, node listing, and SSH certificate access')
finally:
    timer.cancel()
    if process.poll() is None:
        process.kill()
    process.wait()
