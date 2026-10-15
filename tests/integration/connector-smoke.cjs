// Test connector management with the isolated mock lab; never use real users.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { chromium } = require('playwright');

const base = 'https://localhost:3080';
const endpoint = base + '/v1/webapi/zitadel/connectors';
const results = path.join(__dirname, 'test-results');
const chrome = process.env.CHROME_PATH || '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome';
const testName = 'connector-ui-test';

async function login(browser, identity) {
  const context = await browser.newContext({ ignoreHTTPSErrors: true, viewport: { width: 1440, height: 960 } });
  const page = await context.newPage();
  const userResponse = page.waitForResponse(response => /\/webapi\/sites\/[^/]+\/context$/.test(new URL(response.url()).pathname) && response.status() === 200);
  await page.goto(base, { waitUntil: 'networkidle' });
  await page.getByRole('button', { name: /Zitadel/ }).click();
  await page.waitForURL('https://localhost:9443/authorize?**');
  await page.getByRole('button', { name: 'Sign in as ' + identity, exact: true }).click();
  await page.getByText('local-zitadel-lab', { exact: true }).first().waitFor();
  const response = await userResponse;
  const authorization = await response.request().headerValue('authorization');
  assert(authorization, 'UI did not make an authenticated context request');
  return { context, page, headers: { authorization } };
}

async function readConnector(session, name = 'zitadel') {
  const response = await session.context.request.get(endpoint + '/' + encodeURIComponent(name), { headers: session.headers });
  assert.equal(response.status(), 200, 'connector read failed');
  return response.json();
}

async function setEditor(page, content) {
  const editor = page.locator('.ace_editor').first();
  await editor.waitFor();
  await editor.evaluate((element, value) => element.env.editor.session.setValue(value), content);
}

async function saveEditor(page, method) {
  const response = page.waitForResponse(response => response.url().startsWith(endpoint) && response.request().method() === method);
  await page.getByRole('button', { name: 'Save Changes', exact: true }).click();
  const saved = await response;
  assert.equal(saved.status(), 200, 'connector save failed');
  const resource = await saved.json();
  assert(resource.content.includes('<existing-secret>'), 'save response exposed the credential');
  await page.waitForURL(base + '/web/sso');
  return resource;
}

(async () => {
  fs.mkdirSync(results, { recursive: true });
  const browser = await chromium.launch({ headless: true, ...(fs.existsSync(chrome) ? { executablePath: chrome } : {}) });
  let admin;
  let original;
  try {
    admin = await login(browser, 'administrator');
    await admin.page.goto(base + '/web/sso');
    const tile = admin.page.getByTestId('zitadel-tile');
    await tile.getByText('Default', { exact: true }).waitFor();
    assert.equal(await admin.page.getByTestId('Local Connector-tile').getByText('Default', { exact: true }).count(), 0, 'local connector incorrectly appears as default');
    const legacyList = await admin.context.request.get(base + '/v1/webapi/github', { headers: admin.headers });
    assert.equal(legacyList.status(), 200);
    assert.equal((await legacyList.json()).defaultConnectorType, 'oidc', 'GitHub listing overwrote the actual default');
    const list = await admin.context.request.get(endpoint, { headers: admin.headers });
    assert.equal(list.status(), 200);
    assert.equal((await list.json()).defaultConnectorName, 'zitadel');
    console.log('PASS Zitadel is listed as default; GET preserves authentication preferences');
    await admin.page.screenshot({ path: path.join(results, 'zitadel-connectors.png') });

    original = await readConnector(admin);
    assert(original.content.includes('<existing-secret>'), 'editor read exposed the credential');
    await tile.getByTestId('button').click();
    await admin.page.getByText('Edit', { exact: true }).click();
    await admin.page.getByRole('heading', { name: /Editing Zitadel Auth Connector/ }).last().waitFor();
    await setEditor(admin.page, original.content.replace(/display: [^\n]+/, 'display: Zitadel Connector UI Test'));
    await saveEditor(admin.page, 'PUT');
    const edited = await readConnector(admin);
    assert(edited.content.includes('Zitadel Connector UI Test'), 'saved settings did not reach the backend');
    console.log('PASS YAML editor saves native settings and preserves the hidden secret');

    const operator = await login(browser, 'operator');
    const forbidden = await operator.context.request.put(endpoint + '/zitadel', { headers: operator.headers, data: { content: edited.content } });
    assert.equal(forbidden.status(), 403, 'operator was permitted to edit an auth connector');
    await operator.context.close();
    console.log('PASS a fresh login uses the edited connector; operator cannot edit it');

    const stale = await admin.context.request.put(endpoint + '/zitadel', { headers: admin.headers, data: { content: original.content } });
    assert.notEqual(stale.status(), 200, 'stale editor silently overwrote newer settings');
    console.log('PASS stale connector revisions are rejected');

    await admin.page.getByRole('button', { name: 'New Zitadel Connector', exact: true }).click();
    await admin.page.getByRole('heading', { name: /Creating new Zitadel Auth Connector/ }).last().waitFor();
    const mockSecret = fs.readFileSync(path.join(__dirname, '../../dev/generated/client-secret'), 'utf8').trim();
    const newContent = edited.content
      .replace(/\n  name: zitadel\n/, '\n  name: ' + testName + '\n')
      .replace(/\n  revision: [^\n]+/, '')
      .replace(/display: [^\n]+/, 'display: Zitadel UI Test Connector')
      .replace(/client_secret: [^\n]+/, 'client_secret: ' + JSON.stringify(mockSecret));
    await setEditor(admin.page, newContent);
    await saveEditor(admin.page, 'POST');
    await admin.page.getByTestId(testName + '-tile').waitFor();
    console.log('PASS create a Zitadel connector through the YAML editor');

    const defaultSaved = admin.page.waitForResponse(response => response.url().endsWith('/v1/webapi/authconnector/default') && response.request().method() === 'PUT');
    await admin.page.getByTestId(testName + '-tile').getByTestId('button').click();
    await admin.page.getByText('Set as default', { exact: true }).click();
    assert.equal((await defaultSaved).status(), 200, 'setting a Zitadel default failed');
    console.log('PASS set a Zitadel connector as the default in the UI');

    const removed = await admin.context.request.delete(endpoint + '/' + testName, { headers: admin.headers });
    assert.equal(removed.status(), 200, 'test connector deletion failed');
    const afterDelete = await admin.context.request.get(endpoint, { headers: admin.headers });
    assert.equal((await afterDelete.json()).defaultConnectorName, 'zitadel', 'deleted default did not fall back to the remaining Zitadel connector');
    console.log('PASS deleting a default falls back to the remaining Zitadel connector');
    fs.writeFileSync(path.join(results, 'connector-management.json'), JSON.stringify({ passed: true, connector: 'zitadel' }, null, 2) + '\n');
  } catch (error) {
    if (admin) await admin.page.screenshot({ path: path.join(results, 'connector-failure.png'), mask: [admin.page.locator('.ace_editor')] }).catch(() => {});
    throw error;
  } finally {
    if (admin) {
      // Restore the mock connector and remove any test resource on failure too.
      await admin.context.request.delete(endpoint + '/' + testName, { headers: admin.headers }).catch(() => {});
      if (original) {
        const current = await readConnector(admin).catch(() => null);
        if (current) {
          const revision = current.content.match(/\n  revision: ([^\n]+)/)?.[1];
          const content = revision ? original.content.replace(/\n  revision: [^\n]+/, '\n  revision: ' + revision) : original.content;
          const restore = await admin.context.request.put(endpoint + '/zitadel', { headers: admin.headers, data: { content } });
          assert.equal(restore.status(), 200, 'mock connector restoration failed');
        }
      }
      await admin.context.close();
    }
    await browser.close();
  }
})().catch(error => { console.error(error.message); process.exitCode = 1; });
