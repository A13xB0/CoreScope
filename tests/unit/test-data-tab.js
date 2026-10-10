/**
 * The Data tab (public/data.js): runs the real page in jsdom against faked
 * /api/data/* answers. Checks the pieces whose answers must agree with the
 * server (time roll-up, drill-down filters, packet kinds), that the page
 * draws its matrix and chart from a cube, that names off the mesh stay text,
 * and that a CoreScope with the tab off says so instead of breaking.
 */
'use strict';
const REPO_ROOT = require('path').resolve(__dirname, '..', '..');
const fs = require('fs');
const assert = require('assert');
const { JSDOM } = require('jsdom');

const SRC = fs.readFileSync(REPO_ROOT + '/public/data.js', 'utf8');
let passed = 0, failed = 0;
async function test(name, fn) {
  try { await fn(); passed++; console.log('  ✓ ' + name); }
  catch (e) { failed++; console.error('  ✗ ' + name + ': ' + e.message); }
}

const CONFIG = {
  enabled: true, layer: 'country', home: 'SCO', days: 7, rows: 4, filling: false,
  areas: [{ code: 'SCO', label: 'Scotland' }, { code: 'IOI', label: '<img src=x onerror=alert(1)>' }],
  windows: [{ key: '24h', label: 'Last 24 hours' }, { key: '7d', label: 'Last 7 days' }],
  groups: [{ key: 'adverts', label: 'Adverts (flood)', types: [4], floodOnly: true }, { key: 'messages', label: 'Messages', types: [2, 5, 6] }, { key: 'requests', label: 'Requests and routing', types: [0, 1, 3, 7, 8, 9, 10, 11] }],
  typeNames: { 2: 'Direct message', 4: 'Advert', 5: 'Channel message' },
  dimensions: ['border', 'origin', 'heard', 'through', 'kind', 'type', 'channel', 'scope', 'sclass', 'how', 'routing']
};
const T0 = 1800000000 - 1800000000 % 600;
const CUBE = {
  window: '24h', bucketSeconds: 600, complete: true,
  buckets: [T0, T0 + 600, T0 + 1200],
  origins: ['SCO', 'IOI'], heard: ['SCO', 'IOI'], types: [4, 5], routes: [1, 2],
  scopes: [{ key: '#sco', class: 'scoped' }, { key: 'unscoped', class: 'unscoped' }],
  how: ['relayed', 'across', 'unclear'], through: ['', 'IOI'], channels: ['', 'Public'],
  // [bucket, origin, heard, type, scope, how, route, through, channel, packets]
  rows: [[0, 0, 1, 1, 0, 0, 0, 0, 1, 5], [1, 1, 0, 0, 1, 1, 0, 0, 0, 3], [2, 0, 0, 0, 1, 0, 1, 1, 0, 2]]
};
const SUMMARY = {
  window: '24h', label: 'Last 24 hours', complete: true, packets: 10, crossBorder: 8, bases: { 'first-relay': 10 },
  areas: [{ code: 'SCO', observed: true, originated: 7 }, { code: 'IOI', observed: true, originated: 3 }],
  matrix: [{ origin: 'SCO', heard: 'IOI', packets: 5, relayed: 5, across: 0 }, { origin: 'IOI', heard: 'SCO', packets: 3, relayed: 0, across: 3 }, { origin: 'SCO', heard: 'SCO', packets: 2, relayed: 2, across: 0 }],
  matrixByGroup: { adverts: [], messages: [], requests: [] }, flows: []
};

async function setup(config, hash) {
  const dom = new JSDOM('<!doctype html><body><main id="app"></main></body>', { url: 'http://localhost/' + (hash || '#/data'), runScripts: 'outside-only', pretendToBeVisual: true });
  const w = dom.window;
  const pages = {};
  w.registerPage = (name, mod) => { pages[name] = mod; };
  const asked = [];
  w.fetch = async (url) => {
    asked.push(String(url));
    const u = String(url);
    const body = u.indexOf('/api/data/config') === 0 ? config : u.indexOf('/api/data/summary') === 0 ? SUMMARY : u.indexOf('/api/data/cube') === 0 ? CUBE : { total: 0, rows: [] };
    return { ok: true, json: async () => JSON.parse(JSON.stringify(body)) };
  };
  w.requestAnimationFrame = (fn) => setTimeout(fn, 0);
  w.eval(SRC);
  const app = w.document.getElementById('app');
  return { w, pages, app, asked };
}
const settle = () => new Promise((r) => setTimeout(r, 30));

(async () => {
  console.log('Data tab');

  await test('the page registers as "data"', async () => {
    const { pages } = await setup(CONFIG);
    assert.ok(pages.data && typeof pages.data.init === 'function' && typeof pages.data.destroy === 'function');
  });

  await test('with the tab off it says so and asks for nothing else', async () => {
    const { pages, app, asked } = await setup({ enabled: false });
    pages.data.init(app); await settle();
    assert.ok(/not switched on/.test(app.textContent), app.textContent);
    assert.deepStrictEqual(asked, ['/api/data/config']);
  });

  await test('draws the matrix, tiles and chart from the summary and cube', async () => {
    const { pages, app, w } = await setup(CONFIG);
    pages.data.init(app); await settle(); await settle();
    const d = w.document;
    assert.ok(d.getElementById('dtPage'), 'workspace rendered');
    const cells = [...d.querySelectorAll('#dtMatrix td')].map((t) => t.textContent);
    assert.ok(cells.indexOf('5') >= 0 && cells.indexOf('3') >= 0, 'matrix cells: ' + cells.join(','));
    const pre = [...d.querySelectorAll('#dtFilters select.dt-preset option')].map((o) => o.textContent);
    assert.ok(pre.indexOf('Leaving Scotland') >= 0, 'home presets offered among the filters: ' + pre.join(' / '));
    assert.ok(!d.getElementById('dtPresets'), 'presets are part of the filters, not a section of their own');
    assert.ok(d.querySelectorAll('#dtChart svg').length === 1, 'one chart drawn');
    assert.ok(/packets heard/.test(d.getElementById('dtSum').textContent));
  });

  await test('area names from the areas file stay text', async () => {
    const { pages, app, w } = await setup(CONFIG);
    pages.data.init(app); await settle(); await settle();
    assert.strictEqual(w.document.querySelectorAll('#app img').length, 0);
    assert.ok(w.document.getElementById('dtMatrix').textContent.indexOf('<img src=x') >= 0);
  });

  await test('weeks roll up to the Monday they start on (UTC)', async () => {
    const { w } = await setup(CONFIG);
    const X = w.__corescopeDataInternals;
    // 2026-10-08 is a Thursday; its week starts Monday 2026-10-05.
    const thu = Date.UTC(2026, 9, 8, 12) / 1000, sun = Date.UTC(2026, 9, 11, 23) / 1000, mon = Date.UTC(2026, 9, 12, 1) / 1000;
    const rb = X.rolledBuckets({ buckets: [thu, sun, mon] }, 604800);
    // Arrays from the page's own realm: compare their contents.
    assert.strictEqual(JSON.stringify(rb.keys), JSON.stringify([Date.UTC(2026, 9, 5) / 1000, Date.UTC(2026, 9, 12) / 1000]));
    assert.strictEqual(JSON.stringify(rb.map), JSON.stringify([0, 0, 1]));
    assert.strictEqual(X.niceMax(3), 4);
    assert.strictEqual(X.niceMax(130), 200);
  });

  await test('a flooded advert is an advert, a direct one is its own kind', async () => {
    const { w } = await setup(CONFIG);
    const X = w.__corescopeDataInternals;
    X.setState(JSON.parse(JSON.stringify(CONFIG)), {});
    const c = { types: [4, 5], routes: [1, 2] };
    assert.strictEqual(X.DIMS.kind.of(c, [0, 0, 0, 0, 0, 0, 0]), 'adverts');
    assert.strictEqual(X.DIMS.kind.of(c, [0, 0, 0, 0, 0, 0, 1]), 'direct-advert');
    assert.strictEqual(X.DIMS.kind.of(c, [0, 0, 0, 1, 0, 0, 1]), 'messages');
    assert.strictEqual(X.DIMS.border.of({ origins: ['SCO', 'unknown'], heard: ['SCO', 'IOI'] }, [0, 0, 1]), 'crossed');
    assert.strictEqual(X.DIMS.border.of({ origins: ['SCO', 'unknown'], heard: ['SCO', 'IOI'] }, [0, 1, 0]), 'unknown');
  });

  await test('a click becomes the page filters plus what was clicked', async () => {
    const { w } = await setup(CONFIG);
    const X = w.__corescopeDataInternals;
    X.setState(JSON.parse(JSON.stringify(CONFIG)), { f: [{ d: 'origin', n: 0, v: ['SCO'] }], by: 'through', panel: 'heard', x: 'time', pick: [] });
    const res = { panelValue: 'IOI', series: [{ key: 'IOI' }, { key: 'straight' }, { key: '__rest' }] };
    assert.deepStrictEqual(JSON.parse(JSON.stringify(X.drillFilters({ res: res, series: res.series[0] }))), [
      { d: 'origin', n: 0, v: ['SCO'] }, { d: 'heard', n: 0, v: ['IOI'], x: 0 }, { d: 'through', n: 0, v: ['IOI'], x: 1 }]);
    // "Everything else" is every value not shown on its own.
    const rest = JSON.parse(JSON.stringify(X.drillFilters({ res: res, series: res.series[2] })));
    assert.deepStrictEqual(rest[2], { d: 'through', n: 1, v: ['IOI', 'straight'], x: 1 });
  });

  await test('the period comes from the address, and a bad one falls back', async () => {
    const { pages, app, w, asked } = await setup(CONFIG, '#/data?w=7d');
    pages.data.init(app); await settle(); await settle();
    assert.ok(asked.some((u) => u === '/api/data/cube?window=7d'), asked.join(' '));
    const b = await setup(CONFIG, '#/data?w=30d');
    b.pages.data.init(b.app); await settle(); await settle();
    assert.ok(b.asked.some((u) => u === '/api/data/cube?window=24h'), b.asked.join(' '));
    assert.ok(w.document.querySelector('#dtWindows button[aria-pressed="true"]').textContent === 'Last 7 days');
  });

  await test('index.html loads the page and its styles, and the nav lists it', async () => {
    const html = fs.readFileSync(REPO_ROOT + '/public/index.html', 'utf8');
    assert.ok(/<script src="data\.js\?v=__BUST__"/.test(html), 'data.js script');
    assert.ok(/<link rel="stylesheet" href="data\.css\?v=__BUST__">/.test(html), 'data.css link');
    assert.ok(/data-route="data"/.test(html), 'top nav link');
    for (const f of ['nav-drawer.js', 'bottom-nav.js']) {
      assert.ok(/route: 'data'/.test(fs.readFileSync(REPO_ROOT + '/public/' + f, 'utf8')), f + ' lists the route');
    }
  });

  console.log(`\n${passed} passed, ${failed} failed`);
  if (failed) process.exit(1);
})();
