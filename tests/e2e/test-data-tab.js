/**
 * The Data tab (public/data.js): runs the real page in jsdom against faked
 * /api/data/* answers. Checks that dashboards come from the server and draw
 * their panels from the cube, that $variables fill titles and filters, that
 * a built-in is never edited in place (a copy is), that export, import and
 * share links round-trip, that the layout never overlaps, that a figure's
 * drill-down asks for the panel's filters plus what was clicked, that names
 * off the mesh stay text, and that a CoreScope with the tab off says so.
 *
 * Needs jsdom, so it runs in the E2E job (scripts/non-unit-tests.json,
 * domIntegration), not the unit gate.
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
const plain = (v) => JSON.parse(JSON.stringify(v));

const CONFIG = {
  enabled: true, layer: 'country', home: 'SFO', days: 7, rows: 4, filling: false,
  areas: [{ code: 'SFO', label: 'San Francisco' }, { code: 'SJC', label: '<img src=x onerror=alert(1)>' }],
  windows: [{ key: '24h', label: 'Last 24 hours' }, { key: '7d', label: 'Last 7 days' }],
  groups: [{ key: 'adverts', label: 'Adverts (flood)', types: [4], floodOnly: true }, { key: 'messages', label: 'Messages', types: [2, 5, 6] }, { key: 'requests', label: 'Requests and routing', types: [0, 1, 3, 7, 8, 9, 10, 11] }],
  typeNames: { 2: 'Direct message', 4: 'Advert', 5: 'Channel message' },
  dimensions: ['border', 'origin', 'heard', 'through', 'kind', 'type', 'channel', 'scope', 'sclass', 'how', 'routing']
};
const T0 = 1800000000 - 1800000000 % 600;
const CUBE = {
  window: '24h', bucketSeconds: 600, complete: true,
  buckets: [T0, T0 + 600, T0 + 1200],
  origins: ['SFO', 'SJC'], heard: ['SFO', 'SJC'], types: [4, 5], routes: [1, 2],
  scopes: [{ key: '#be', class: 'scoped' }, { key: 'unscoped', class: 'unscoped' }],
  how: ['relayed', 'across', 'unclear'], through: ['', 'SJC'], channels: ['', 'Public'],
  // [bucket, origin, heard, type, scope, how, route, through, channel, packets]
  rows: [[0, 0, 1, 1, 0, 0, 0, 0, 1, 5], [1, 1, 0, 0, 1, 1, 0, 0, 0, 3], [2, 0, 0, 0, 1, 0, 1, 1, 0, 2]]
};
const lay = (x, y, w, h) => ({ x, y, w, h });
const OVERVIEW = {
  schema: 1, id: 'overview', title: 'Overview', window: '24h', source: 'built-in',
  panels: [
    { id: 'all', title: 'Packets', type: 'stat', query: {}, layout: lay(0, 0, 3, 2) },
    { id: 'who', title: 'Who hears whom', type: 'matrix', query: { rows: 'origin', cols: 'heard' }, layout: lay(3, 0, 6, 4) },
    { id: 'scopes', title: 'Busiest scopes', type: 'top', query: { dim: 'scope', top: 5, filters: [{ d: 'origin', n: 0, v: ['SFO'] }] }, layout: lay(9, 0, 3, 4) },
    { id: 'kinds', title: 'By kind', type: 'bar', query: { x: 'time', split: 'kind' }, layout: lay(0, 4, 12, 4) }
  ]
};
const CROSS = {
  schema: 1, id: 'cross', title: 'Leaving $home', window: '24h', source: 'operator',
  variables: [{ name: 'home', kind: 'area', label: 'Home' }],
  panels: [
    { id: 'leave', title: 'Leaving $home', type: 'stat', query: { filters: [{ d: 'origin', n: 0, v: ['$home'] }, { d: 'border', n: 0, v: ['crossed'] }] }, layout: lay(0, 0, 4, 2) },
    { id: 'note', title: 'About', type: 'text', options: { text: 'Traffic that started in **$home**.' }, query: {}, layout: lay(4, 0, 4, 2) }
  ]
};
const LIBRARY = { default: 'overview', home: 'SFO', dashboards: [OVERVIEW, CROSS] };

async function setup(config, hash, opts) {
  const dom = new JSDOM('<!doctype html><body><main id="app"></main></body>', { url: 'http://localhost/' + (hash || '#/data'), runScripts: 'outside-only', pretendToBeVisual: true });
  const w = dom.window;
  if (opts && opts.stored) w.localStorage.setItem('corescope-data-dashboards', JSON.stringify(opts.stored));
  const pages = {};
  w.registerPage = (name, mod) => { pages[name] = mod; };
  const asked = [];
  w.fetch = async (url) => {
    asked.push(String(url));
    const u = String(url);
    const body = u.indexOf('/api/data/config') === 0 ? config : u.indexOf('/api/data/dashboards') === 0 ? LIBRARY : u.indexOf('/api/data/cube') === 0 ? CUBE : { total: 0, rows: [] };
    return { ok: true, json: async () => plain(body) };
  };
  w.requestAnimationFrame = (fn) => setTimeout(fn, 0);
  w.eval(SRC);
  const app = w.document.getElementById('app');
  return { w, pages, app, asked, d: w.document };
}
const settle = () => new Promise((r) => setTimeout(r, 30));
async function open(hash, opts) {
  const s = await setup(CONFIG, hash, opts);
  s.pages.data.init(s.app); await settle(); await settle(); await settle();
  return s;
}
function panel(d, id) { const e = d.querySelector('.dt-panel[data-id="' + id + '"]'); assert.ok(e, 'panel ' + id + ' is drawn'); return e; }
function byId(d, id) { const e = d.getElementById(id); assert.ok(e, '#' + id + ' is on the page'); return e; }
function one(root, sel) { const e = root.querySelector(sel); assert.ok(e, sel + ' is on the page'); return e; }
function internals(w) {
  const X = w.__corescopeDataInternals;
  for (const k of ['resolveLayout', 'seriesQuery', 'rolledBuckets', 'niceMax', 'b64urlEncode', 'b64urlDecode', 'setConfig']) assert.strictEqual(typeof X[k], 'function', 'the page exposes ' + k);
  return X;
}
const stored = (w) => JSON.parse(w.localStorage.getItem('corescope-data-dashboards') || '[]');

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

  await test('opens the default dashboard and draws every panel from the cube', async () => {
    const { d, asked } = await open();
    assert.ok(asked.indexOf('/api/data/dashboards') >= 0, asked.join(' '));
    assert.strictEqual(d.querySelectorAll('.dt-panel').length, 4);
    assert.strictEqual(one(panel(d, 'all'), '.dt-stat-v').textContent, '10');
    const cells = [...panel(d, 'who').querySelectorAll('td')].map((t) => t.textContent);
    assert.ok(cells.indexOf('5') >= 0 && cells.indexOf('3') >= 0, 'matrix cells: ' + cells.join(','));
    // The top list honours its own filter: only packets from SFO (5 + 2).
    const top = [...panel(d, 'scopes').querySelectorAll('.dt-top-row')].map((r) => r.textContent);
    assert.deepStrictEqual(top, ['#be5', 'unscoped2']);
    assert.strictEqual(panel(d, 'kinds').querySelectorAll('svg').length, 1, 'bar chart drawn');
    assert.strictEqual(panel(d, 'who').style.gridColumn, '4 / span 6', 'placed by its layout');
  });

  await test('the picker groups dashboards by where they come from', async () => {
    const { d } = await open('#/data', { stored: [{ schema: 1, id: 'mine-1', title: 'Mine', panels: [] }] });
    const groups = [...d.querySelectorAll('#dtDash optgroup')].map((g) => g.label + ':' + [...g.children].map((o) => o.value).join(','));
    assert.deepStrictEqual(groups, ['Built-in:overview', 'From this site:cross', 'Your dashboards:mine-1']);
  });

  await test('$variables fill titles, text and filters, and follow the bar', async () => {
    const { d, w } = await open('#/data?dash=cross');
    assert.strictEqual(panel(d, 'leave').querySelector('h3').textContent, 'Leaving San Francisco');
    // Started in SFO and heard elsewhere: the 5-packet row only.
    assert.strictEqual(one(panel(d, 'leave'), '.dt-stat-v').textContent, '5');
    assert.ok(/started in San Francisco/.test(panel(d, 'note').textContent));
    const sel = byId(d, 'dtVar-home');
    sel.value = 'SJC'; sel.dispatchEvent(new w.Event('change'));
    await settle(); await settle();
    assert.strictEqual(one(panel(d, 'leave'), '.dt-stat-v').textContent, '3');
    assert.ok(/v\.home=SJC/.test(w.location.hash), w.location.hash);
  });

  await test('area names from the areas file stay text', async () => {
    const { d } = await open();
    assert.strictEqual(d.querySelectorAll('#app img').length, 0);
    assert.ok(panel(d, 'who').textContent.indexOf('<img src=x') >= 0);
  });

  await test('editing a built-in edits a copy in this browser, never the original', async () => {
    const { d, w } = await open();
    byId(d, 'dtEdit').click(); await settle();
    const list = stored(w);
    assert.strictEqual(list.length, 1);
    assert.strictEqual(list[0].title, 'Overview (my copy)');
    assert.ok(!('source' in list[0]), 'the source is not saved');
    assert.ok(!byId(d, 'dtRail').hidden, 'the editor is open');
    // Change the copy's title from the rail; the built-in keeps its own.
    const t = byId(d, 'dtDashTitle');
    t.value = 'Ours'; t.dispatchEvent(new w.Event('change')); await settle();
    assert.strictEqual(stored(w)[0].title, 'Ours');
    assert.strictEqual(LIBRARY.dashboards[0].title, 'Overview');
    const opts = [...d.querySelectorAll('#dtDash option')].map((o) => o.textContent);
    assert.ok(opts.indexOf('Overview') >= 0 && opts.indexOf('Ours') >= 0, opts.join(' / '));
  });

  await test('undo steps back one change', async () => {
    const { d, w } = await open();
    byId(d, 'dtEdit').click(); await settle();
    const t = byId(d, 'dtDashTitle');
    t.value = 'First'; t.dispatchEvent(new w.Event('change')); await settle();
    byId(d, 'dtUndo').click(); await settle();
    assert.strictEqual(stored(w)[0].title, 'Overview (my copy)');
  });

  await test('a new panel lands below the others and the layout never overlaps', async () => {
    const { d, w } = await open();
    byId(d, 'dtEdit').click(); await settle();
    one(d, '.dt-addtile').click(); await settle();
    [...d.querySelectorAll('.dt-types button')].find((b) => /Stat/.test(b.textContent)).click(); await settle();
    const p = stored(w)[0].panels;
    assert.strictEqual(p.length, 5);
    assert.strictEqual(p[4].layout.y, 8, 'under the 4-row chart that starts at row 4');
    const X = internals(w);
    // Drop a wide panel on top of two others: it stays, they move down.
    const ps = plain(OVERVIEW.panels);
    ps[3].layout = lay(0, 0, 12, 2);
    X.resolveLayout(ps, ps[3]);
    for (let i = 0; i < ps.length; i++) for (let j = i + 1; j < ps.length; j++) {
      const a = ps[i].layout, b = ps[j].layout;
      assert.ok(!(a.x < b.x + b.w && b.x < a.x + a.w && a.y < b.y + b.h && b.y < a.y + a.h), ps[i].id + ' overlaps ' + ps[j].id);
    }
    assert.deepStrictEqual(plain(ps[3].layout), lay(0, 0, 12, 2));
    assert.strictEqual(ps[0].layout.y, 2, 'pushed down, then lifted to the first free row');
  });

  await test('the editor turns a panel into another type and back', async () => {
    const { d, w } = await open();
    byId(d, 'dtEdit').click(); await settle();
    panel(d, 'kinds').click(); await settle();
    const type = byId(d, 'dtPType');
    type.value = 'line'; type.dispatchEvent(new w.Event('change')); await settle();
    assert.strictEqual(stored(w)[0].panels[3].type, 'line');
    assert.ok(panel(d, 'kinds').classList.contains('dt-type-line'));
  });

  await test('import checks the file before anything changes', async () => {
    const { d, w } = await open();
    byId(d, 'dtImport').click();
    const ta = one(d, '.dt-code'), ok = [...d.querySelectorAll('.dt-acts button')].find((b) => b.textContent === 'Import');
    ta.value = JSON.stringify({ schema: 2, id: 'x', title: 'X', panels: [] }); ok.click();
    assert.ok(/schema 2/.test(one(d, '.dt-err').textContent));
    ta.value = JSON.stringify({ schema: 1, id: 'x', title: 'X', panels: [{ id: 'a', title: 'A', type: 'stat', query: {}, layout: lay(8, 0, 6, 2) }] }); ok.click();
    assert.ok(/12-column grid/.test(one(d, '.dt-err').textContent));
    ta.value = JSON.stringify({ schema: 1, id: 'x', title: 'X', panels: [{ id: 'a', title: 'A', type: 'bar', query: { split: 'colour' }, layout: lay(0, 0, 6, 2) }] }); ok.click();
    assert.ok(/Unknown dimension "colour"/.test(one(d, '.dt-err').textContent));
    assert.strictEqual(stored(w).length, 0, 'nothing saved yet');
    // An id already taken is renamed, not overwritten.
    ta.value = JSON.stringify({ schema: 1, id: 'overview', title: 'Imported', panels: [] }); ok.click();
    await settle();
    assert.deepStrictEqual(stored(w).map((x) => x.id), ['overview-2']);
    assert.strictEqual(byId(d, 'dtDTitle').textContent, 'Imported');
  });

  await test('a share link carries the whole layout and opens without an account', async () => {
    const a = await open();
    const X = internals(a.w);
    const doc = { schema: 1, id: 'scot', title: 'Shared one', panels: [{ id: 'n', title: 'Count', type: 'stat', query: {}, layout: lay(0, 0, 3, 2) }] };
    assert.strictEqual(X.b64urlDecode(X.b64urlEncode(JSON.stringify(doc))), JSON.stringify(doc));
    const b = await open('#/data?d=' + X.b64urlEncode(JSON.stringify(doc)));
    assert.strictEqual(byId(b.d, 'dtDTitle').textContent, 'Shared one');
    assert.strictEqual(one(panel(b.d, 'n'), '.dt-stat-v').textContent, '10');
    assert.strictEqual(byId(b.d, 'dtEdit').textContent, 'Edit a copy');
    assert.strictEqual(stored(b.w).length, 0, 'opening a link stores nothing');
    const bad = await open('#/data?d=not-base64-json');
    assert.strictEqual(byId(bad.d, 'dtDTitle').textContent, 'Overview', 'a damaged link falls back to the default');
  });

  await test('export gives the document without its source', async () => {
    const { d } = await open('#/data?dash=cross');
    byId(d, 'dtExport').click();
    const doc = JSON.parse(one(d, '.dt-code').value);
    assert.strictEqual(doc.id, 'cross');
    assert.ok(!('source' in doc));
    assert.strictEqual(doc.variables[0].name, 'home');
  });

  await test('a figure lists its packets with the panel filters plus what was clicked', async () => {
    const { d, asked } = await open();
    one(panel(d, 'scopes'), '.dt-top-row').click(); await settle();
    const u = asked.filter((x) => x.indexOf('/api/data/packets') === 0).pop();
    assert.ok(u, 'asked for packets');
    const q = new URLSearchParams(u.split('?')[1]);
    assert.strictEqual(q.get('window'), '24h');
    assert.deepStrictEqual(JSON.parse(q.get('f')), [{ d: 'origin', n: 0, v: ['SFO'] }, { d: 'scope', n: 0, v: ['#be'], x: 0 }]);
    assert.ok(/Busiest scopes: #be/.test(byId(d, 'dtModalTitle').textContent));
  });

  await test('"Everything else" is every value not shown on its own', async () => {
    const { w } = await setup(CONFIG);
    const X = internals(w);
    X.setConfig(plain(CONFIG));
    const c = plain(CUBE);
    // Eight scopes, one row each: six on their own, two folded into the rest.
    c.scopes = 'abcdefgh'.split('').map((k) => ({ key: '#' + k, class: 'scoped' }));
    c.rows = c.scopes.map((_, i) => [0, 0, 0, 0, i, 0, 0, 0, 0, 10 - i]);
    const res = X.seriesQuery(c, c.rows, { x: 'time', split: 'scope' }, '24h');
    assert.strictEqual(res.series.length, 7);
    const rest = res.series[6];
    assert.strictEqual(rest.key, '__rest');
    assert.strictEqual(rest.total, 3 + 4);
    assert.deepStrictEqual(plain(rest.others), ['#a', '#b', '#c', '#d', '#e', '#f']);
  });

  await test('weeks roll up to the Monday they start on (UTC)', async () => {
    const { w } = await setup(CONFIG);
    const X = internals(w);
    // 2026-10-08 is a Thursday; its week starts Monday 2026-10-05.
    const thu = Date.UTC(2026, 9, 8, 12) / 1000, sun = Date.UTC(2026, 9, 11, 23) / 1000, mon = Date.UTC(2026, 9, 12, 1) / 1000;
    const rb = X.rolledBuckets({ buckets: [thu, sun, mon] }, 604800);
    assert.strictEqual(JSON.stringify(rb.keys), JSON.stringify([Date.UTC(2026, 9, 5) / 1000, Date.UTC(2026, 9, 12) / 1000]));
    assert.strictEqual(JSON.stringify(rb.map), JSON.stringify([0, 0, 1]));
    assert.strictEqual(X.niceMax(3), 4);
    assert.strictEqual(X.niceMax(130), 200);
  });

  await test('a flooded advert is an advert, a direct one is its own kind', async () => {
    const { w } = await setup(CONFIG);
    const X = internals(w);
    X.setConfig(plain(CONFIG));
    const c = { types: [4, 5], routes: [1, 2] };
    assert.strictEqual(X.DIMS.kind.of(c, [0, 0, 0, 0, 0, 0, 0]), 'adverts');
    assert.strictEqual(X.DIMS.kind.of(c, [0, 0, 0, 0, 0, 0, 1]), 'direct-advert');
    assert.strictEqual(X.DIMS.kind.of(c, [0, 0, 0, 1, 0, 0, 1]), 'messages');
    assert.strictEqual(X.DIMS.border.of({ origins: ['SFO', 'unknown'], heard: ['SFO', 'SJC'] }, [0, 0, 1]), 'crossed');
    assert.strictEqual(X.DIMS.border.of({ origins: ['SFO', 'unknown'], heard: ['SFO', 'SJC'] }, [0, 1, 0]), 'unknown');
  });

  await test('the period comes from the address, and a bad one falls back', async () => {
    const a = await open('#/data?w=7d');
    assert.ok(a.asked.indexOf('/api/data/cube?window=7d') >= 0, a.asked.join(' '));
    assert.strictEqual(one(a.d, '#dtWindows button[aria-pressed="true"]').textContent, 'Last 7 days');
    const b = await open('#/data?w=30d');
    assert.ok(b.asked.indexOf('/api/data/cube?window=24h') >= 0, b.asked.join(' '));
  });

  await test('without areas the editor offers no place dimensions and refuses dashboards that need them', async () => {
    const placeless = plain(CONFIG);
    placeless.places = false; placeless.home = ''; placeless.areas = [];
    placeless.dimensions = ['kind', 'type', 'channel', 'scope', 'sclass', 'routing'];
    const s = await setup(placeless, '#/data?dash=mine-1', { stored: [{ schema: 1, id: 'mine-1', title: 'Mine', panels: [{ id: 'k', title: 'K', type: 'bar', query: { x: 'time', split: 'kind' }, layout: lay(0, 0, 6, 4) }] }] });
    s.pages.data.init(s.app); await settle(); await settle(); await settle();
    const d = s.d;
    byId(d, 'dtEdit').click(); await settle();
    panel(d, 'k').click(); await settle();
    const split = [...byId(d, 'dtPSplit').options].map((o) => o.value);
    for (const dim of ['origin', 'heard', 'through', 'border', 'how']) assert.ok(split.indexOf(dim) < 0, 'split offers ' + dim);
    assert.ok(split.indexOf('scope') >= 0, 'split still offers scope');
    // A new matrix starts on dimensions that exist.
    byId(d, 'dtEdit').click(); await settle();
    byId(d, 'dtEdit').click(); await settle();
    one(d, '.dt-addtile').click(); await settle();
    [...d.querySelectorAll('.dt-types button')].find((b) => /Matrix/.test(b.textContent)).click(); await settle();
    const m = stored(s.w)[0].panels.find((p) => p.type === 'matrix');
    assert.ok(m && ['origin', 'heard'].indexOf(m.query.rows) < 0 && ['origin', 'heard'].indexOf(m.query.cols) < 0, 'matrix on ' + JSON.stringify(m && m.query));
    // Dashboard settings offer no area variables.
    s.d.dispatchEvent(new s.w.KeyboardEvent('keydown', { key: 'Escape' })); await settle();
    const kinds = [...byId(d, 'dtVarKind').options].map((o) => o.value);
    assert.ok(kinds.indexOf('area') < 0, 'variable kinds: ' + kinds.join(','));
    // Importing one that needs places says why.
    byId(d, 'dtImport').click();
    one(d, '.dt-code').value = JSON.stringify({ schema: 1, id: 'p', title: 'P', panels: [{ id: 'a', title: 'A', type: 'top', query: { dim: 'heard' }, layout: lay(0, 0, 4, 4) }] });
    [...d.querySelectorAll('.dt-acts button')].find((b) => b.textContent === 'Import').click();
    assert.ok(/needs areas/.test(one(d, '.dt-err').textContent), one(d, '.dt-err').textContent);
  });

  await test('built-in dashboards without areas draw without place panels', async () => {
    const placeless = plain(CONFIG);
    placeless.places = false; placeless.home = ''; placeless.areas = [];
    placeless.dimensions = ['kind', 'type', 'channel', 'scope', 'sclass', 'routing'];
    const s = await setup(placeless, '#/data');
    s.pages.data.init(s.app); await settle(); await settle(); await settle();
    // The server sends the library; a shared or stored one may still name places.
    // Here the default (OVERVIEW) has a matrix on origin x heard: it says it needs areas rather than drawing nonsense.
    assert.ok(/needs areas/.test(panel(s.d, 'who').textContent), panel(s.d, 'who').textContent);
    assert.strictEqual(one(panel(s.d, 'all'), '.dt-stat-v').textContent, '10');
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
