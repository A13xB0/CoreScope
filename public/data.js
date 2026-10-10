/* === CoreScope — data.js ===
   The Data tab (route #/data): every packet of the last few days, placed by
   the area it STARTED in and the areas whose observers heard it, as a
   full-screen workspace. Stackable filter chips, a chart whose X axis, split
   and small multiples are all choosable, a who-hears-whom matrix, and the
   packets behind any figure in a table, each opening in the packet view.

   Data: GET /api/data/config once, then /api/data/summary and
   /api/data/cube for the period on screen, /api/data/packets for a drill.
   Area names come from the deployment's areas file and node names off the
   mesh, so everything is written with textContent. */
'use strict';
(function () {
  // Cube row layout, as internal/dataindex writes it.
  var I = { b: 0, o: 1, h: 2, t: 3, s: 4, w: 5, rt: 6, x: 7, ch: 8, n: 9 };

  var SLOTS = ['var(--dt-c1)', 'var(--dt-c2)', 'var(--dt-c3)', 'var(--dt-c4)', 'var(--dt-c5)', 'var(--dt-c6)', 'var(--dt-c7)'];
  var REST = 'var(--dt-c-rest)';
  var MAX_SERIES = 6;
  var MAX_PANELS = 6;
  // Colour follows the entity, so a filter never repaints what survives it.
  // Areas take the slot of their place in the areas file; packet types and
  // arrival are fixed here.
  var TYPE_SLOT = { 4: 0, 5: 1, 2: 2, 0: 3, 1: 4, 8: 5, 3: 6 };
  var HOW_LABEL = { relayed: 'Relayed by repeaters there', across: 'Heard straight across', unclear: 'Last repeater not placed' };
  var SCOPE_KIND = { scoped: 'A region scope', unscoped: 'Unscoped flood', unrecognised: 'Code nobody here can name', direct: 'Direct route' };
  var BASIS = { advert: 'Its advert', node: 'Its node record', 'first-relay': 'First repeater', 'zero-hop': 'Heard direct', unknown: 'Not placed' };
  // Ways to group time, per period: the finest is the cube's own step.
  var ROLLUPS = {
    '24h': [['10m', '10 minutes', 600], ['30m', '30 minutes', 1800], ['1h', 'Hour', 3600]],
    '7d': [['1h', 'Hour', 3600], ['6h', '6 hours', 21600], ['1d', 'Day', 86400]],
    '30d': [['6h', '6 hours', 21600], ['1d', 'Day', 86400], ['1w', 'Week', 604800]]
  };
  var DEFAULT_ROLL = { '24h': '1h', '7d': '6h', '30d': '1d' };
  var VIEWS = [['bars', 'Bars'], ['lines', 'Lines'], ['area', 'Area']];
  var MODES = [['count', 'Count'], ['share', 'Share']];
  var DIM_ORDER = ['border', 'origin', 'heard', 'through', 'kind', 'type', 'channel', 'scope', 'sclass', 'how', 'routing'];

  var nf = new Intl.NumberFormat();
  var cfg = null, areas = {}, areaSlot = {}, current = '24h', group = 'all';
  var summaries = {}, cubes = {}, loading = {}, gen = 0, root = null, q = null;
  var lastChart = null, resizeTimer = null, keyHandler = null, resizeHandler = null;

  function $(id) { return document.getElementById(id); }
  function el(tag, attrs, kids) {
    var e = document.createElement(tag);
    if (attrs) for (var k in attrs) {
      if (k === 'text') e.textContent = attrs[k];
      else e.setAttribute(k, attrs[k]);
    }
    (kids || []).forEach(function (c) { if (c) e.appendChild(typeof c === 'string' ? document.createTextNode(c) : c); });
    return e;
  }
  function n(v) { return nf.format(v || 0); }
  function name(code) {
    if (code === 'unknown') return 'Not known';
    return areas[code] ? areas[code].label : code;
  }
  function typeName(t) { return (cfg.typeNames && cfg.typeNames[t]) || ('Type ' + t); }
  function windowLabel(w) {
    for (var i = 0; i < cfg.windows.length; i++) if (cfg.windows[i].key === w) return cfg.windows[i].label.toLowerCase();
    return w;
  }
  function areaCodes() { return cfg.areas.map(function (a) { return a.code; }); }
  function home() { return cfg.home || ''; }
  function cellOf(cells, o, h) {
    for (var i = 0; i < cells.length; i++) if (cells[i].origin === o && cells[i].heard === h) return cells[i].packets;
    return 0;
  }

  // ---- query and address ---------------------------------------------
  function defaultQuery() {
    var f = [{ d: 'border', n: 0, v: ['crossed'] }];
    if (home()) f.push({ d: 'origin', n: 0, v: [home()] });
    return { f: f, view: 'bars', mode: 'count', x: 'time', by: 'scope', panel: 'none', pick: [], roll: '' };
  }
  function syncHash() {
    var p = new URLSearchParams();
    p.set('w', current);
    if (group !== 'all') p.set('t', group);
    p.set('q', JSON.stringify(q));
    try { history.replaceState(null, '', '#/data?' + p.toString()); } catch (e) { /* not fatal */ }
  }
  function readHash() {
    var p = new URLSearchParams(location.hash.split('?')[1] || '');
    var keys = cfg.windows.map(function (w) { return w.key; });
    current = keys.indexOf(p.get('w')) >= 0 ? p.get('w') : keys[0];
    var groups = (cfg.groups || []).map(function (g) { return g.key; });
    group = groups.indexOf(p.get('t')) >= 0 ? p.get('t') : 'all';
    q = defaultQuery();
    var raw = p.get('q');
    if (!raw) return;
    try {
      var parsed = JSON.parse(raw);
      if (!parsed || !Array.isArray(parsed.f)) return;
      q.f = parsed.f.filter(function (f) { return f && DIMS[f.d] && Array.isArray(f.v); });
      if (/^(bars|lines|area)$/.test(parsed.view)) q.view = parsed.view;
      if (parsed.mode === 'share') q.mode = 'share';
      if (parsed.x === 'time' || DIMS[parsed.x]) q.x = parsed.x;
      if (parsed.panel === 'none' || DIMS[parsed.panel]) q.panel = parsed.panel;
      if (parsed.by === 'none' || DIMS[parsed.by]) q.by = parsed.by;
      if (Array.isArray(parsed.pick)) q.pick = parsed.pick.map(String);
      if (typeof parsed.roll === 'string') q.roll = parsed.roll;
    } catch (e) { /* a mangled link falls back to the default view */ }
  }

  // ---- dimensions -----------------------------------------------------
  function areaSlotOf(v) { return areaSlot[v]; }
  var DIMS = {
    border: {
      label: 'Crossed a border',
      values: function () { return ['crossed', 'local', 'unknown']; },
      of: function (c, r) { var o = c.origins[r[I.o]]; return o === 'unknown' ? 'unknown' : (o === c.heard[r[I.h]] ? 'local' : 'crossed'); },
      name: function (v) { return { crossed: 'Yes', local: 'No, heard where it started', unknown: 'Start not known' }[v] || v; },
      slot: function (v) { return { crossed: 0, local: 1, unknown: -2 }[v]; }
    },
    origin: { label: 'From', values: function (c) { return c.origins; }, of: function (c, r) { return c.origins[r[I.o]]; }, name: name, slot: areaSlotOf },
    heard: { label: 'Heard in', values: function (c) { return c.heard; }, of: function (c, r) { return c.heard[r[I.h]]; }, name: name, slot: areaSlotOf },
    through: {
      label: 'Through',
      values: function (c) {
        var seen = {};
        c.through.forEach(function (t) { (t ? t.split('+') : ['straight']).forEach(function (a) { seen[a] = true; }); });
        var list = ['straight'].concat(areaCodes());
        Object.keys(seen).forEach(function (a) { if (list.indexOf(a) < 0) list.push(a); });
        return list.filter(function (v) { return seen[v]; });
      },
      of: function (c, r) { return c.through[r[I.x]] || 'straight'; },
      // A packet that crossed two areas matches either one.
      match: function (v, vals) { return v.split('+').some(function (a) { return vals.indexOf(a) >= 0; }); },
      name: function (v) { return v === 'straight' ? 'Straight between the two' : v.split('+').map(name).join(' and '); },
      slot: function (v) { return v === 'straight' ? -2 : (v.indexOf('+') < 0 ? areaSlot[v] : undefined); }
    },
    kind: {
      label: 'Kind',
      values: function () { return (cfg.groups || []).map(function (g) { return g.key; }).concat(['direct-advert']); },
      of: function (c, r) {
        var t = c.types[r[I.t]], route = c.routes[r[I.rt]];
        for (var i = 0; i < (cfg.groups || []).length; i++) {
          var g = cfg.groups[i];
          if (g.types.indexOf(t) >= 0) return (g.floodOnly && route !== 0 && route !== 1) ? 'direct-advert' : g.key;
        }
        return 'requests';
      },
      name: function (v) {
        if (v === 'direct-advert') return 'Adverts (direct)';
        var g = (cfg.groups || []).filter(function (gg) { return gg.key === v; })[0];
        return g ? g.label : v;
      },
      slot: function (v) { return { adverts: 0, messages: 1, requests: 2, 'direct-advert': 3 }[v]; }
    },
    type: { label: 'Packet type', values: function (c) { return c.types.map(String); }, of: function (c, r) { return String(c.types[r[I.t]]); }, name: function (v) { return typeName(+v); }, slot: function (v) { return TYPE_SLOT[v]; } },
    channel: {
      label: 'Channel',
      values: function (c) { return c.channels; },
      of: function (c, r) { return c.channels[r[I.ch]]; },
      name: function (v) { return v === '' ? 'Not a channel message' : (v.indexOf('hash:') === 0 ? 'Channel ' + v.slice(5) + ' (no key here)' : v); },
      slot: function (v) { return v === '' ? -2 : undefined; }
    },
    scope: {
      label: 'Scope',
      values: function (c) { return c.scopes.map(function (sc) { return sc.key; }); },
      of: function (c, r) { return c.scopes[r[I.s]].key; },
      name: function (v) { return v; },
      // The busiest scopes keep their colour across filters and periods.
      slot: function (v) { var c = cubes[current]; if (!c) return undefined; for (var i = 0; i < c.scopes.length && i < SLOTS.length; i++) if (c.scopes[i].key === v) return i; return undefined; }
    },
    sclass: { label: 'Scope kind', values: function () { return ['scoped', 'unscoped', 'unrecognised', 'direct']; }, of: function (c, r) { return c.scopes[r[I.s]].class; }, name: function (v) { return SCOPE_KIND[v] || v; }, slot: function (v) { return { scoped: 0, unscoped: 1, unrecognised: -2, direct: 3 }[v]; } },
    how: { label: 'How it arrived', values: function (c) { return c.how; }, of: function (c, r) { return c.how[r[I.w]]; }, name: function (v) { return HOW_LABEL[v] || v; }, slot: function (v) { return { relayed: 0, across: 1, unclear: -2 }[v]; } },
    routing: { label: 'Routing', values: function () { return ['flood', 'direct']; }, of: function (c, r) { var rt = c.routes[r[I.rt]]; return rt === 0 || rt === 1 ? 'flood' : 'direct'; }, name: function (v) { return v === 'flood' ? 'Flooded' : 'Routed direct'; }, slot: function (v) { return v === 'flood' ? 0 : 1; } }
  };

  function presets() {
    var crossed = { d: 'border', n: 0, v: ['crossed'] }, h = home(), list = [];
    if (h) {
      list.push(['Leaving ' + name(h), [crossed, { d: 'origin', n: 0, v: [h] }]]);
      list.push(['Entering ' + name(h), [crossed, { d: 'heard', n: 0, v: [h] }]]);
      list.push(['Entering via another area', [crossed, { d: 'heard', n: 0, v: [h] }, { d: 'through', n: 1, v: ['straight'] }]]);
      list.push(['Between other areas', [crossed, { d: 'origin', n: 1, v: [h] }, { d: 'heard', n: 1, v: [h] }]]);
    }
    list.push(['Between areas', [crossed]]);
    list.push(['Within each area', [{ d: 'border', n: 0, v: ['local'] }]]);
    list.push(['Everything', []]);
    return list;
  }

  function filterText(f) {
    var D = DIMS[f.d], vals = f.v.map(D.name);
    var list = vals.length <= 2 ? vals.join(' or ') : vals.slice(0, 2).join(', ') + ' or ' + (vals.length - 2) + ' more';
    return D.label + (f.n ? ' is not ' : ' is ') + list;
  }
  // The same filter as part of a sentence: "from Scotland", "not heard in Wales".
  function filterPhrase(f) {
    var D = DIMS[f.d], vals = f.v.map(D.name);
    var list = vals.length <= 3 ? vals.join(' or ') : vals.slice(0, 2).join(', ') + ' or ' + (vals.length - 2) + ' more';
    var not = f.n ? 'not ' : '';
    if (f.d === 'origin') return not + 'from ' + list;
    if (f.d === 'heard') return not + 'heard in ' + list;
    if (f.d === 'border') return (f.n ? 'not ' : '') + f.v.map(function (v) { return { crossed: 'heard across a border', local: 'heard where it started', unknown: 'start not known' }[v]; }).join(' or ');
    if (f.d === 'through') return f.v.length === 1 && f.v[0] === 'straight' ? (f.n ? 'through another area' : 'straight between the two') : not + 'through ' + list;
    return D.label.toLowerCase() + (f.n ? ' not ' : ' ') + list;
  }

  // ---- page skeleton ----------------------------------------------------
  function pageHtml() {
    return '' +
      '<div class="dt-page" id="dtPage">' +
        '<header class="dt-bar">' +
          '<h2 class="dt-title">Data</h2>' +
          '<div class="dt-seg" role="group" aria-label="Period, for the whole page" id="dtWindows"></div>' +
          '<span class="dt-cover" id="dtCover" aria-live="polite">Loading…</span>' +
          '<span class="dt-bar-actions"><span id="dtReset"></span>' +
          '<button type="button" class="dt-btn" id="dtFill" aria-pressed="false" title="Hide the site navigation">Full screen</button></span>' +
        '</header>' +
        '<div class="dt-body">' +
          '<aside class="dt-rail" aria-label="Build the question">' +
            '<section class="dt-block dt-filters-block"><h3 class="dt-k">Filters</h3><div class="dt-chips" id="dtFilters" role="group" aria-label="Filters"></div>' +
              '<div class="dt-editor" id="dtEditor" hidden></div></section>' +
            '<button type="button" class="dt-rail-toggle" id="dtRailToggle" aria-expanded="false" aria-controls="dtRailMore">Presets and chart options</button>' +
            '<div class="dt-rail-more" id="dtRailMore">' +
            '<section class="dt-block"><h3 class="dt-k">Start from</h3><div class="dt-chips" id="dtPresets" role="group" aria-label="Start from"></div></section>' +
            '<section class="dt-block"><h3 class="dt-k">Chart</h3><div class="dt-view" id="dtView"></div></section>' +
            '<section class="dt-block" id="dtPicksBlock"><h3 class="dt-k">Series</h3><div class="dt-chips" id="dtPicks" role="group" aria-label="Picked-out values"></div>' +
              '<div class="dt-editor" id="dtPicker" hidden></div></section>' +
            '</div>' +
          '</aside>' +
          '<main class="dt-main">' +
            '<div class="dt-tiles" id="dtTiles"></div>' +
            '<section class="dt-card">' +
              '<p class="dt-sum" id="dtSum" aria-live="polite"></p>' +
              '<div class="dt-chart" id="dtChart"></div>' +
              '<div class="dt-legend" id="dtLegend"></div>' +
              '<div id="dtRelays"></div>' +
              '<details class="dt-more"><summary>The numbers behind the chart</summary><div class="dt-scroll" id="dtTable"></div></details>' +
            '</section>' +
            '<section class="dt-card" id="dtMatrixCard">' +
              '<div class="dt-card-h"><h3>Who hears whom</h3><p>Rows are where packets started, columns where they were heard; a packet heard in three areas counts in each. Select any figure or heading to chart it. The buttons narrow the matrix only.</p></div>' +
              '<div class="dt-seg dt-groups" role="group" aria-label="Packets shown in the matrix" id="dtGroups"></div>' +
              '<p class="dt-note" id="dtGroupNote" aria-live="polite"></p>' +
              '<div class="dt-matrix-wrap" id="dtMatrix"></div>' +
              '<p class="dt-note" id="dtMatrixNote"></p>' +
            '</section>' +
            '<details class="dt-card dt-method"><summary>How it is counted</summary><div class="dt-method-in">' +
              '<p><strong>Where a packet started</strong>Its advert, when the advert carries a position. Otherwise the sender\'s node record, then the first repeater that passed it on, then, for a packet heard with no repeater, the observer that heard it. The bar at the top says how many could be placed.</p>' +
              '<p><strong>Where it was heard</strong>By the observers that reported it, placed by their own node position or, without one, their airport code. An area with no observer can never show anything arriving.</p>' +
              '<p><strong>Relayed or heard across</strong>If the repeater an observer heard it from stands in the observer\'s own area, that area\'s mesh carried it in. If it came straight from the starting area, it was simply heard over the air. Each packet counts once per area: relayed if any copy there was.</p>' +
              '<p><strong>Through</strong>The other areas whose repeaters passed it on between start and receipt. Only repeaters that can be placed count.</p>' +
              '<p><strong>Areas are places, not operators</strong>A repeater counts for where it stands, whoever runs it.</p>' +
              '<p><strong>Short path hashes place nothing</strong>A 1-byte hop is shared by many repeaters, so it never places a relay. Packets that crossed only 1-byte hops are counted where they were heard but may have no known start.</p>' +
            '</div></details>' +
          '</main>' +
        '</div>' +
      '</div>' +
      '<dialog class="dt-modal" id="dtModal" aria-labelledby="dtModalTitle">' +
        '<div class="dt-modal-in">' +
          '<div class="dt-modal-head"><div><h3 id="dtModalTitle">Packets</h3><p class="dt-modal-sum" id="dtModalSum" aria-live="polite"></p></div>' +
          '<button type="button" class="dt-btn" id="dtModalClose">Close</button></div>' +
          '<div class="dt-chips dt-modal-cols" id="dtModalCols" role="group" aria-label="Columns"></div>' +
          '<div class="dt-modal-table" id="dtModalTable"></div>' +
        '</div>' +
      '</dialog>';
  }

  // ---- page-wide period ----------------------------------------------
  function renderWindows() {
    var box = $('dtWindows');
    box.textContent = '';
    cfg.windows.forEach(function (w) {
      var b = el('button', { type: 'button', 'aria-pressed': String(w.key === current), text: w.label });
      b.addEventListener('click', function () { if (current === w.key) return; current = w.key; q.roll = ''; syncHash(); render(); });
      box.appendChild(b);
    });
  }

  function renderCover(s) {
    var box = $('dtCover');
    if (!s) { box.textContent = 'Loading the ' + windowLabel(current) + '…'; return; }
    var b = s.bases || {};
    var text = n(s.packets) + ' packets, ' + n(s.crossBorder) + ' heard outside the area they started in, ' + n(s.packets - (b.unknown || 0)) + ' with a known start';
    if (!s.complete) {
      text = 'Filled from ' + new Date(s.coveredFrom).toLocaleString([], { day: 'numeric', month: 'short', hour: '2-digit', minute: '2-digit' }) +
        ', so this period is not complete yet. ' + text;
    }
    box.textContent = text + '.';
    box.title = text + '. The period applies to the whole page.';
  }

  function tile(label, rows) {
    var t = el('div', { 'class': 'dt-tile' }, [el('span', { 'class': 'dt-tile-k', text: label })]);
    var vals = el('div', { 'class': 'dt-tile-vals' });
    rows.forEach(function (r) { vals.appendChild(el('span', { 'class': 'dt-tile-v' }, [el('b', { text: n(r[0]) }), el('small', { text: r[1] })])); });
    t.appendChild(vals);
    return t;
  }

  function renderTiles(s) {
    var box = $('dtTiles');
    box.textContent = '';
    if (!s) return;
    var b = s.bases || {}, h = home();
    box.appendChild(tile('Packets', [[s.packets, 'heard'], [s.packets - (b.unknown || 0), 'start known']]));
    box.appendChild(tile('Across a border', [[s.crossBorder, 'heard outside their start']]));
    if (h) {
      // The busiest neighbours of the home area, each way.
      var others = {};
      s.matrix.forEach(function (c) {
        if (c.origin === h && c.heard !== h) others[c.heard] = (others[c.heard] || 0) + c.packets;
        if (c.heard === h && c.origin !== h && c.origin !== 'unknown') others[c.origin] = (others[c.origin] || 0) + c.packets;
      });
      Object.keys(others).sort(function (a, b2) { return others[b2] - others[a]; }).slice(0, 2).forEach(function (a) {
        box.appendChild(tile(name(a), [[cellOf(s.matrix, h, a), 'from ' + name(h)], [cellOf(s.matrix, a, h), 'to ' + name(h)]]));
      });
    } else {
      var seen = s.areas.filter(function (a) { return a.observed; }).length;
      box.appendChild(tile('Areas', [[seen, 'with an observer'], [s.areas.length - seen, 'without']]));
    }
  }

  // ---- matrix ---------------------------------------------------------
  function renderGroups() {
    var box = $('dtGroups');
    box.textContent = '';
    [{ key: 'all', label: 'All traffic' }].concat(cfg.groups || []).forEach(function (g) {
      var b = el('button', { type: 'button', 'aria-pressed': String(g.key === group), text: g.label });
      b.addEventListener('click', function () { group = g.key; syncHash(); renderGroups(); renderMatrix(summaries[current]); });
      box.appendChild(b);
    });
    var names = { all: 'Every packet type.' };
    (cfg.groups || []).forEach(function (g) {
      names[g.key] = g.floodOnly ? 'Adverts sent as a flood, scoped or unscoped. Direct adverts are only in All traffic.' : g.types.map(typeName).join(', ') + '.';
    });
    $('dtGroupNote').textContent = names[group];
  }

  // A cell is one start and one receiving area, a heading a whole row or
  // column: each charts that slice.
  function exploreFrom(origin, heard) {
    q.f = [];
    if (origin) q.f.push({ d: 'origin', n: 0, v: [origin] });
    if (heard) q.f.push({ d: 'heard', n: 0, v: [heard] });
    if (group !== 'all') q.f.push({ d: 'kind', n: 0, v: [group] });
    closeEditor(); syncHash(); renderExplorer();
    var main = root.querySelector('.dt-main');
    if (main) main.scrollTo({ top: 0, behavior: 'smooth' });
  }
  function clickable(node, title, fn) {
    node.classList.add('dt-hit');
    node.tabIndex = 0;
    node.setAttribute('role', 'button');
    node.title = title;
    node.addEventListener('click', fn);
    node.addEventListener('keydown', function (ev) { if (ev.key === 'Enter' || ev.key === ' ') { ev.preventDefault(); fn(); } });
  }

  function renderMatrix(s) {
    var box = $('dtMatrix');
    box.textContent = '';
    if (!s) return;
    var obs = {};
    s.areas.forEach(function (a) { obs[a.code] = a.observed; });
    // An area with no observer can never show anything arriving, so it gets
    // no column, only a row; the note under the table names them.
    var all = areaCodes(), cols = all.filter(function (c) { return obs[c]; }), rows = all.concat(['unknown']);
    var cells = group === 'all' ? s.matrix : ((s.matrixByGroup || {})[group] || []);
    var by = {}, max = 1;
    cells.forEach(function (c) { by[c.origin + '|' + c.heard] = c; if (c.origin !== c.heard && c.packets > max) max = c.packets; });
    var unseen = all.filter(function (c) { return !obs[c]; }).map(name);
    $('dtMatrixNote').textContent = unseen.length ?
      (unseen.length > 1 ? unseen.slice(0, -1).join(', ') + ' and ' + unseen[unseen.length - 1] : unseen[0]) +
      (unseen.length > 1 ? ' have' : ' has') + ' no observer reporting in this period, so nothing can be counted arriving there and ' + (unseen.length > 1 ? 'they have' : 'it has') + ' no column. What they send still shows in their rows.' : '';
    if (!cols.length) { box.appendChild(el('p', { 'class': 'dt-empty', text: 'No observer in any area reported in this period.' })); return; }
    var t = el('table', { 'class': 'dt-matrix' });
    t.appendChild(el('caption', { 'class': 'dt-sr', text: 'Packets by where they started (rows) and where they were heard (columns). Every figure and heading charts it.' }));
    var hr = el('tr', null, [el('th', { scope: 'col', 'class': 'dt-corner' }, [el('span', { text: 'Heard in →' }), el('span', { text: 'From ↓' })])]);
    cols.forEach(function (h) {
      var th = el('th', { scope: 'col', text: name(h) });
      clickable(th, 'Chart everything heard in ' + name(h), function () { exploreFrom(null, h); });
      hr.appendChild(th);
    });
    t.appendChild(el('thead', null, [hr]));
    var tb = el('tbody');
    rows.forEach(function (o) {
      var rh = el('th', { scope: 'row', text: name(o) });
      clickable(rh, o === 'unknown' ? 'Chart packets whose start is not known' : 'Chart everything from ' + name(o), function () { exploreFrom(o, null); });
      var tr = el('tr', null, [rh]);
      cols.forEach(function (h) {
        var c = by[o + '|' + h], v = c ? c.packets : 0, td;
        if (o === h) td = el('td', { 'class': 'dt-local', text: n(v) });
        else {
          td = el('td', { text: n(v) });
          if (v) td.style.background = 'color-mix(in srgb, var(--accent) ' + Math.round(8 + 42 * Math.log(1 + v) / Math.log(1 + max)) + '%, transparent)';
        }
        var what = o === h ? name(o) + "'s own traffic" : (o === 'unknown' ? 'packets of unknown start heard in ' + name(h) : 'packets from ' + name(o) + ' heard in ' + name(h));
        clickable(td, 'Chart ' + n(v) + ' ' + what, function () { exploreFrom(o, h); });
        tr.appendChild(td);
      });
      tb.appendChild(tr);
    });
    t.appendChild(tb);
    box.appendChild(t);
  }

  // ---- explorer controls ---------------------------------------------
  function renderPresets() {
    var box = $('dtPresets');
    box.textContent = '';
    presets().forEach(function (p) {
      var b = el('button', { type: 'button', 'class': 'dt-chip', 'aria-pressed': String(JSON.stringify(q.f) === JSON.stringify(p[1])), text: p[0] });
      b.addEventListener('click', function () { q.f = JSON.parse(JSON.stringify(p[1])); closeEditor(); syncHash(); renderExplorer(); });
      box.appendChild(b);
    });
    // Back to the view the tab opens with. The period is the page's, so it stays.
    var reset = el('button', { type: 'button', 'class': 'dt-btn', text: 'Reset' });
    if (JSON.stringify(q) === JSON.stringify(defaultQuery())) reset.disabled = true;
    reset.addEventListener('click', function () {
      q = defaultQuery();
      closeEditor();
      var pk = $('dtPicker'); pk.hidden = true; pk.textContent = '';
      syncHash(); renderExplorer();
    });
    var slot = $('dtReset');
    slot.textContent = '';
    slot.appendChild(reset);
  }

  function closeEditor() { var e = $('dtEditor'); if (e) { e.hidden = true; e.textContent = ''; } }

  function renderFilterChips() {
    var box = $('dtFilters');
    box.textContent = '';
    q.f.forEach(function (f, i) {
      var chip = el('span', { 'class': 'dt-fchip' });
      var edit = el('button', { type: 'button', 'class': 'dt-fchip-edit', text: filterText(f), 'aria-label': 'Edit filter: ' + filterText(f) });
      edit.addEventListener('click', function () { openEditor(i); });
      var del = el('button', { type: 'button', 'class': 'dt-fchip-x', 'aria-label': 'Remove filter: ' + filterText(f), text: '×' });
      del.addEventListener('click', function () { q.f.splice(i, 1); closeEditor(); syncHash(); renderExplorer(); });
      chip.appendChild(edit); chip.appendChild(del);
      box.appendChild(chip);
    });
    var add = el('button', { type: 'button', 'class': 'dt-chip dt-add', text: '+ Add filter' });
    add.addEventListener('click', function () { openEditor(-1); });
    box.appendChild(add);
    if (q.f.length) {
      var clear = el('button', { type: 'button', 'class': 'dt-link', text: 'Clear all' });
      clear.addEventListener('click', function () { q.f = []; closeEditor(); syncHash(); renderExplorer(); });
      box.appendChild(clear);
    }
  }

  function rowPasses(c, r, skip) {
    for (var i = 0; i < q.f.length; i++) {
      if (i === skip) continue;
      var f = q.f[i], D = DIMS[f.d], v = D.of(c, r);
      var hit = D.match && !f.x ? D.match(v, f.v) : f.v.indexOf(v) >= 0;
      if (hit === !!f.n) return false;
    }
    return true;
  }

  // Packets per value of a dimension under the filters, leaving out one filter
  // (the one being edited) so its own choices still show their counts.
  function valueCounts(c, d, skip, combos) {
    var counts = {};
    c.rows.forEach(function (r) {
      if (!rowPasses(c, r, skip)) return;
      var val = DIMS[d].of(c, r);
      (d === 'through' && !combos && val !== 'straight' ? val.split('+') : [val]).forEach(function (k) { counts[k] = (counts[k] || 0) + r[I.n]; });
    });
    return counts;
  }

  // Toggle chips: dashed until chosen, then the accent pill with a cross.
  function valueChips(values, selected, counts, nameOf, colourOf, onChange) {
    var wrap = el('div', { 'class': 'dt-vchips', role: 'group' });
    var bar = el('div', { 'class': 'dt-vchips-bar' });
    var all = el('button', { type: 'button', 'class': 'dt-link', text: 'All' });
    all.addEventListener('click', function () { onChange(values.slice()); });
    var none = el('button', { type: 'button', 'class': 'dt-link', text: 'None' });
    none.addEventListener('click', function () { onChange([]); });
    bar.appendChild(all); bar.appendChild(none);
    wrap.appendChild(bar);
    var list = el('div', { 'class': 'dt-vchips-list' });
    values.slice().sort(function (a, b) { return (counts[b] || 0) - (counts[a] || 0); }).forEach(function (v) {
      var on = selected.indexOf(v) >= 0;
      var b = el('button', { type: 'button', 'class': 'dt-vchip' + (counts[v] ? '' : ' is-empty'), 'aria-pressed': String(on) });
      if (colourOf) { var sw = el('span', { 'class': 'dt-sw' }); sw.style.background = colourOf(v); b.appendChild(sw); }
      b.appendChild(el('span', { text: nameOf(v) }));
      b.appendChild(el('span', { 'class': 'dt-vcount', text: n(counts[v] || 0) }));
      b.appendChild(el('span', { 'class': 'dt-vx', 'aria-hidden': 'true', text: on ? '×' : '+' }));
      b.addEventListener('click', function () { onChange(on ? selected.filter(function (x) { return x !== v; }) : selected.concat([v])); });
      list.appendChild(b);
    });
    wrap.appendChild(list);
    return wrap;
  }

  function openEditor(i) {
    var c = cubes[current];
    if (!c) return;
    var f = i >= 0 ? JSON.parse(JSON.stringify(q.f[i])) : { d: 'heard', n: 0, v: [] };
    var box = $('dtEditor');
    box.hidden = false;
    var draw = function () {
      box.textContent = '';
      var dimSel = el('select', { 'aria-label': 'Dimension' });
      DIM_ORDER.forEach(function (d) { var o = el('option', { value: d, text: DIMS[d].label }); if (d === f.d) o.selected = true; dimSel.appendChild(o); });
      dimSel.addEventListener('change', function () { f.d = dimSel.value; f.v = []; delete f.x; draw(); });
      var opSel = el('select', { 'aria-label': 'Match' });
      [['0', 'is'], ['1', 'is not']].forEach(function (o) { var op = el('option', { value: o[0], text: o[1] }); if (+o[0] === f.n) op.selected = true; opSel.appendChild(op); });
      opSel.addEventListener('change', function () { f.n = +opSel.value; });
      box.appendChild(el('div', { 'class': 'dt-ed-row' }, [el('label', null, ['Dimension', dimSel]), el('label', null, ['Match', opSel])]));
      box.appendChild(valueChips(DIMS[f.d].values(c), f.v, valueCounts(c, f.d, i), DIMS[f.d].name, null, function (vals) { f.v = vals; draw(); }));
      var apply = el('button', { type: 'button', 'class': 'dt-btn dt-btn-primary', text: i >= 0 ? 'Update filter' : 'Add filter' });
      apply.addEventListener('click', function () {
        if (!f.v.length) { closeEditor(); return; }
        if (i >= 0) q.f[i] = f; else q.f.push(f);
        closeEditor(); syncHash(); renderExplorer();
      });
      var cancel = el('button', { type: 'button', 'class': 'dt-btn', text: 'Cancel' });
      cancel.addEventListener('click', closeEditor);
      box.appendChild(el('div', { 'class': 'dt-ed-row' }, [apply, cancel]));
    };
    draw();
  }

  function selectControl(label, value, options, onChange) {
    var sel = el('select', { 'aria-label': label });
    options.forEach(function (o) { var op = el('option', { value: o[0], text: o[1] }); if (o[0] === value) op.selected = true; sel.appendChild(op); });
    sel.addEventListener('change', function () { onChange(sel.value); });
    return el('label', null, [label, sel]);
  }
  function dimOptions(withNone) {
    return (withNone ? [['none', 'Nothing']] : []).concat(DIM_ORDER.map(function (d) { return [d, DIMS[d].label]; }));
  }
  function rollFor(w) {
    var opts = ROLLUPS[w] || ROLLUPS['24h'], want = q.roll || DEFAULT_ROLL[w];
    for (var i = 0; i < opts.length; i++) if (opts[i][0] === want) return opts[i];
    return opts.filter(function (o) { return o[0] === DEFAULT_ROLL[w]; })[0] || opts[0];
  }
  function segControl(label, value, options, onPick, disabled) {
    var seg = el('div', { 'class': 'dt-seg', role: 'group', 'aria-label': label });
    options.forEach(function (o) {
      var off = disabled && disabled(o[0]);
      var b = el('button', { type: 'button', 'aria-pressed': String(value === o[0]), text: o[1] });
      if (off) { b.disabled = true; b.title = 'Lines and area need time on the X axis'; }
      b.addEventListener('click', function () { onPick(o[0]); });
      seg.appendChild(b);
    });
    return el('div', { 'class': 'dt-view-seg' }, [el('span', { 'class': 'dt-view-k', text: label }), seg]);
  }

  function renderViewControls() {
    var box = $('dtView');
    box.textContent = '';
    if (q.x !== 'time' && q.view !== 'bars') q.view = 'bars';
    box.appendChild(segControl('Chart', q.view, VIEWS, function (v) { q.view = v; syncHash(); renderExplorer(); }, function (v) { return q.x !== 'time' && v !== 'bars'; }));
    box.appendChild(segControl('Show', q.mode, MODES, function (v) { q.mode = v; syncHash(); renderExplorer(); }));
    box.appendChild(selectControl('X axis', q.x, [['time', 'Time']].concat(dimOptions(false)), function (v) { q.x = v; syncHash(); renderExplorer(); }));
    if (q.x === 'time') box.appendChild(selectControl('Group time by', rollFor(current)[0], (ROLLUPS[current] || []).map(function (o) { return [o[0], o[1]]; }), function (v) { q.roll = v; syncHash(); renderExplorer(); }));
    box.appendChild(selectControl('Split by', q.by, dimOptions(true), function (v) { q.by = v; q.pick = []; syncHash(); renderExplorer(); }));
    box.appendChild(selectControl('Separate charts by', q.panel, dimOptions(true), function (v) { q.panel = v; syncHash(); renderExplorer(); }));
    renderPicks();
  }

  // The picked-out values, as chips in the same style as the filters.
  function renderPicks() {
    var block = $('dtPicksBlock'), box = $('dtPicks'), picker = $('dtPicker');
    box.textContent = '';
    if (q.by === 'none') { block.hidden = true; picker.hidden = true; return; }
    block.hidden = false;
    var D = DIMS[q.by];
    box.appendChild(el('span', { 'class': 'dt-picks-k', text: q.pick.length ? 'Picked out' : 'The six biggest, the rest together' }));
    q.pick.forEach(function (v) {
      var chip = el('span', { 'class': 'dt-fchip' });
      var label = el('button', { type: 'button', 'class': 'dt-fchip-edit', text: D.name(v), 'aria-label': 'Change picked values' });
      label.addEventListener('click', function () { picker.hidden = false; renderPicker(); renderPicks(); });
      var del = el('button', { type: 'button', 'class': 'dt-fchip-x', 'aria-label': 'Stop picking out ' + D.name(v), text: '×' });
      del.addEventListener('click', function () { q.pick = q.pick.filter(function (x) { return x !== v; }); syncHash(); renderExplorer(); });
      chip.appendChild(label); chip.appendChild(del);
      box.appendChild(chip);
    });
    var add = el('button', { type: 'button', 'class': 'dt-chip dt-add', 'aria-expanded': String(!picker.hidden), text: picker.hidden ? '+ Pick out ' + (q.pick.length ? 'another' : 'values') : 'Done' });
    add.addEventListener('click', function () { picker.hidden = !picker.hidden; renderPicker(); renderPicks(); });
    box.appendChild(add);
    if (q.pick.length) {
      var clear = el('button', { type: 'button', 'class': 'dt-link', text: 'Clear' });
      clear.addEventListener('click', function () { q.pick = []; syncHash(); renderExplorer(); });
      box.appendChild(clear);
    }
  }

  function renderPicker() {
    var box = $('dtPicker'), c = cubes[current];
    if (box.hidden || q.by === 'none' || !c) { box.hidden = true; box.textContent = ''; return; }
    box.textContent = '';
    var D = DIMS[q.by];
    box.appendChild(el('p', { 'class': 'dt-hint', text: 'Pick the ' + D.label.toLowerCase() + ' values to show on their own. Everything else is counted together.' }));
    var shown = {};
    query(c).series.forEach(function (sr) { shown[sr.key] = sr.color; });
    var colourOf = function (v) { if (shown[v]) return shown[v]; var sl = D.slot ? D.slot(v) : undefined; return sl >= 0 ? SLOTS[sl] : REST; };
    var combos = q.by === 'through';
    var vals = combos ? c.through.map(function (t) { return t || 'straight'; }) : D.values(c);
    box.appendChild(valueChips(vals, q.pick, valueCounts(c, q.by, -1, combos), D.name, colourOf, function (picked) { q.pick = picked; syncHash(); renderExplorer(); }));
  }

  // ---- the query ------------------------------------------------------
  // Buckets rolled up to the chosen step: weeks start on Monday, UTC.
  function rolledBuckets(c, step) {
    var keys = [], idx = {}, map = [];
    c.buckets.forEach(function (t, i) {
      var day = Math.floor(t / 86400);
      // Day 0 was a Thursday, so (day + 3) % 7 counts days since Monday.
      var k = step === 604800 ? (day - (day + 3) % 7) * 86400 : t - t % step;
      if (!(k in idx)) { idx[k] = keys.length; keys.push(k); }
      map[i] = idx[k];
    });
    return { keys: keys, map: map };
  }

  function assignColours(list, D) {
    var used = {};
    list.forEach(function (sr) { var sl = D && D.slot ? D.slot(sr.key) : undefined; sr.slot = sl; if (sl >= 0) used[sl] = true; });
    var free = SLOTS.map(function (_, i) { return i; }).filter(function (i) { return !used[i]; });
    list.forEach(function (sr) {
      if (sr.slot === -2) { sr.color = REST; return; }
      if (!(sr.slot >= 0)) sr.slot = free.length ? free.shift() : -1;
      sr.color = sr.slot >= 0 ? SLOTS[sr.slot] : REST;
    });
  }

  // One chart's worth of figures. test narrows the rows further (a panel).
  function query(c, test) {
    var D = q.by === 'none' ? null : DIMS[q.by];
    var X = q.x === 'time' ? null : DIMS[q.x];
    var roll = rollFor(current), rb = rolledBuckets(c, roll[2]);
    var zero = function () { return rb.keys.map(function () { return 0; }); };
    var raw = {}, total = 0, how = { relayed: 0, across: 0, unclear: 0 }, rowTotals = {};
    c.rows.forEach(function (r) {
      if (!rowPasses(c, r, -1) || (test && !test(r))) return;
      var key = D ? D.of(c, r) : 'all', v = r[I.n];
      var sr = raw[key] || (raw[key] = { key: key, label: D ? D.name(key) : 'Packets', values: zero(), rows: {}, total: 0 });
      sr.values[rb.map[r[I.b]]] += v; sr.total += v; total += v;
      if (X) {
        var rk = X.of(c, r);
        sr.rows[rk] = (sr.rows[rk] || 0) + v;
        rowTotals[rk] = (rowTotals[rk] || 0) + v;
      }
      how[c.how[r[I.w]]] += v;
    });
    var list = Object.keys(raw).map(function (k) { return raw[k]; }).sort(function (a, b) { return b.total - a.total; });
    var keep = q.pick.length ? function (sr) { return q.pick.indexOf(sr.key) >= 0; }
      : function (sr, i) { return i < MAX_SERIES || list.length === MAX_SERIES + 1; };
    var kept = [], rest = null;
    list.forEach(function (sr, i) {
      if (!D || keep(sr, i)) { kept.push(sr); return; }
      if (!rest) rest = { key: '__rest', label: 'Everything else', values: zero(), rows: {}, total: 0 };
      sr.values.forEach(function (x, j) { rest.values[j] += x; });
      for (var k in sr.rows) rest.rows[k] = (rest.rows[k] || 0) + sr.rows[k];
      rest.total += sr.total;
    });
    if (!D) kept.forEach(function (sr) { sr.color = SLOTS[0]; });
    else assignColours(kept, D);
    if (rest) { rest.color = REST; kept.push(rest); }
    var rowKeys = Object.keys(rowTotals).sort(function (a, b) { return rowTotals[b] - rowTotals[a]; });
    var res = { series: kept, total: total, how: how, rowKeys: rowKeys, rowTotals: rowTotals, rowName: X ? X.name : null, buckets: rb.keys, step: roll[2], pct: false };
    if (q.mode === 'share') toShare(res);
    return res;
  }

  // Share: each period (or each row on a dimension axis) adds up to 100%.
  // The counts stay on each series for the tooltip and the table.
  function toShare(res) {
    res.pct = true;
    if (q.x === 'time') {
      var tot = res.buckets.map(function (_, i) { var t = 0; res.series.forEach(function (sr) { t += sr.values[i]; }); return t; });
      res.series.forEach(function (sr) { sr.counts = sr.values; sr.values = sr.values.map(function (v, i) { return tot[i] ? 100 * v / tot[i] : 0; }); });
    } else {
      res.series.forEach(function (sr) {
        sr.rowCounts = sr.rows; var pr = {};
        for (var k in sr.rows) pr[k] = res.rowTotals[k] ? 100 * sr.rows[k] / res.rowTotals[k] : 0;
        sr.rows = pr;
      });
    }
  }

  function chartTop(res) {
    if (res.pct) return 100;
    if (q.x !== 'time') return niceMax(Math.max.apply(null, [1].concat(res.rowKeys.map(function (k) { return res.rowTotals[k]; }))));
    var stacked = q.view !== 'lines', m = 1;
    res.buckets.forEach(function (_, i) {
      if (stacked) { var t = 0; res.series.forEach(function (sr) { t += sr.values[i]; }); m = Math.max(m, t); }
      else res.series.forEach(function (sr) { m = Math.max(m, sr.values[i]); });
    });
    return niceMax(m);
  }

  function drawChart(box, res, top, pv) {
    res.panelValue = pv;
    if (q.x !== 'time') totalsChart(box, res, top);
    else if (q.view === 'lines' || q.view === 'area') lineChart(box, res, q.view === 'area', top);
    else barChart(box, res, top);
  }

  // The chart, or one chart per value of "separate charts by", all on one
  // scale so they compare directly.
  function renderCharts(c, res) {
    var box = $('dtChart');
    box.textContent = '';
    if (q.panel === 'none') { drawChart(box, res, chartTop(res)); return; }
    var P = DIMS[q.panel];
    var counts = valueCounts(c, q.panel, -1, true);
    var vals = Object.keys(counts).sort(function (a, b) { return counts[b] - counts[a]; });
    var shown = vals.slice(0, MAX_PANELS);
    var panels = shown.map(function (v) { return { v: v, res: query(c, function (r) { return P.of(c, r) === v; }) }; });
    var top = 1;
    panels.forEach(function (p) { top = Math.max(top, chartTop(p.res)); });
    var grid = el('div', { 'class': 'dt-panels' });
    panels.forEach(function (p) {
      var cell = el('div', { 'class': 'dt-panel' });
      cell.appendChild(el('h4', { 'class': 'dt-panel-h' }, [P.label + ': ', el('strong', { text: P.name(p.v) }), el('span', { 'class': 'dt-vcount', text: ' ' + n(counts[p.v]) })]));
      var cb = el('div', { 'class': 'dt-chart dt-panel-chart' });
      cell.appendChild(cb);
      grid.appendChild(cell);
      p.box = cb;
    });
    box.appendChild(grid);
    panels.forEach(function (p) { drawChart(p.box, p.res, top, p.v); });
    if (vals.length > shown.length) box.appendChild(el('p', { 'class': 'dt-empty', text: (vals.length - shown.length) + ' smaller ' + P.label.toLowerCase() + ' values have no chart of their own; add a filter to see them.' }));
  }

  function renderExplorer() {
    if (!root || !cfg) return;
    renderPresets();
    renderFilterChips();
    var c = cubes[current];
    var main = root.querySelector('.dt-main');
    if (!c) {
      main.classList.add('dt-stale');
      loadPeriod(current);
      return;
    }
    main.classList.remove('dt-stale');
    renderViewControls();
    renderPicker();
    var res = query(c);
    var sum = $('dtSum');
    sum.textContent = '';
    if (!res.total) {
      sum.textContent = 'Nothing matches these filters in the ' + windowLabel(current) + '.';
    } else {
      sum.appendChild(el('strong', { text: n(res.total) }));
      sum.appendChild(document.createTextNode(' packets heard' + (q.f.length ? ', ' + q.f.map(filterPhrase).join(', ') : '') + '. ' +
        n(res.how.relayed) + ' passed on by repeaters where they were heard, ' + n(res.how.across) + ' heard straight across' +
        (res.how.unclear ? ', ' + n(res.how.unclear) + ' with the last repeater not placed.' : '.') + ' Select any part of the chart to list its packets.'));
    }
    lastChart = function () { var cc = cubes[current]; if (cc) renderCharts(cc, query(cc)); };
    renderCharts(c, res);
    renderLegend(res.series);
    renderRelays(summaries[current]);
    renderPivot(res);
  }

  function getJSON(url) {
    return fetch(url, { headers: { Accept: 'application/json' } }).then(function (r) {
      if (!r.ok) throw new Error('status ' + r.status);
      return r.json();
    });
  }

  // A period's summary and cube come together; both are cached server-side.
  function loadPeriod(w) {
    if (loading[w]) return;
    loading[w] = true;
    var my = gen;
    Promise.all([getJSON('/api/data/summary?window=' + encodeURIComponent(w)), getJSON('/api/data/cube?window=' + encodeURIComponent(w))]).then(function (both) {
      loading[w] = false;
      if (my !== gen) return;
      summaries[w] = both[0]; cubes[w] = both[1];
      if (w === current) render();
    }).catch(function () {
      loading[w] = false;
      if (my !== gen || !root) return;
      $('dtSum').textContent = 'The figures for this period could not be loaded. Try again shortly.';
    });
  }

  function renderLegend(series) {
    var lg = $('dtLegend');
    lg.textContent = '';
    if (q.by === 'none') return;
    series.slice().reverse().forEach(function (sr) {
      var sw = el('span', { 'class': 'dt-sw' }); sw.style.background = sr.color;
      var picked = q.pick.indexOf(sr.key) >= 0;
      var b = el('button', { type: 'button', 'class': 'dt-leg', 'aria-pressed': String(picked), title: sr.key === '__rest' ? 'Everything not shown on its own' : (picked ? 'Stop picking this out' : 'Pick this out from the rest') }, [sw, sr.label, el('b', { text: n(sr.total) })]);
      if (sr.key !== '__rest') b.addEventListener('click', function () {
        q.pick = picked ? q.pick.filter(function (v) { return v !== sr.key; }) : q.pick.concat([sr.key]);
        syncHash(); renderExplorer();
      });
      lg.appendChild(b);
    });
  }

  function relayTable(caption, rows, emptyText) {
    var t = el('table', { 'class': 'dt-t' });
    t.appendChild(el('caption', { text: caption }));
    if (!rows || !rows.length) {
      t.appendChild(el('tbody', null, [el('tr', null, [el('td', { text: emptyText })])]));
      return t;
    }
    t.appendChild(el('thead', null, [el('tr', null, [el('th', { text: 'Repeater' }), el('th', { text: 'Stands in' }), el('th', { 'class': 'dt-n', text: 'Packets' })])]));
    var tb = el('tbody');
    rows.forEach(function (r) { tb.appendChild(el('tr', null, [el('td', { text: r.name }), el('td', { 'class': 'dt-rg', text: name(r.area) }), el('td', { 'class': 'dt-n', text: n(r.packets) })])); });
    t.appendChild(tb);
    return t;
  }

  // The relay tables need one starting area and one receiving area.
  function pinned(d) {
    var f = q.f.filter(function (ff) { return ff.d === d && !ff.n && ff.v.length === 1; });
    return f.length ? f[0].v[0] : null;
  }

  function renderRelays(s) {
    var box = $('dtRelays');
    box.textContent = '';
    var o = pinned('origin'), h = pinned('heard');
    if (!s || !o || !h || o === h) return;
    var f = s.flows.filter(function (fl) { return fl.origin === o && fl.heard === h; })[0];
    if (!f) return;
    var hm = home();
    box.appendChild(el('p', { 'class': 'dt-sum', text: 'The repeaters that carried traffic from ' + name(o) + ' to ' + name(h) +
      ', across every packet type and scope in this period.' + (hm && o !== hm && h !== hm && f.viaHome ? ' ' + n(f.viaHome) + ' of its packets went through a repeater in ' + name(hm) + '.' : '') }));
    var parts = [el('div', null, [relayTable('Observers in ' + name(h) + ' heard it from', f.from, 'No repeater placed.')])];
    if (hm) {
      var cap = h === hm ? 'First repeater in ' + name(hm) + ' it reached' : (o === hm ? 'Last repeater in ' + name(hm) + ' before it left' : 'Repeater in ' + name(hm) + ' it passed through');
      parts.unshift(el('div', null, [relayTable(cap, f.gates, 'None placed in ' + name(hm) + '.')]));
    }
    box.appendChild(el('div', { 'class': 'dt-grid2' }, parts));
  }

  function tickLabel(t, step) {
    var d = new Date(t * 1000);
    if (step >= 604800) return 'w/c ' + d.toLocaleDateString([], { day: 'numeric', month: 'short', timeZone: 'UTC' });
    if (step >= 86400) return d.toLocaleDateString([], { day: 'numeric', month: 'short' });
    if (step >= 6 * 3600) return d.toLocaleDateString([], { weekday: 'short' }) + ' ' + d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
    return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
  }

  function renderPivot(res) {
    var box = $('dtTable');
    box.textContent = '';
    if (!res.series.length) return;
    var cols = res.series.slice().reverse();
    var t = el('table', { 'class': 'dt-t' });
    var hr = el('tr', null, [el('th', { text: q.x !== 'time' ? DIMS[q.x].label : 'Period' })]);
    cols.forEach(function (sr) { hr.appendChild(el('th', { 'class': 'dt-n', text: sr.label })); });
    if (cols.length > 1) hr.appendChild(el('th', { 'class': 'dt-n', text: 'Total' }));
    t.appendChild(el('thead', null, [hr]));
    var tb = el('tbody');
    var row = function (label, get, total) {
      var tr = el('tr', null, [el('td', { 'class': 'dt-code', text: label })]);
      cols.forEach(function (sr) { tr.appendChild(el('td', { 'class': 'dt-n', text: n(get(sr)) })); });
      if (cols.length > 1) tr.appendChild(el('td', { 'class': 'dt-n', text: n(total) }));
      tb.appendChild(tr);
    };
    // The table always shows counts, whatever the chart shows.
    if (q.x !== 'time') res.rowKeys.forEach(function (k) { row(res.rowName(k), function (sr) { return (sr.rowCounts || sr.rows)[k] || 0; }, res.rowTotals[k]); });
    else res.buckets.forEach(function (b, i) {
      var tot = 0; cols.forEach(function (sr) { tot += (sr.counts || sr.values)[i]; });
      if (tot) row(tickLabel(b, res.step), function (sr) { return (sr.counts || sr.values)[i]; }, tot);
    });
    row('Total', function (sr) { return sr.total; }, res.total);
    t.appendChild(tb);
    box.appendChild(t);
  }

  // ---- charts ---------------------------------------------------------
  function niceMax(v) {
    if (v <= 4) return 4;
    var p = Math.pow(10, Math.floor(Math.log10(v))), m = v / p;
    return (m <= 1 ? 1 : m <= 2 ? 2 : m <= 2.5 ? 2.5 : m <= 5 ? 5 : 10) * p;
  }
  var NS = 'http://www.w3.org/2000/svg';
  function svgEl(tag, at) { var e = document.createElementNS(NS, tag); for (var k in at) e.setAttribute(k, at[k]); return e; }
  function resolver() {
    var cs = getComputedStyle(root || document.documentElement);
    return function (c) { var m = /var\((--[^)]+)\)/.exec(c); return m ? cs.getPropertyValue(m[1]).trim() : c; };
  }
  function placeTip(tip, box, xPx, yPx) {
    var bx = box.getBoundingClientRect();
    tip.style.left = Math.min(Math.max(8, xPx - 80), Math.max(8, bx.width - 230)) + 'px';
    tip.style.top = (yPx || 0) + 'px';
  }
  function tipRows(tip, title, items) {
    tip.textContent = '';
    tip.appendChild(el('div', { 'class': 'dt-tip-h', text: title }));
    items.forEach(function (it) {
      var key = el('span', { 'class': 'dt-sw' }); key.style.background = it[2];
      tip.appendChild(el('div', { 'class': 'dt-tip-row' }, [key, el('strong', { text: it[0] }), document.createTextNode(' ' + it[1])]));
    });
    tip.hidden = false;
  }
  function empty(box) { box.textContent = ''; box.appendChild(el('p', { 'class': 'dt-empty', text: 'Nothing to draw.' })); }
  function xLabels(svg, nb, xAt, y, buckets, step) {
    var picks = nb <= 2 ? [0, nb - 1] : [0, Math.floor(nb / 2), nb - 1];
    picks.forEach(function (i, k) {
      var tx = svgEl('text', { 'class': 'dt-tick', x: xAt(i), y: y, 'text-anchor': k === 0 ? 'start' : k === picks.length - 1 ? 'end' : 'middle' });
      tx.textContent = tickLabel(buckets[i], step); svg.appendChild(tx);
    });
  }
  function yAxis(svg, top, L, R, W, y, pct) {
    [0, top / 2, top].forEach(function (v) {
      svg.appendChild(svgEl('line', { 'class': 'dt-grid', x1: L, x2: W - R, y1: y(v), y2: y(v) }));
      var tx = svgEl('text', { 'class': 'dt-tick', x: L - 6, y: y(v) + 4, 'text-anchor': 'end' }); tx.textContent = nf.format(v) + (pct ? '%' : ''); svg.appendChild(tx);
    });
  }
  // The chart takes the room the workspace has; small multiples share it.
  function chartHeight() {
    var h = Math.round(window.innerHeight * (q.panel === 'none' ? 0.42 : 0.26));
    return Math.max(220, Math.min(h, 560));
  }

  function barChart(box, res, top) {
    var series = res.series, buckets = res.buckets, share = res.pct;
    if (!series.length || !res.total) return empty(box);
    box.textContent = '';
    var W = Math.max(280, box.clientWidth), H = chartHeight(), L = 46, R = 8, T = 10, B = 26, col = resolver();
    var nb = buckets.length, cnt = function (sr, i) { return (sr.counts || sr.values)[i]; };
    var totals = buckets.map(function (_, i) { var t = 0; series.forEach(function (sr) { t += cnt(sr, i); }); return t; });
    var band = (W - L - R) / nb, cw = Math.max(1, Math.min(24, band - (band > 4 ? 2 : 0)));
    var y = function (v) { return T + (H - T - B) * (1 - v / top); };
    var svg = svgEl('svg', { viewBox: '0 0 ' + W + ' ' + H, role: 'img', 'aria-label': (share ? 'Share of packets per period' : 'Packets per period') + '; the same figures are in the table below' });
    yAxis(svg, top, L, R, W, y, share);
    xLabels(svg, nb, function (i) { return L + band * i + band / 2; }, H - 8, buckets, res.step);
    var tip = el('div', { 'class': 'dt-tip', role: 'status' }); tip.hidden = true;
    buckets.forEach(function (b, i) {
      var g = svgEl('g', { 'class': 'dt-col', tabindex: '0', role: 'button', 'aria-label': tickLabel(b, res.step) + ', ' + n(totals[i]) + ' packets: list them' });
      var hitR = svgEl('rect', { 'class': 'dt-hitr', x: L + band * i, y: T, width: band, height: H - T - B, fill: 'transparent' });
      hitR.addEventListener('click', function () { openDrill({ res: res, from: b, to: b + res.step }); });
      g.appendChild(hitR);
      g.addEventListener('keydown', function (ev) { if (ev.key === 'Enter') openDrill({ res: res, from: b, to: b + res.step }); });
      var xx = L + band * i + (band - cw) / 2, base = y(0);
      var segs = series.filter(function (sr) { return sr.values[i] > 0; });
      segs.forEach(function (sr, j) {
        var h = (H - T - B) * sr.values[i] / top, y0 = base, yt = Math.min(y0 - h, y0 - 1);
        var last = j === segs.length - 1, r = last ? Math.min(4, y0 - yt, cw / 2) : 0, gap = j > 0 && cw > 3 ? 2 : 0;
        var d = 'M' + xx + ',' + (y0 - gap) + 'V' + (yt + r) + (r ? 'Q' + xx + ',' + yt + ' ' + (xx + r) + ',' + yt + 'H' + (xx + cw - r) + 'Q' + (xx + cw) + ',' + yt + ' ' + (xx + cw) + ',' + (yt + r) : 'H' + (xx + cw)) + 'V' + (y0 - gap) + 'Z';
        var seg = svgEl('path', { 'class': 'dt-seg-r', d: d, fill: col(sr.color) });
        seg.addEventListener('click', function (ev) { ev.stopPropagation(); openDrill({ res: res, from: b, to: b + res.step, series: sr }); });
        g.appendChild(seg);
        base = yt;
      });
      var show = function () {
        tipRows(tip, tickLabel(b, res.step) + ', ' + n(totals[i]) + ' in all', series.slice().reverse().filter(function (sr) { return sr.values[i]; }).map(function (sr) {
          return [share ? Math.round(sr.values[i]) + '% (' + n(cnt(sr, i)) + ')' : n(sr.values[i]), sr.label, col(sr.color)];
        }));
        placeTip(tip, box, (L + band * i + band / 2) * box.getBoundingClientRect().width / W + 18);
      };
      g.addEventListener('pointerenter', show); g.addEventListener('focus', show);
      g.addEventListener('pointerleave', function () { tip.hidden = true; }); g.addEventListener('blur', function () { tip.hidden = true; });
      svg.appendChild(g);
    });
    box.appendChild(svg);
    box.appendChild(tip);
  }

  // Lines (one per series) or a stacked area, with a crosshair that snaps to
  // the nearest period and lists every series there.
  function lineChart(box, res, stacked, top) {
    var series = res.series, buckets = res.buckets;
    if (!series.length || !res.total) return empty(box);
    box.textContent = '';
    var W = Math.max(280, box.clientWidth), H = chartHeight(), L = 46, R = 14, T = 12, B = 26, col = resolver();
    var cnt = function (sr, i) { return (sr.counts || sr.values)[i]; };
    var nb = buckets.length, cum = buckets.map(function () { return 0; });
    var layers = series.map(function (sr) {
      var lo = cum.slice(), hi = sr.values.map(function (v, i) { return stacked ? cum[i] + v : v; });
      if (stacked) cum = hi.slice();
      return { sr: sr, lo: lo, hi: hi };
    });
    var xp = function (i) { return nb === 1 ? (L + W - R) / 2 : L + (W - L - R) * i / (nb - 1); };
    var y = function (v) { return T + (H - T - B) * (1 - v / top); };
    var svg = svgEl('svg', { viewBox: '0 0 ' + W + ' ' + H, role: 'img', 'aria-label': (stacked ? 'Stacked area' : 'Lines') + ' of packets per period; the same figures are in the table below' });
    yAxis(svg, top, L, R, W, y, res.pct);
    xLabels(svg, nb, xp, H - 8, buckets, res.step);
    var path = function (vals) { return vals.map(function (v, i) { return (i ? 'L' : 'M') + xp(i).toFixed(1) + ',' + y(v).toFixed(1); }).join(''); };
    layers.forEach(function (ly) {
      var c = col(ly.sr.color);
      if (stacked) {
        var area = path(ly.hi) + ly.lo.slice().reverse().map(function (v, j) { var i = nb - 1 - j; return 'L' + xp(i).toFixed(1) + ',' + y(v).toFixed(1); }).join('') + 'Z';
        svg.appendChild(svgEl('path', { d: area, fill: c, opacity: '0.85' }));
      } else {
        svg.appendChild(svgEl('path', { d: path(ly.hi) + 'L' + xp(nb - 1) + ',' + y(0) + 'L' + xp(0) + ',' + y(0) + 'Z', fill: c, opacity: '0.08' }));
      }
      svg.appendChild(svgEl('path', { d: path(ly.hi), fill: 'none', stroke: c, 'stroke-width': 2, 'stroke-linejoin': 'round', 'stroke-linecap': 'round' }));
      if (!stacked) svg.appendChild(svgEl('circle', { cx: xp(nb - 1), cy: y(ly.hi[nb - 1]), r: 4, fill: c, stroke: col('var(--card-bg)'), 'stroke-width': 2 }));
    });
    var cross = svgEl('line', { x1: 0, x2: 0, y1: T, y2: H - B, stroke: col('var(--text-muted)'), 'stroke-width': 1, visibility: 'hidden' });
    svg.appendChild(cross);
    var dots = svgEl('g', { visibility: 'hidden' });
    svg.appendChild(dots);
    var tip = el('div', { 'class': 'dt-tip', role: 'status' }); tip.hidden = true;
    var hit = svgEl('rect', { x: L, y: T, width: W - L - R, height: H - T - B, fill: 'transparent', tabindex: '0', 'aria-label': 'Use the arrow keys to move along the chart, Enter to list the packets' });
    var at = function (i) {
      cross.setAttribute('x1', xp(i)); cross.setAttribute('x2', xp(i)); cross.setAttribute('visibility', 'visible');
      while (dots.firstChild) dots.removeChild(dots.firstChild);
      layers.forEach(function (ly) { dots.appendChild(svgEl('circle', { cx: xp(i), cy: y(ly.hi[i]), r: 4, fill: col(ly.sr.color), stroke: col('var(--card-bg)'), 'stroke-width': 2 })); });
      dots.setAttribute('visibility', 'visible');
      var tot = 0; series.forEach(function (sr) { tot += cnt(sr, i); });
      tipRows(tip, tickLabel(buckets[i], res.step) + ', ' + n(tot) + ' in all', series.slice().reverse().map(function (sr) {
        return [res.pct ? Math.round(sr.values[i]) + '% (' + n(cnt(sr, i)) + ')' : n(sr.values[i]), sr.label, col(sr.color)];
      }));
      placeTip(tip, box, xp(i) * box.getBoundingClientRect().width / W + 18);
    };
    var idx = 0;
    hit.addEventListener('pointermove', function (ev) {
      var r = svg.getBoundingClientRect(), px = (ev.clientX - r.left) * W / r.width;
      idx = Math.max(0, Math.min(nb - 1, Math.round((px - L) / ((W - L - R) / Math.max(1, nb - 1)))));
      at(idx);
    });
    hit.addEventListener('focus', function () { at(idx); });
    hit.addEventListener('click', function () { openDrill({ res: res, from: buckets[idx], to: buckets[idx] + res.step }); });
    hit.addEventListener('keydown', function (ev) {
      if (ev.key === 'ArrowRight') { idx = Math.min(nb - 1, idx + 1); at(idx); ev.preventDefault(); }
      if (ev.key === 'ArrowLeft') { idx = Math.max(0, idx - 1); at(idx); ev.preventDefault(); }
      if (ev.key === 'Enter') openDrill({ res: res, from: buckets[idx], to: buckets[idx] + res.step });
    });
    var hide = function () { tip.hidden = true; cross.setAttribute('visibility', 'hidden'); dots.setAttribute('visibility', 'hidden'); };
    hit.addEventListener('pointerleave', hide); hit.addEventListener('blur', hide);
    svg.appendChild(hit);
    box.appendChild(svg);
    box.appendChild(tip);
  }

  // Totals: one horizontal bar per X value, stacked by the split.
  function totalsChart(box, res, top) {
    var keys = res.rowKeys.slice(0, 14);
    if (!keys.length) return empty(box);
    box.textContent = '';
    var W = Math.max(280, box.clientWidth), rowH = 30, T = 6, B = 24, col = resolver();
    var rc = function (sr, k) { return (sr.rowCounts || sr.rows)[k] || 0; };
    var labelW = Math.min(190, Math.max(110, W * 0.3)), L = labelW + 10, R = 64;
    var H = T + B + keys.length * rowH;
    var x = function (v) { return L + (W - L - R) * v / top; };
    var svg = svgEl('svg', { viewBox: '0 0 ' + W + ' ' + H, role: 'img', 'aria-label': 'Packets by ' + DIMS[q.x].label.toLowerCase() + '; the same figures are in the table below' });
    [0, top / 2, top].forEach(function (v, k) {
      svg.appendChild(svgEl('line', { 'class': 'dt-grid', x1: x(v), x2: x(v), y1: T, y2: H - B }));
      var tx = svgEl('text', { 'class': 'dt-tick', x: x(v), y: H - 6, 'text-anchor': k === 0 ? 'start' : k === 2 ? 'end' : 'middle' }); tx.textContent = nf.format(v) + (res.pct ? '%' : ''); svg.appendChild(tx);
    });
    var tip = el('div', { 'class': 'dt-tip', role: 'status' }); tip.hidden = true;
    keys.forEach(function (k, i) {
      var yy = T + i * rowH, bh = Math.min(20, rowH - 8), by = yy + (rowH - bh) / 2;
      var full = res.rowName(k);
      var label = svgEl('text', { 'class': 'dt-tick', x: labelW, y: by + bh / 2 + 4, 'text-anchor': 'end' });
      label.textContent = full.length > 26 ? full.slice(0, 25) + '…' : full;
      svg.appendChild(label);
      var g = svgEl('g', { 'class': 'dt-col', tabindex: '0', role: 'button', 'aria-label': full + ', ' + n(res.rowTotals[k]) + ' packets: list them' });
      var hitR = svgEl('rect', { 'class': 'dt-hitr', x: 0, y: yy, width: W, height: rowH, fill: 'transparent' });
      hitR.addEventListener('click', function () { openDrill({ res: res, row: k }); });
      g.appendChild(hitR);
      g.addEventListener('keydown', function (ev) { if (ev.key === 'Enter') openDrill({ res: res, row: k }); });
      var base = x(0), segs = res.series.filter(function (sr) { return sr.rows[k]; });
      segs.forEach(function (sr, j) {
        var w = (W - L - R) * sr.rows[k] / top, x0 = base + (j > 0 ? 2 : 0), x1 = Math.max(x0 + 1, base + w);
        var last = j === segs.length - 1, r = last ? Math.min(4, x1 - x0, bh / 2) : 0;
        var d = 'M' + x0 + ',' + by + 'H' + (x1 - r) + (r ? 'Q' + x1 + ',' + by + ' ' + x1 + ',' + (by + r) + 'V' + (by + bh - r) + 'Q' + x1 + ',' + (by + bh) + ' ' + (x1 - r) + ',' + (by + bh) : 'V' + (by + bh)) + 'H' + x0 + 'Z';
        var seg = svgEl('path', { 'class': 'dt-seg-r', d: d, fill: col(sr.color) });
        seg.addEventListener('click', function (ev) { ev.stopPropagation(); openDrill({ res: res, row: k, series: sr }); });
        g.appendChild(seg);
        base = x1;
      });
      var val = svgEl('text', { 'class': 'dt-tick', x: Math.min(base + 6, W - R + 4), y: by + bh / 2 + 4 }); val.textContent = n(res.rowTotals[k]); g.appendChild(val);
      var show = function () {
        tipRows(tip, full + ', ' + n(res.rowTotals[k]) + ' in all', res.series.slice().reverse().filter(function (sr) { return sr.rows[k]; }).map(function (sr) {
          return [res.pct ? Math.round(sr.rows[k]) + '% (' + n(rc(sr, k)) + ')' : n(sr.rows[k]), sr.label, col(sr.color)];
        }));
        var sx = box.getBoundingClientRect().width / W;
        placeTip(tip, box, x(res.pct ? 100 : res.rowTotals[k]) * sx, yy * sx + 30);
      };
      g.addEventListener('pointerenter', show); g.addEventListener('focus', show);
      g.addEventListener('pointerleave', function () { tip.hidden = true; }); g.addEventListener('blur', function () { tip.hidden = true; });
      svg.appendChild(g);
    });
    box.appendChild(svg);
    box.appendChild(tip);
    if (res.rowKeys.length > keys.length) box.appendChild(el('p', { 'class': 'dt-empty', text: 'The ' + (res.rowKeys.length - keys.length) + ' smallest rows are in the table below.' }));
  }

  // ---- packets behind a figure ------------------------------------------
  function shortTime(r) { var d = new Date(r.at * 1000); return d.toLocaleDateString([], { day: 'numeric', month: 'short' }) + ' ' + d.toLocaleTimeString(); }
  var COLS = {
    at: { label: 'Time', get: function (r) { return r.at; }, show: shortTime, num: true },
    hash: { label: 'Packet', get: function (r) { return r.hash; } },
    typeName: { label: 'Type', get: function (r) { return r.typeName; } },
    kind: { label: 'Kind', get: function (r) { return DIMS.kind.name(r.kind); } },
    routing: { label: 'Routing', get: function (r) { return DIMS.routing.name(r.routing); } },
    scope: { label: 'Scope', get: function (r) { return r.scope; } },
    scopeClass: { label: 'Scope kind', get: function (r) { return SCOPE_KIND[r.scopeClass] || r.scopeClass; } },
    channel: { label: 'Channel', get: function (r) { return r.channel ? DIMS.channel.name(r.channel) : ''; } },
    border: { label: 'Crossed a border', get: function (r) { return DIMS.border.name(r.border); } },
    origin: { label: 'From', get: function (r) { return name(r.origin); } },
    basis: { label: 'Start placed by', get: function (r) { return BASIS[r.basis] || r.basis; } },
    heard: { label: 'Heard in', get: function (r) { return name(r.heard); } },
    through: { label: 'Through', get: function (r) { return DIMS.through.name(r.through); } },
    how: { label: 'How it arrived', get: function (r) { return HOW_LABEL[r.how] || r.how; } },
    observers: { label: 'Observers', get: function (r) { return r.observers; }, num: true },
    heardFrom: { label: 'Heard from', get: function (r) { return r.heardFrom || ''; } },
    boundary: { label: 'Boundary repeater', get: function (r) { return r.boundary || ''; } }
  };
  var COL_ORDER = ['at', 'hash', 'typeName', 'kind', 'routing', 'scope', 'scopeClass', 'channel', 'border', 'origin', 'basis', 'heard', 'through', 'how', 'observers', 'heardFrom', 'boundary'];
  var DEFAULT_COLS = ['at', 'typeName', 'scope', 'origin', 'heard', 'how', 'hash'];
  var COLS_KEY = 'corescope-data-cols';
  var cols = (function () {
    try { var c = JSON.parse(localStorage.getItem(COLS_KEY) || 'null'); if (Array.isArray(c) && c.length && c.every(function (k) { return COLS[k]; })) return c; } catch (e) { /* defaults */ }
    return DEFAULT_COLS.slice();
  })();
  function saveCols() { try { localStorage.setItem(COLS_KEY, JSON.stringify(cols)); } catch (e) { /* not fatal */ } }
  var drill = { rows: [], total: 0, sort: 'at', dir: -1, shown: 200, title: '', filters: [] };

  // A click on the chart becomes filters: the explorer's own, plus the panel,
  // the series and the row that were clicked. "Everything else" is every value
  // not shown on its own.
  function drillFilters(sel) {
    var fs = JSON.parse(JSON.stringify(q.f));
    var exact = function (d) { return d === 'through' ? 1 : 0; };
    if (q.panel !== 'none' && sel.res.panelValue !== undefined) fs.push({ d: q.panel, n: 0, v: [sel.res.panelValue], x: exact(q.panel) });
    if (sel.row !== undefined) fs.push({ d: q.x, n: 0, v: [sel.row], x: exact(q.x) });
    if (sel.series && q.by !== 'none') {
      if (sel.series.key === '__rest') {
        var kept = sel.res.series.filter(function (sr) { return sr.key !== '__rest'; }).map(function (sr) { return sr.key; });
        if (kept.length) fs.push({ d: q.by, n: 1, v: kept, x: exact(q.by) });
      } else fs.push({ d: q.by, n: 0, v: [sel.series.key], x: exact(q.by) });
    }
    return fs;
  }

  function drillTitle(sel) {
    var parts = [];
    if (sel.from !== undefined) parts.push(tickLabel(sel.from, sel.res.step));
    if (q.panel !== 'none' && sel.res.panelValue !== undefined) parts.push(DIMS[q.panel].name(sel.res.panelValue));
    if (sel.row !== undefined) parts.push(DIMS[q.x].name(sel.row));
    if (sel.series && q.by !== 'none') parts.push(sel.series.label);
    return parts.join(', ');
  }

  function openDrill(sel) {
    var dlg = $('dtModal');
    drill.filters = drillFilters(sel);
    drill.title = drillTitle(sel);
    drill.rows = []; drill.total = 0; drill.shown = 200;
    renderDrill(true);
    if (!dlg.open) { if (dlg.showModal) dlg.showModal(); else dlg.setAttribute('open', ''); }
    var params = 'window=' + encodeURIComponent(current) + '&f=' + encodeURIComponent(JSON.stringify(drill.filters)) +
      (sel.from !== undefined ? '&from=' + sel.from + '&to=' + sel.to : '');
    var my = gen;
    getJSON('/api/data/packets?' + params).then(function (d) {
      if (my !== gen) return;
      drill.rows = d.rows; drill.total = d.total;
      renderDrill(false);
    }).catch(function () {
      if (root) $('dtModalSum').textContent = 'The packets could not be loaded. Try again shortly.';
    });
  }

  function renderDrill(isLoading) {
    $('dtModalTitle').textContent = drill.title || 'Packets';
    var sum = $('dtModalSum');
    var phrases = drill.filters.filter(function (f) { return DIMS[f.d]; }).map(filterPhrase);
    if (isLoading) sum.textContent = 'Loading the packets behind this figure…';
    else {
      sum.textContent = n(drill.total) + ' packets heard over the ' + windowLabel(current) + (phrases.length ? ': ' + phrases.join(', ') : '') + '.' +
        (drill.total > drill.rows.length ? ' Showing the ' + n(drill.rows.length) + ' newest; narrow the selection to see the rest.' : '') +
        ' Select a packet to open it.';
    }
    renderColChips();
    renderDrillTable();
  }

  function renderColChips() {
    var box = $('dtModalCols');
    box.textContent = '';
    box.appendChild(el('span', { 'class': 'dt-picks-k', text: 'Columns' }));
    cols.forEach(function (k, i) {
      var chip = el('span', { 'class': 'dt-fchip' });
      chip.appendChild(el('span', { 'class': 'dt-fchip-edit', text: COLS[k].label }));
      var del = el('button', { type: 'button', 'class': 'dt-fchip-x', 'aria-label': 'Remove the ' + COLS[k].label + ' column', text: '×' });
      if (cols.length === 1) del.disabled = true;
      del.addEventListener('click', function () { cols.splice(i, 1); saveCols(); renderColChips(); renderDrillTable(); });
      chip.appendChild(del);
      box.appendChild(chip);
    });
    var free = COL_ORDER.filter(function (k) { return cols.indexOf(k) < 0; });
    if (free.length) {
      var sel = el('select', { 'class': 'dt-addcol', 'aria-label': 'Add a column' });
      sel.appendChild(el('option', { value: '', text: '+ Add column' }));
      free.forEach(function (k) { sel.appendChild(el('option', { value: k, text: COLS[k].label })); });
      sel.addEventListener('change', function () { if (sel.value) { cols.push(sel.value); saveCols(); renderColChips(); renderDrillTable(); } });
      box.appendChild(sel);
    }
    var reset = el('button', { type: 'button', 'class': 'dt-link', text: 'Default columns' });
    reset.addEventListener('click', function () { cols = DEFAULT_COLS.slice(); saveCols(); renderColChips(); renderDrillTable(); });
    box.appendChild(reset);
  }

  function packetHref(hash) { return '#/packets/' + encodeURIComponent(hash); }

  function renderDrillTable() {
    var box = $('dtModalTable');
    box.textContent = '';
    if (!drill.rows.length) return;
    var C = COLS[drill.sort] || COLS.at;
    var rows = drill.rows.slice().sort(function (a, b) {
      var x1 = C.get(a), x2 = C.get(b);
      if (C.num) return (x1 - x2) * drill.dir;
      return String(x1).localeCompare(String(x2), undefined, { numeric: true }) * drill.dir;
    });
    var t = el('table', { 'class': 'dt-t dt-drill-t' });
    var hr = el('tr');
    cols.forEach(function (k) {
      var on = drill.sort === k;
      var th = el('th', { scope: 'col', 'aria-sort': on ? (drill.dir > 0 ? 'ascending' : 'descending') : 'none', 'class': COLS[k].num ? 'dt-n' : '' });
      var b = el('button', { type: 'button', 'class': 'dt-sort', text: COLS[k].label + (on ? (drill.dir > 0 ? ' ↑' : ' ↓') : '') });
      b.addEventListener('click', function () {
        if (drill.sort === k) drill.dir = -drill.dir; else { drill.sort = k; drill.dir = COLS[k].num ? -1 : 1; }
        renderDrillTable();
      });
      th.appendChild(b);
      hr.appendChild(th);
    });
    t.appendChild(el('thead', null, [hr]));
    var tb = el('tbody');
    rows.slice(0, drill.shown).forEach(function (r) {
      var href = packetHref(r.hash);
      var tr = el('tr', { 'class': 'dt-link-row' });
      cols.forEach(function (k) {
        var C2 = COLS[k], text = C2.show ? C2.show(r) : String(C2.get(r));
        var td = el('td', { 'class': (C2.num ? 'dt-n ' : '') + (k === 'hash' ? 'dt-code' : '') });
        if (k === 'hash') td.appendChild(el('a', { href: href, text: text }));
        else td.textContent = text;
        tr.appendChild(td);
      });
      // The packet opens in CoreScope's own packet view; Back returns here
      // with the same question, since it lives in the address.
      tr.addEventListener('click', function (ev) {
        if (ev.target.tagName === 'A') { $('dtModal').close(); return; }
        $('dtModal').close();
        location.hash = href;
      });
      tb.appendChild(tr);
    });
    t.appendChild(tb);
    box.appendChild(t);
    if (rows.length > drill.shown) {
      var more = el('button', { type: 'button', 'class': 'dt-btn dt-more-btn', text: 'Show ' + n(Math.min(200, rows.length - drill.shown)) + ' more of ' + n(rows.length) });
      more.addEventListener('click', function () { drill.shown += 200; renderDrillTable(); });
      box.appendChild(more);
    }
  }

  // ---- page -----------------------------------------------------------
  function render() {
    if (!root) return;
    var s = summaries[current];
    renderWindows();
    renderCover(s);
    renderTiles(s);
    renderGroups();
    renderMatrix(s);
    renderExplorer();
  }

  function setFull(on) {
    document.body.classList.toggle('dt-full', on);
    var b = $('dtFill');
    if (b) { b.setAttribute('aria-pressed', String(on)); b.textContent = on ? 'Exit full screen' : 'Full screen'; }
    requestAnimationFrame(function () { if (lastChart) lastChart(); });
  }

  function showOff(container) {
    container.textContent = '';
    var box = el('div', { 'class': 'dt-off' }, [
      el('h2', { text: 'Data' }),
      el('p', { text: 'The Data tab is not switched on for this CoreScope. An operator turns it on with data.enabled and an areas file in config.json; see config.example.json.' })
    ]);
    container.appendChild(box);
  }

  function init(container) {
    gen++;
    root = null; cfg = null; summaries = {}; cubes = {}; loading = {}; lastChart = null;
    var my = gen;
    container.textContent = '';
    container.appendChild(el('div', { 'class': 'dt-off', text: 'Loading…' }));
    getJSON('/api/data/config').then(function (c) {
      if (my !== gen) return;
      if (!c.enabled) { showOff(container); return; }
      cfg = c;
      cfg.windows = cfg.windows || [];
      areas = {}; areaSlot = {};
      (cfg.areas || []).forEach(function (a, i) { areas[a.code] = a; if (i < SLOTS.length) areaSlot[a.code] = i; });
      if (!cfg.windows.length) { showOff(container); return; }
      container.innerHTML = pageHtml();
      root = $('dtPage');
      readHash();
      // On a phone the presets and chart options fold away under the
      // filters, so the chart is on the first screen.
      $('dtRailToggle').addEventListener('click', function () {
        var open = $('dtRailToggle').getAttribute('aria-expanded') !== 'true';
        $('dtRailToggle').setAttribute('aria-expanded', String(open));
        root.classList.toggle('dt-rail-open', open);
      });
      $('dtFill').addEventListener('click', function () { setFull(!document.body.classList.contains('dt-full')); });
      $('dtModalClose').addEventListener('click', function () { $('dtModal').close(); });
      $('dtModal').addEventListener('click', function (ev) { if (ev.target === ev.currentTarget) ev.currentTarget.close(); });
      if (c.filling) $('dtCover').textContent = 'The index is filling; recent periods are ready first.';
      render();
    }).catch(function () {
      if (my !== gen) return;
      container.textContent = '';
      container.appendChild(el('div', { 'class': 'dt-off', text: 'The Data tab could not be loaded. Try again shortly.' }));
    });
    resizeHandler = function () {
      clearTimeout(resizeTimer);
      resizeTimer = setTimeout(function () { if (lastChart) lastChart(); }, 150);
    };
    window.addEventListener('resize', resizeHandler);
    keyHandler = function (ev) {
      if (ev.key !== 'Escape' || !root) return;
      var dlg = $('dtModal');
      if (dlg && dlg.open) return;
      var ed = $('dtEditor');
      if (ed && !ed.hidden) { closeEditor(); return; }
      if (document.body.classList.contains('dt-full')) setFull(false);
    };
    document.addEventListener('keydown', keyHandler);
  }

  function destroy() {
    gen++;
    root = null;
    document.body.classList.remove('dt-full');
    if (resizeHandler) window.removeEventListener('resize', resizeHandler);
    if (keyHandler) document.removeEventListener('keydown', keyHandler);
    resizeHandler = keyHandler = null;
    clearTimeout(resizeTimer);
  }

  if (typeof window !== 'undefined') {
    // For the helper tests: the pieces whose answers must match the server's.
    window.__corescopeDataInternals = { DIMS: DIMS, rolledBuckets: rolledBuckets, niceMax: niceMax, drillFilters: drillFilters, setState: function (c, query0) { cfg = c; q = query0; } };
  }

  registerPage('data', { init: init, destroy: destroy });
})();
