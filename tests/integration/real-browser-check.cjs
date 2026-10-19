// Check only the real IdP handoff. Human login and MFA stay in the user's browser.
const assert = require('node:assert/strict');
const crypto = require('node:crypto');
const fs = require('node:fs');
const path = require('node:path');
const { execFileSync } = require('node:child_process');
const { chromium } = require('playwright');

(async () => {
  const repo = path.resolve(__dirname, '../..');
  const root = path.join(repo, 'dev/generated-real');
  // Read the native backend settings because UI edits supersede bootstrap files.
  const resource = name => {
    const items = JSON.parse(execFileSync('docker', ['compose', '-f', 'dev/compose.real.yaml', 'exec', '-T', 'teleport',
      '/usr/local/bin/tctl', '--config=/lab/generated/teleport.yaml', 'get', name, '--format=json'],
      { cwd: repo, encoding: 'utf8' }));
    return Array.isArray(items) ? items[0] : items;
  };
  const preference = resource('cluster_auth_preference');
  assert.equal(preference.spec.type, 'oidc', 'Real lab default is not OIDC');
  const connector = resource('oidc/' + preference.spec.connector_name);
  assert.equal(connector.spec.provider, 'zitadel', 'Default OIDC connector is not managed by Zitadel');
  const cfg = {
    issuer_url: connector.spec.issuer_url,
    project_id: connector.metadata.labels['zitadel/project-id'],
  };
  const issuer = new URL(cfg.issuer_url);
  const certificate = new crypto.X509Certificate(fs.readFileSync(path.join(root, 'teleport.pem')));
  const spki = crypto.createHash('sha256').update(certificate.publicKey.export({ type: 'spki', format: 'der' })).digest('base64');
  const chrome = process.env.CHROME_PATH || '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome';
  const browser = await chromium.launch({ headless: true,
    ...(fs.existsSync(chrome) ? { executablePath: chrome } : {}),
    args: ['--ignore-certificate-errors-spki-list=' + spki],
  });
  try {
    // Ignore only the generated localhost leaf key; real issuer TLS stays checked.
    const context = await browser.newContext();
    const page = await context.newPage();
    let authorization;
    page.on('request', request => {
      const url = new URL(request.url());
      if (url.origin === issuer.origin && url.pathname === '/oauth/v2/authorize') authorization = url;
    });
    await page.goto('https://localhost:3080', { waitUntil: 'networkidle' });
    await page.getByRole('button', { name: connector.spec.display || connector.metadata.name, exact: true }).click();
    await page.waitForURL(url => url.origin === issuer.origin && url.pathname.startsWith('/ui/'), { timeout: 30000 });
    assert(authorization, 'Browser did not request the Zitadel authorization endpoint');
    const scopes = new Set(authorization.searchParams.get('scope').split(' '));
    for (const scope of ['openid', 'profile', 'email',
      'urn:zitadel:iam:org:project:id:' + cfg.project_id + ':aud',
      'urn:zitadel:iam:org:projects:roles']) {
      assert(scopes.has(scope), 'Browser authorization request is missing a required role or identity scope');
    }
    const text = await page.locator('body').innerText();
    assert(!/internal error occurred|User Agent does not correspond|invalid.redirect|invalid.client/i.test(text), 'Zitadel rejected the login handoff');
    await page.locator('input:not([type="hidden"]):not([type="submit"])').first().waitFor({ timeout: 15000 });
    const results = path.join(__dirname, 'test-results');
    fs.mkdirSync(results, { recursive: true });
    await page.screenshot({ path: path.join(results, 'real-zitadel-login.png'), fullPage: true });
    const target = new URL(page.url());
    console.log('PASS Teleport → real Zitadel login page: ' + target.origin + target.pathname);
    console.log('PASS browser requested the configured project role scopes.');
    console.log('Human login and MFA have not been completed.');
  } finally { await browser.close(); }
})().catch(error => { console.error(error.message); process.exitCode = 1; });
