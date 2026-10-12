/**
 * Path watch on the Data tab (public/data.js): the "watch" and "watchlist"
 * panels, the repeater variable, and adding a watch. Runs the real page in
 * jsdom against faked /api/data/* and /api/nodes answers, like
 * test-data-tab.js.
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
const K = (c) => c.repeat(64);
const GOYLE = K('a'), INSH = K('d'), KEIR = K('b'), WARD = K('c'), EVIL = K('e');

const CONFIG = {
  enabled: true, layer: 'country', home: 'SFO', days: 7, rows: 4, filling: false,
  areas: [{ code: 'SFO', label: 'San Francisco' }],
  windows: [{ key: '24h', label: 'Last 24 hours' }, { key: '7d', label: 'Last 7 days' }],
  groups: [], typeNames: {}, dimensions: ['border', 'origin', 'heard', 'through', 'kind', 'type', 'channel', 'scope', 'sclass', 'how', 'routing']
};
const CUBE = { window: '7d', bucketSeconds: 3600, complete: true, buckets: [], origins: [], heard: [], types: [], routes: [], scopes: [], how: [], through: [], channels: [], rows: [] };
const lay = (x, y, w, h) => ({ x, y, w, h });
const PATH_DASH = {
  schema: 1, id: 'path', title: 'Path between two repeaters', window: '7d', source: 'built-in',
  variables: [{ name: 'from', kind: 'repeater', label: 'From' }, { name: 'to', kind: 'repeater', label: 'To' }],
  panels: [{ id: 'watch', title: '$from to $to', type: 'watch', query: { from: '$from', to: '$to' }, layout: lay(0, 0, 12, 6) }]
};
const LINKS = {
  schema: 1, id: 'links', title: 'Links we watch', window: '7d', source: 'operator',
  panels: [{ id: 'list', title: 'Watches', type: 'watchlist', query: {}, options: { watches: [
    { name: 'North link', from: GOYLE, to: INSH, both: true },
    { name: 'Quiet one', from: WARD, to: INSH }
  ] }, layout: lay(0, 0, 12, 5) }]
};
const LIBRARY = { default: 'links', home: 'SFO', dashboards: [PATH_DASH, LINKS] };

const day = (i) => new Date(Date.UTC(2026, 9, 5 + i, 12)).toISOString();
function pathResp(from, to, share, flag) {
  const shares = [0.86, 0.88, 0.85, 0.87, 0.86, 0.84, share];
  return {
    from: { key: from, name: from === GOYLE ? 'Goyle Hill' : from === WARD ? 'Ward Hill' : 'Insheswood' },
    to: { key: to, name: to === INSH ? 'Insheswood' : 'Goyle Hill' },
    window: '7d', basis: 'all',
    days: shares.map((s, i) => ({ day: day(i), passed: 100, reached: Math.round(s * 100), share: s })),
    share, usual: 0.86, low: 0.76, high: 0.96, verdict: share < 0.76 ? 'worse' : 'normal',
    hops: { median: 2, min: 1, max: 4 },
    routes: [{ via: [{ key: KEIR, name: 'Keir Hill' }], share: 0.71, usual: 0.9 }, { via: [{ key: EVIL, name: '<img src=x onerror=alert(1)>' }], share: 0.2, usual: 0.05 }],
    changes: flag ? [flag] : [],
    flag: flag || undefined
  };
}
const QUIET = { day: day(6), kind: 'repeater-quiet', node: { key: KEIR, name: 'Keir Hill' }, was: 0.71, now: 0, shareWas: 0.86, shareNow: 0.3, text: 'Keir Hill dropped out of this path (it carried 71%) and has gone quiet everywhere.' };
const REPEATERS = { nodes: [{ public_key: GOYLE, name: 'Goyle Hill', role: 'repeater' }, { public_key: INSH, name: 'Insheswood', role: 'repeater' }, { public_key: WARD, name: 'Ward Hill', role: 'repeater' }, { public_key: K('f'), name: 'A companion', role: 'companion' }] };

async function setup(hash, opts) {
  const dom = new JSDOM('<!doctype html><body><main id="app"></main></body>', { url: 'http://localhost/' + (hash || '#/data'), runScripts: 'outside-only', pretendToBeVisual: true });
  const w = dom.window;
  if (opts && opts.stored) w.localStorage.setItem('corescope-data-dashboards', JSON.stringify(opts.stored));
  const pages = {};
  w.registerPage = (name, mod) => { pages[name] = mod; };
  const asked = [], posted = [];
  w.fetch = async (url, init) => {
    const u = String(url);
    asked.push(u);
    let body;
    if (u.indexOf('/api/data/config') === 0) body = CONFIG;
    else if (u.indexOf('/api/data/dashboards') === 0) body = LIBRARY;
    else if (u.indexOf('/api/data/cube') === 0) body = CUBE;
    else if (u.indexOf('/api/nodes') === 0) body = REPEATERS;
    else if (u.indexOf('/api/data/paths') === 0) {
      const req = JSON.parse(init.body);
      posted.push(req);
      body = { paths: req.pairs.map((p) => pathResp(p.from, p.to, p.from === WARD ? 0.3 : 0.84, p.from === WARD ? QUIET : null)) };
    } else if (u.indexOf('/api/data/path?') === 0) {
      const q = new URLSearchParams(u.split('?')[1]);
      body = (opts && opts.emptyPath) ? Object.assign(pathResp(q.get('from'), q.get('to'), 0), { days: [], verdict: '', routes: [] }) : pathResp(q.get('from'), q.get('to'), 0.3, QUIET);
    } else body = { total: 0, rows: [] };
    return { ok: true, json: async () => plain(body) };
  };
  w.requestAnimationFrame = (fn) => setTimeout(fn, 0);
  w.eval(SRC);
  const app = w.document.getElementById('app');
  pages.data.init(app);
  for (let i = 0; i < 6; i++) await settle();
  return { w, app, asked, posted, d: w.document };
}
const settle = () => new Promise((r) => setTimeout(r, 30));
function panel(d, id) { const e = d.querySelector('.dt-panel[data-id="' + id + '"]'); assert.ok(e, 'panel ' + id + ' is drawn'); return e; }
const stored = (w) => JSON.parse(w.localStorage.getItem('corescope-data-dashboards') || '[]');

(async () => {
  console.log('Data tab: path watch');

  await test('a watch panel follows the two repeaters picked above and shows share, range, routes and the flag', async () => {
    const { d, asked } = await setup('#/data?dash=path&v.from=' + GOYLE + '&v.to=' + INSH);
    const q = asked.filter((u) => u.indexOf('/api/data/path?') === 0)[0];
    assert.ok(q && q.indexOf('from=' + GOYLE) > 0 && q.indexOf('to=' + INSH) > 0 && /window=7d/.test(q), 'asked ' + q);
    const p = panel(d, 'watch');
    assert.strictEqual(p.querySelector('h3').textContent, 'Goyle Hill to Insheswood', 'title fills repeater names');
    const t = p.textContent.replace(/\s+/g, ' ');
    assert.ok(/30%/.test(t) && /usually 86%/.test(t) && /76–96%/.test(t), 'share: ' + t);
    assert.ok(/Keir Hill/.test(t) && /71%/.test(t), 'routes: ' + t);
    assert.ok(/Repeater quiet/.test(t) && /dropped out of this path/.test(t), 'flag: ' + t);
    assert.strictEqual(p.querySelectorAll('.dt-watch-bars rect title').length, 7, 'a dated bar per day');
  });

  await test('a watch with no repeaters picked says to pick them and asks nothing', async () => {
    const { d, asked } = await setup('#/data?dash=path');
    assert.ok(/Pick two repeaters/.test(panel(d, 'watch').textContent));
    assert.ok(!asked.some((u) => u.indexOf('/api/data/path') === 0), asked.join(' '));
  });

  await test('the repeater picker lists repeaters by name', async () => {
    const { d } = await setup('#/data?dash=path');
    const opts = [...d.querySelectorAll('#dtVar-from option')].map((o) => o.textContent);
    assert.ok(opts.some((o) => /^Goyle Hill/.test(o)) && !opts.some((o) => /companion/.test(o)), opts.join(' | '));
  });

  await test('a watch list asks for all its pairs at once, one line each, and opens a watch on click', async () => {
    const { d, posted, asked } = await setup('#/data?dash=links');
    assert.strictEqual(posted.length, 1, 'one request');
    assert.strictEqual(posted[0].pairs.length, 3, 'both ways for the North link, one for the other');
    assert.ok(!asked.some((u) => u.indexOf('/api/data/cube') === 0), 'no cube needed');
    const rows = [...panel(d, 'list').querySelectorAll('.dt-wl-row')];
    assert.strictEqual(rows.length, 2);
    const quiet = rows[1].textContent.replace(/\s+/g, ' ');
    assert.ok(/Quiet one/.test(quiet) && /30%/.test(quiet) && /Repeater quiet/.test(quiet), 'row: ' + quiet);
    rows[1].click();
    for (let i = 0; i < 4; i++) await settle();
    const dlg = d.getElementById('dtModal');
    assert.ok(dlg && dlg.hasAttribute('open') && /dropped out of this path/.test(dlg.textContent), 'detail opened');
  });

  await test('adding a watch to a site dashboard saves it to your copy', async () => {
    const { d, w } = await setup('#/data?dash=links');
    panel(d, 'list').querySelector('.dt-wl-add').click();
    for (let i = 0; i < 4; i++) await settle();
    const from = d.getElementById('dtWFrom'), to = d.getElementById('dtWTo'), name = d.getElementById('dtWName');
    from.value = 'Ward Hill · ' + WARD.slice(0, 8); to.value = 'Goyle Hill · ' + GOYLE.slice(0, 8); name.value = 'Back link';
    d.getElementById('dtWAdd').click();
    for (let i = 0; i < 6; i++) await settle();
    const mine = stored(w);
    assert.strictEqual(mine.length, 1, 'a copy was made');
    const ws = mine[0].panels[0].options.watches;
    assert.deepStrictEqual(ws[ws.length - 1], { name: 'Back link', from: WARD, to: GOYLE, both: false });
    assert.strictEqual(LINKS.panels[0].options.watches.length, 2, 'the site dashboard is untouched');
  });

  await test('a pair nothing has passed between asks before it is added', async () => {
    const { d, w } = await setup('#/data?dash=links', { emptyPath: true });
    panel(d, 'list').querySelector('.dt-wl-add').click();
    for (let i = 0; i < 4; i++) await settle();
    d.getElementById('dtWFrom').value = 'Ward Hill · ' + WARD.slice(0, 8);
    d.getElementById('dtWTo').value = 'Goyle Hill · ' + GOYLE.slice(0, 8);
    d.getElementById('dtWName').value = 'Nothing here';
    d.getElementById('dtWAdd').click();
    for (let i = 0; i < 4; i++) await settle();
    assert.ok(/Nothing has passed/.test(d.getElementById('dtModal').textContent), 'warned');
    assert.strictEqual(stored(w).length, 0, 'not added yet');
    d.getElementById('dtWAdd').click();
    for (let i = 0; i < 4; i++) await settle();
    assert.strictEqual(stored(w)[0].panels[0].options.watches.slice(-1)[0].name, 'Nothing here', 'added on the second press');
  });

  await test('repeater names off the mesh stay text', async () => {
    const { d } = await setup('#/data?dash=path&v.from=' + GOYLE + '&v.to=' + INSH);
    assert.strictEqual(d.querySelectorAll('#app img').length, 0);
    assert.ok(panel(d, 'watch').textContent.indexOf('<img src=x') >= 0);
  });

  await test('an imported watch list with a bad key is refused', async () => {
    const { w } = await setup('#/data?dash=links');
    const X = w.__corescopeDataInternals;
    const bad = { schema: 1, id: 'x', title: 'x', panels: [{ id: 'l', title: 'W', type: 'watchlist', query: {}, options: { watches: [{ name: 'n', from: 'nothex', to: INSH }] }, layout: lay(0, 0, 6, 4) }] };
    assert.ok(/repeater/.test(X.validateDashboard(bad)), X.validateDashboard(bad));
    const good = { schema: 1, id: 'x', title: 'x', variables: [{ name: 'a', kind: 'repeater' }], panels: [{ id: 'w', title: 'W', type: 'watch', query: { from: '$a', to: INSH }, layout: lay(0, 0, 6, 4) }] };
    assert.strictEqual(X.validateDashboard(good), '');
  });

  await test('a watch can keep its flag for two weeks, and asks for it', async () => {
    const two = JSON.parse(JSON.stringify(LINKS));
    two.id = 'two'; two.panels[0].query = { flagDays: 14 };
    LIBRARY.dashboards.push(two);
    try {
      const { posted } = await setup('#/data?dash=two');
      assert.strictEqual(posted[0].flagDays, 14, JSON.stringify(posted[0]));
    } finally { LIBRARY.dashboards.pop(); }
  });

  console.log(`\n${passed} passed, ${failed} failed`);
  process.exit(failed > 0 ? 1 : 0);
})();
