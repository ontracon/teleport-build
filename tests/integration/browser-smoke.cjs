// Exercise the actual Teleport UI and callback cookie handling with the mock.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { chromium } = require('playwright');

const base = 'https://localhost:3080';
const results = path.join(__dirname, 'test-results');
fs.mkdirSync(results, { recursive: true });
const chrome = process.env.CHROME_PATH || '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome';
const displayOnly = process.argv.includes('--display-only');
const sessionCookie = cookies => cookies.some(cookie => cookie.name === '__Host-session' && cookie.value);

async function begin(page) {
  await page.goto(base, { waitUntil: 'networkidle' });
  await page.getByRole('button', { name: /Zitadel Local Lab/ }).click();
  await page.waitForURL('https://localhost:9443/authorize?**');
  await page.getByRole('heading', { name: 'Test a Zitadel login' }).waitFor();
}

(async () => {
  const browser = await chromium.launch({ headless: true, ...(fs.existsSync(chrome) ? { executablePath: chrome } : {}) });
  const report = [];
  try {
    for (const [name, button, success] of [
      ['operator', 'Sign in as operator', true],
      ['administrator', 'Sign in as administrator', true],
      ['no mapped role', 'Test: no mapped role', false],
      ['wrong organization', 'Test: wrong organization', false],
      ['unverified email', 'Test: unverified email', false],
    ]) {
      if (displayOnly && !success) continue;
      const context = await browser.newContext({ ignoreHTTPSErrors: true, viewport: { width: 1440, height: 960 } });
      const page = await context.newPage();
      let callback;
      page.on('request', request => {
        if (request.url().startsWith(base + '/v1/webapi/oidc/callback?')) callback = request.url();
      });
      try {
        const authenticatedContext = success ? page.waitForResponse(response => {
          const url = new URL(response.url());
          return url.origin === base && /\/webapi\/sites\/[^/]+\/context$/.test(url.pathname) && response.status() === 200;
        }, { timeout: 45000 }) : null;
        await begin(page);
        await page.getByRole('button', { name: button, exact: true }).click();
        if (success) {
          await page.waitForURL(url => url.port === '3080' && url.pathname.startsWith('/web') && !/login|callback/.test(url.pathname), { timeout: 30000 });
          assert(sessionCookie(await context.cookies(base)), 'successful login did not set a Teleport session cookie');
          await page.getByText('local-zitadel-lab', { exact: true }).first().waitFor({ timeout: 30000 });
          const menu = page.getByRole('button', { name: 'User Menu', exact: true });
          await menu.getByText(name + '@lab.invalid', { exact: true }).waitFor();
          // Inspect the UI's authenticated response without reproducing its
          // bearer-token headers or exposing session credentials.
          const response = await authenticatedContext;
          const user = await response.json();
          assert.match(user.userName, /^zitadel-[a-f0-9]{32}$/, 'readable label replaced the authentication identity');
          assert.equal(user.displayName, name + '@lab.invalid', 'user context is missing the verified display email');
          await page.screenshot({ path: path.join(results, name + '.png'), fullPage: true });
          assert(callback, 'browser callback was not observed');
          if (!displayOnly) {
            const replay = await context.request.get(callback, { maxRedirects: 0 });
            assert.match(await replay.text(), /\/web\/msg\/error\/login/, 'consumed callback was accepted');
          }
        } else {
          await page.waitForURL(url => url.port === '3080' && url.pathname.startsWith('/web/msg/error/login'), { timeout: 30000 });
          assert(!sessionCookie(await context.cookies(base)), 'denied login set a Teleport session cookie');
        }
        report.push({ case: name, passed: true });
        console.log('PASS ' + name + (success ? ': readable email and stable authentication identity' : ''));
      } catch (error) {
        await page.screenshot({ path: path.join(results, 'failure.png'), fullPage: true }).catch(() => {});
        throw error;
      } finally { await context.close(); }
    }
    if (displayOnly) {
      fs.writeFileSync(path.join(results, 'display-name.json'), JSON.stringify(report, null, 2) + '\n');
      return;
    }
    // Complete a valid IdP authentication in a browser without the initiating
    // Teleport CSRF cookie. The Proxy must refuse to install a web session.
    const original = await browser.newContext({ ignoreHTTPSErrors: true });
    const stranger = await browser.newContext({ ignoreHTTPSErrors: true });
    const page = await original.newPage();
    await begin(page);
    let release;
    const intercepted = new Promise(resolve => { release = resolve; });
    await page.route('https://localhost:9443/authorize', async route => {
      const response = await route.fetch({ maxRedirects: 0 });
      assert.equal(response.status(), 303, 'mock provider did not issue a callback');
      release(response.headers().location);
      await route.abort();
    });
    await page.getByRole('button', { name: 'Sign in as operator', exact: true }).click();
    const callback = await Promise.race([intercepted, new Promise((_, reject) => setTimeout(() => reject(new Error('callback interception timed out')), 15000))]);
    const rejected = await stranger.request.get(callback, { maxRedirects: 0 });
    assert.match(await rejected.text(), /\/web\/msg\/error\/login/, 'wrong-browser callback was accepted');
    assert(!sessionCookie(await stranger.cookies(base)), 'wrong-browser callback installed a session');
    report.push({ case: 'browser CSRF binding', passed: true });
    console.log('PASS browser CSRF binding');
    await original.close();
    await stranger.close();
    fs.writeFileSync(path.join(results, 'browser.json'), JSON.stringify(report, null, 2) + '\n');
  } finally { await browser.close(); }
})().catch(error => { console.error(error.message); process.exitCode = 1; });
