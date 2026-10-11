/**
 * E2E — Tools › Companion Contacts.
 *
 * Add the nodes CoreScope has heard to a MeshCore companion over USB, and
 * remove an area of the companion's existing contacts. The companion is
 * tests/helpers/fake-companion.js behind a fake navigator.serial, so the
 * page talks the real protocol (frames, codes, layout) to it.
 *
 * Run: BASE_URL=http://localhost:13581 node tests/e2e/test-companion-contacts-e2e.js
 */
'use strict';
const path = require('node:path');
const fs = require('node:fs');
const { chromium } = require('playwright');

const BASE = process.env.BASE_URL || 'http://localhost:13581';
const FAKE = fs.readFileSync(path.join(__dirname, '..', 'helpers', 'fake-companion.js'), 'utf8');

let passed = 0, failed = 0;
async function step(name, fn) {
  try { await fn(); passed++; console.log('  ✓ ' + name); }
  catch (e) { failed++; console.error('  ✗ ' + name + ': ' + e.message); }
}
function assert(c, m) { if (!c) throw new Error(m || 'assertion failed'); }

(async () => {
  const browser = await chromium.launch({
    headless: true,
    executablePath: process.env.CHROMIUM_PATH || undefined,
    args: ['--no-sandbox', '--disable-gpu', '--disable-dev-shm-usage'],
  });
  console.log(`\n=== Companion Contacts — E2E against ${BASE} ===`);

  // Nodes with positions from the fixture; seed the fake companion with one
  // of them so "already on the device" has something to show.
  const ctx0 = await browser.newContext();
  const nodesResp = await ctx0.request.get(BASE + '/api/nodes?limit=2000');
  const all = (await nodesResp.json()).nodes || [];
  await ctx0.close();
  const placed = all.filter((n) => typeof n.lat === 'number' && typeof n.lon === 'number' && (n.lat || n.lon) && /^[0-9a-f]{64}$/i.test(n.public_key));
  const lat = placed.reduce((s, n) => s + n.lat, 0) / placed.length;
  const lon = placed.reduce((s, n) => s + n.lon, 0) / placed.length;
  const seeded = placed.find((n) => /repeater/i.test(n.role || '')) || placed[0];
  const seed = { key: seeded.public_key.toLowerCase(), type: 2, name: seeded.name || 'seeded', lat: seeded.lat, lon: seeded.lon, lastAdvert: 1759000000, outPathLen: 1 };
  const far = { key: 'ee'.repeat(32), type: 1, name: 'Far Away', lat: lat + 20, lon: lon + 20, lastAdvert: 1700000000 };

  // Two areas from the server's area list: one round the seeded node, one
  // round the far contact (config.json-style bounding boxes).
  const box = (la, lo, d) => ({ latMin: la - d, latMax: la + d, lonMin: lo - d, lonMax: lo + d });
  const AREAS = [{ key: 'NEAR', label: 'Near' }, { key: 'FAR', label: 'Far' }];
  const POLYS = [Object.assign({ key: 'NEAR', label: 'Near' }, box(seeded.lat, seeded.lon, 0.5)), Object.assign({ key: 'FAR', label: 'Far' }, box(far.lat, far.lon, 1))];
  const nearCount = placed.filter((n) => Math.abs(n.lat - seeded.lat) <= 0.5 && Math.abs(n.lon - seeded.lon) <= 0.5).length;

  const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 }, acceptDownloads: true });
  await ctx.route(/\/api\/config\/areas(\/polygons|\/geojson)?$/, (route) => {
    const u = route.request().url();
    if (u.endsWith('/geojson')) return route.fulfill({ status: 404, body: '' });
    return route.fulfill({ json: u.endsWith('/polygons') ? POLYS : AREAS });
  });
  await ctx.addInitScript({ content: FAKE + `
    window.__fake = new FakeCompanion({ name: 'Bench Radio', maxContacts: 100, contacts: ${JSON.stringify([seed, far])} });
    installFakeSerial(window.__fake);` });
  const page = await ctx.newPage();
  page.setDefaultTimeout(10000);
  const errors = [];
  page.on('pageerror', (e) => errors.push(e.message));

  await step('Tools lists Companion Contacts', async () => {
    await page.goto(BASE + '/#/tools', { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('a.tools-card[href="#/tools/companion-contacts"]');
  });

  await step('the page opens with connect buttons and no device yet', async () => {
    await page.click('a.tools-card[href="#/tools/companion-contacts"]');
    await page.waitForSelector('#ccConnectUsb');
    assert(await page.isEnabled('#ccConnectUsb'), 'USB connect is disabled although Web Serial is present');
    assert(await page.evaluate(() => /not connected/i.test(document.getElementById('ccStatus').textContent)), 'status should say not connected');
  });

  await step('connecting over USB shows the device name and how full it is', async () => {
    await page.click('#ccConnectUsb');
    await page.waitForFunction(() => /Bench Radio/.test(document.getElementById('ccStatus').textContent));
    const t = await page.textContent('#ccStatus');
    assert(/2 of 100/.test(t), 'capacity line: ' + t);
    assert(/USB/.test(t), 'transport not named: ' + t);
  });

  await step('the map shows the nodes CoreScope has heard', async () => {
    await page.waitForFunction(() => window._companionContacts && window._companionContacts.state().nodeMarkers > 0);
  });

  await step('a circle on the map selects nodes, split into new and already on the device', async () => {
    // Draw for real: pick the circle tool, press at the centre, drag out.
    await page.selectOption('#ccHeard', 'any');
    await page.click('#ccToolCircle');
    const box = await page.locator('#ccMap').boundingBox();
    await page.evaluate(([la, lo]) => window._companionContacts.centreOn(la, lo, 9), [seeded.lat, seeded.lon]);
    await page.waitForTimeout(300);
    const cx = box.x + box.width / 2, cy = box.y + box.height / 2;
    await page.mouse.move(cx, cy);
    await page.mouse.down();
    await page.mouse.move(cx + 120, cy, { steps: 6 });
    await page.mouse.up();
    await page.waitForTimeout(200);
    const s = await page.evaluate(() => window._companionContacts.state());
    assert(s.shape === 'circle', 'shape ' + s.shape);
    assert(s.inArea >= 1, 'nothing in the circle: ' + JSON.stringify(s));
    assert(s.already >= 1, 'the seeded contact is not shown as already on the device: ' + JSON.stringify(s));
  });

  await step('picking an area in the area list selects its nodes without drawing', async () => {
    await page.click('#ccToolClear');
    for (const r of ['repeater', 'room', 'companion', 'sensor']) {
      const cb = page.locator(`#ccRoles input[value="${r}"]`);
      if (!(await cb.isChecked())) await cb.check();
    }
    await page.waitForFunction(() => document.querySelectorAll('#ccArea option').length >= 3);
    await page.selectOption('#ccArea', 'NEAR');
    const s = await page.evaluate(() => window._companionContacts.state());
    assert(s.inArea === nearCount, `in area ${s.inArea}, expected the ${nearCount} nodes inside Near`);
    assert(s.already >= 1, 'the seeded contact sits in Near');
    await page.selectOption('#ccArea', '');
  });

  let toAdd = 0;
  await step('the whole area: roles filter, counts and the add button agree', async () => {
    await page.evaluate(([la, lo]) => window._companionContacts.selectCircle(la, lo, 2e7), [lat, lon]); // every node, outliers included
    // Include every role so the count is predictable.
    for (const r of ['repeater', 'room', 'companion', 'sensor']) {
      const cb = page.locator(`#ccRoles input[value="${r}"]`);
      if (!(await cb.isChecked())) await cb.check();
    }
    await page.selectOption('#ccHeard', 'any');
    const s = await page.evaluate(() => window._companionContacts.state());
    assert(s.inArea >= placed.length - 1, `in area ${s.inArea}, expected about ${placed.length}`);
    toAdd = Math.min(s.fresh, 98); // two slots used
    const label = await page.textContent('#ccAddBtn');
    assert(new RegExp('Add ' + toAdd + ' ').test(label), 'button: ' + label + ' expected ' + toAdd);
  });

  await step('adding sends each contact, reports progress, and they arrive on the device', async () => {
    await page.click('#ccAddBtn');
    await page.waitForSelector('#ccProgress:not([hidden])');
    await page.waitForFunction(() => /added/i.test(document.getElementById('ccProgressTitle').textContent) && !document.getElementById('ccProgress').classList.contains('is-running'), null, { timeout: 30000 });
    const n = await page.evaluate(() => window.__fake.contacts.length);
    assert(n === 2 + toAdd, `device holds ${n}, expected ${2 + toAdd}`);
    const v = await page.evaluate((k) => window.__fake.contactView(k), seed.key);
    assert(v.outPathLen === 1, 'the existing contact\'s path was reset');
    await page.click('#ccDoneBtn');
    await page.waitForSelector('#ccProgress', { state: 'hidden' });
  });

  await step('when the area is bigger than the free space, only what fits is offered', async () => {
    const free = await page.evaluate(() => window._companionContacts.state().free);
    assert(free === 100 - (2 + toAdd), 'free ' + free);
    if (free === 0) {
      assert(await page.isDisabled('#ccAddBtn'), 'add should be disabled when full');
    }
  });

  await step('Manage lists the device contacts and draws them on the map', async () => {
    await page.click('#ccTabManage');
    await page.waitForFunction(() => window._companionContacts.state().deviceContacts > 0);
    const s = await page.evaluate(() => window._companionContacts.state());
    assert(s.deviceContacts === 2 + toAdd, 'device contacts ' + s.deviceContacts);
  });

  await step('Manage: picking an area selects the contacts in it, and each says its area', async () => {
    await page.selectOption('#ccMArea', 'FAR');
    const s = await page.evaluate(() => window._companionContacts.state());
    assert(s.toRemove === 1, 'only the far contact is in Far: ' + s.toRemove);
    const row = await page.textContent('#ccRemoveList li');
    assert(/Far Away/.test(row) && /\bFAR\b/.test(row), 'row names the contact and its area: ' + row);
    await page.selectOption('#ccMArea', 'none');
    const none = await page.evaluate(() => window._companionContacts.state());
    assert(none.toRemove >= 1, 'contacts in no area can be picked too');
    await page.selectOption('#ccMArea', '');
  });

  await step('Find stale contacts lists the ones nothing has heard for 30 days', async () => {
    await page.click('#ccFindStale');
    const s = await page.evaluate(() => window._companionContacts.state());
    const names = await page.$$eval('#ccRemoveList li', (lis) => lis.map((li) => li.textContent));
    assert(names.some((t) => /Far Away/.test(t)), 'the far contact (advert in 2023, unknown to CoreScope) is stale: ' + names.join(' | '));
    assert(!names.some((t) => t.indexOf(seed.name) >= 0), 'the seeded contact was heard by CoreScope recently');
    assert(names.every((t) => /last heard/.test(t)), 'each row says when it was last heard');
    assert(s.toRemove === names.length, 'all listed are selected');
    assert(await page.inputValue('#ccMStale') === '30', 'the stale filter is set');
    await page.selectOption('#ccMStale', '0');
  });

  await step('selecting an area of contacts and removing them asks first, offers a backup, then removes', async () => {
    await page.evaluate(([la, lo]) => window._companionContacts.selectCircle(la, lo, 100000), [far.lat, far.lon]);
    const s = await page.evaluate(() => window._companionContacts.state());
    assert(s.toRemove === 1, 'only the far contact should be selected: ' + s.toRemove);
    await page.click('#ccRemoveBtn');
    await page.waitForSelector('#ccConfirm[open]');
    const dl = page.waitForEvent('download');
    await page.click('#ccConfirmGo');
    const file = await dl;
    assert(/companion-contacts.*\.json$/.test(file.suggestedFilename()), 'backup file name ' + file.suggestedFilename());
    await page.waitForFunction(() => !window.__fake.contacts.some((c) => c.key === 'ee'.repeat(32)));
  });

  await step('#/tools/companion-contacts?tab=manage opens on Manage', async () => {
    await page.goto(BASE + '/#/tools/companion-contacts?tab=manage', { waitUntil: 'domcontentloaded' });
    await page.waitForSelector('#ccTabManage[aria-selected="true"]');
  });

  await step('no page errors', async () => { assert(errors.length === 0, errors.join('; ')); });

  const p2 = await (await browser.newContext()).newPage();
  await step('a browser without Web Serial or Bluetooth is told why', async () => {
    await p2.addInitScript(() => {
      Object.defineProperty(navigator, 'serial', { value: undefined, configurable: true });
      Object.defineProperty(navigator, 'bluetooth', { value: undefined, configurable: true });
    });
    await p2.goto(BASE + '/#/tools/companion-contacts', { waitUntil: 'domcontentloaded' });
    await p2.waitForSelector('#ccUnsupported:not([hidden])');
    assert(await p2.isDisabled('#ccConnectUsb'), 'USB connect should be disabled');
  });

  await browser.close();
  console.log(`\n${passed} passed, ${failed} failed`);
  process.exit(failed > 0 ? 1 : 0);
})().catch((e) => { console.error(e); process.exit(1); });
