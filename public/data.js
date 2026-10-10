/* === CoreScope — data.js ===
   The Data tab (route #/data): dashboards of panels over every packet of the
   last few days, each packet placed by the area it STARTED in and the areas
   whose observers heard it.

   A dashboard is a schema-1 JSON document (see GET /api/data/dashboards):
   panels, each a query over the period's cube, a panel type and a place on a
   12-column grid, plus optional $variables. The built-in and operator
   dashboards come from the server; a visitor's own live in their browser and
   can be exported, imported and shared as a link. Editing a built-in or
   operator dashboard edits a copy.

   Data: /api/data/config and /api/data/dashboards once, /api/data/cube per
   period on screen, /api/data/packets for the packets behind a figure. Area
   names come from the areas file and node names off the mesh, so everything
   is written with textContent. */
'use strict';
(function () {
  // Cube row layout, as internal/dataindex writes it.
  var I = { b: 0, o: 1, h: 2, t: 3, s: 4, w: 5, rt: 6, x: 7, ch: 8, n: 9 };
  var SLOTS = ['var(--dt-c1)', 'var(--dt-c2)', 'var(--dt-c3)', 'var(--dt-c4)', 'var(--dt-c5)', 'var(--dt-c6)', 'var(--dt-c7)'];
  var REST = 'var(--dt-c-rest)';
  var MAX_SERIES = 6, MAX_FACETS = 6, MAX_PANELS = 24, UNDO_DEPTH = 20, MAX_LINK = 8000;
  var TYPE_SLOT = { 4: 0, 5: 1, 2: 2, 0: 3, 1: 4, 8: 5, 3: 6 };
  var HOW_LABEL = { relayed: 'Relayed by repeaters there', across: 'Heard straight across', unclear: 'Last repeater not placed' };
  var SCOPE_KIND = { scoped: 'A region scope', unscoped: 'Unscoped flood', unrecognised: 'Code nobody here can name', direct: 'Direct route' };
  var BASIS = { advert: 'Its advert', node: 'Its node record', 'first-relay': 'First repeater', 'zero-hop': 'Heard direct', unknown: 'Not placed' };
  var ROLLUPS = {
    '24h': [['10m', '10 minutes', 600], ['30m', '30 minutes', 1800], ['1h', 'Hour', 3600]],
    '7d': [['1h', 'Hour', 3600], ['6h', '6 hours', 21600], ['1d', 'Day', 86400]],
    '30d': [['6h', '6 hours', 21600], ['1d', 'Day', 86400], ['1w', 'Week', 604800]]
  };
  var DEFAULT_ROLL = { '24h': '1h', '7d': '6h', '30d': '1d' };
  var DIM_ORDER = ['border', 'origin', 'heard', 'through', 'kind', 'type', 'channel', 'scope', 'sclass', 'how', 'routing'];
  var PANEL_TYPES = [
    ['bar', 'Bars', 'Counts over time or across a dimension, split into series'],
    ['line', 'Lines', 'One line per series over time'],
    ['area', 'Area', 'Stacked area over time'],
    ['matrix', 'Matrix', 'Any two dimensions as a shaded grid'],
    ['stat', 'Stat', 'One number with its trend'],
    ['table', 'Table', 'Rows by columns, with totals'],
    ['top', 'Top list', 'The biggest values of one dimension'],
    ['text', 'Text', 'A short note for readers']
  ];
  var STORE_KEY = 'corescope-data-dashboards';

  var nf = new Intl.NumberFormat();
  var cfg = null, library = null, mine = [], shared = null;
  var cubes = {}, loading = {}, gen = 0, root = null;
  var state = { id: '', window: '', vars: {}, edit: false, sel: null };
  var undo = [];
  var redrawers = [], resizeTimer = null, keyHandler = null, resizeHandler = null;

  function $(id) { return document.getElementById(id); }
  function el(tag, attrs, kids) {
    var e = document.createElement(tag);
    if (attrs) for (var k in attrs) {
      if (k === 'text') e.textContent = attrs[k];
      else if (attrs[k] !== undefined && attrs[k] !== null) e.setAttribute(k, attrs[k]);
    }
    (kids || []).forEach(function (c) { if (c) e.appendChild(typeof c === 'string' ? document.createTextNode(c) : c); });
    return e;
  }
  function n(v) { return nf.format(Math.round(v || 0)); }
  function clone(o) { return JSON.parse(JSON.stringify(o)); }
  function areaName(code) {
    if (code === 'unknown') return 'Not known';
    var a = cfg && (cfg.areas || []).filter(function (x) { return x.code === code; })[0];
    return a ? a.label : code;
  }
  function typeName(t) { return (cfg.typeNames && cfg.typeNames[t]) || ('Type ' + t); }
  function windowLabel(w) {
    var x = (cfg.windows || []).filter(function (v) { return v.key === w; })[0];
    return x ? x.label.toLowerCase() : w;
  }
  function windowKeys() { return (cfg.windows || []).map(function (w) { return w.key; }); }
  // Without areas (config places false) the place dimensions mean nothing:
  // every packet starts and is heard in "unknown".
  var PLACE_DIMS = { border: true, origin: true, heard: true, through: true, how: true };
  function places() { return !cfg || cfg.places !== false; }
  function hasDim(k) { return !!DIMS[k] && (places() || !PLACE_DIMS[k]); }
  // placeUse names the first thing in d that needs areas, or ''.
  function placeUse(d) {
    var v = (d.variables || []).filter(function (x) { return x.kind === 'area'; })[0];
    if (v) return 'variable $' + v.name + ' is an area';
    for (var i = 0; i < (d.panels || []).length; i++) { var u = panelPlaceUse(d.panels[i]); if (u) return 'panel "' + d.panels[i].id + '" ' + u; }
    return '';
  }
  function panelPlaceUse(p) {
    var q = p.query || {}, dims = [q.x, q.split, q.facet, q.rows, q.cols, q.dim];
    for (var i = 0; i < dims.length; i++) if (PLACE_DIMS[dims[i]]) return 'uses ' + DIMS[dims[i]].label;
    for (var j = 0; j < (q.filters || []).length; j++) if (PLACE_DIMS[q.filters[j].d]) return 'filters on ' + DIMS[q.filters[j].d].label;
    return '';
  }

  // ---- dimensions -----------------------------------------------------
  var areaSlot = {};
  function scopeSlot(v) { var c = cubes[state.window]; if (!c) return undefined; for (var i = 0; i < c.scopes.length && i < SLOTS.length; i++) if (c.scopes[i].key === v) return i; return undefined; }
  var DIMS = {
    border: {
      label: 'Crossed a border',
      values: function () { return ['crossed', 'local', 'unknown']; },
      of: function (c, r) { var o = c.origins[r[I.o]]; return o === 'unknown' ? 'unknown' : (o === c.heard[r[I.h]] ? 'local' : 'crossed'); },
      name: function (v) { return { crossed: 'Yes', local: 'No, heard where it started', unknown: 'Start not known' }[v] || v; },
      slot: function (v) { return { crossed: 1, local: 0, unknown: -2 }[v]; }
    },
    origin: { label: 'From', area: true, values: function (c) { return c.origins; }, of: function (c, r) { return c.origins[r[I.o]]; }, name: areaName, slot: function (v) { return areaSlot[v]; } },
    heard: { label: 'Heard in', area: true, values: function (c) { return c.heard; }, of: function (c, r) { return c.heard[r[I.h]]; }, name: areaName, slot: function (v) { return areaSlot[v]; } },
    through: {
      label: 'Through',
      values: function (c) {
        var seen = {};
        c.through.forEach(function (t) { (t ? t.split('+') : ['straight']).forEach(function (a) { seen[a] = true; }); });
        return Object.keys(seen);
      },
      of: function (c, r) { return c.through[r[I.x]] || 'straight'; },
      // A filter on Through matches a packet that passed through ANY of the
      // chosen areas; x (exact) matches the whole route instead.
      match: function (v, vals) { return v.split('+').some(function (a) { return vals.indexOf(a) >= 0; }); },
      name: function (v) { return v === 'straight' ? 'Straight between the two' : v.split('+').map(areaName).join(' and '); },
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
    scope: { label: 'Scope', values: function (c) { return c.scopes.map(function (sc) { return sc.key; }); }, of: function (c, r) { return c.scopes[r[I.s]].key; }, name: function (v) { return v; }, slot: scopeSlot },
    sclass: { label: 'Scope kind', values: function () { return ['scoped', 'unscoped', 'unrecognised', 'direct']; }, of: function (c, r) { return c.scopes[r[I.s]].class; }, name: function (v) { return SCOPE_KIND[v] || v; }, slot: function (v) { return { scoped: 0, unscoped: 1, unrecognised: -2, direct: 3 }[v]; } },
    how: { label: 'How it arrived', values: function (c) { return c.how; }, of: function (c, r) { return c.how[r[I.w]]; }, name: function (v) { return HOW_LABEL[v] || v; }, slot: function (v) { return { relayed: 0, across: 1, unclear: -2 }[v]; } },
    routing: { label: 'Routing', values: function () { return ['flood', 'direct']; }, of: function (c, r) { var rt = c.routes[r[I.rt]]; return rt === 0 || rt === 1 ? 'flood' : 'direct'; }, name: function (v) { return v === 'flood' ? 'Flooded' : 'Routed direct'; }, slot: function (v) { return v === 'flood' ? 0 : 1; } }
  };
  function colourFor(D, v) {
    if (v === '__rest') return REST;
    var sl = D && D.slot ? D.slot(v) : undefined;
    return sl === -2 ? REST : (sl >= 0 ? SLOTS[sl] : undefined);
  }

  // ---- variables --------------------------------------------------------
  function dashVars(d) { return (d && d.variables) || []; }
  function varValue(v) {
    if (state.vars[v.name] !== undefined) return state.vars[v.name];
    if (v.default) return v.default;
    if (v.kind === 'area') return cfg.home || ((cfg.areas || [])[0] || {}).code || '';
    return '';
  }
  function varOf(d, k) { return dashVars(d).filter(function (x) { return x.name === k; })[0]; }
  function subst(s, d) {
    return String(s).replace(/\$([a-z][a-z0-9_]*)/g, function (m, k) { var v = varOf(d, k); return v ? varValue(v) : m; });
  }
  function substTitle(s, d) {
    return String(s).replace(/\$([a-z][a-z0-9_]*)/g, function (m, k) {
      var v = varOf(d, k);
      if (!v) return m;
      var val = varValue(v);
      return v.kind === 'area' ? areaName(val) : val;
    });
  }
  function panelFilters(p, d) {
    return ((p.query && p.query.filters) || []).map(function (f) {
      var o = { d: f.d, n: f.n || 0, v: f.v.map(function (v) { return subst(v, d); }) };
      if (f.x) o.x = f.x;
      return o;
    });
  }

  // ---- validation (the same rules the server applies) --------------------
  var ID_RE = /^[a-z0-9][a-z0-9-]{0,63}$/, VAR_RE = /^[a-z][a-z0-9_]{0,31}$/;
  function validatePanel(p, ids) {
    if (!p || !ID_RE.test(p.id || '') || ids[p.id]) return 'Panel ids must be unique lower-case letters, digits and dashes.';
    ids[p.id] = true;
    if (!PANEL_TYPES.some(function (t) { return t[0] === p.type; })) return 'Unknown panel type "' + p.type + '".';
    var q = p.query || {};
    if (q.window && !ROLLUPS[q.window]) return 'Panel "' + p.id + '" has an unknown period.';
    for (var k = 0; k < (q.filters || []).length; k++) {
      var f = q.filters[k];
      if (!f || !DIMS[f.d] || !Array.isArray(f.v)) return 'Unknown filter dimension "' + (f && f.d) + '".';
    }
    var dims = [q.split, q.facet, q.rows, q.cols, q.dim, q.x === 'time' ? '' : q.x];
    for (var m = 0; m < dims.length; m++) if (dims[m] && !DIMS[dims[m]]) return 'Unknown dimension "' + dims[m] + '".';
    var l = p.layout || {};
    if (!(l.w >= 1 && l.w <= 12 && l.h >= 1 && l.h <= 12 && l.x >= 0 && l.x + l.w <= 12 && l.y >= 0)) return 'Panel "' + p.id + '" must fit the 12-column grid.';
    return '';
  }
  function validateDashboard(d) {
    if (!d || typeof d !== 'object' || Array.isArray(d)) return 'Expected a dashboard object.';
    if (d.schema !== 1) return 'This file uses schema ' + d.schema + '; this CoreScope reads schema 1.';
    if (!ID_RE.test(d.id || '')) return 'The dashboard id must be lower-case letters, digits and dashes.';
    if (!d.title) return 'The dashboard needs a title.';
    if (!Array.isArray(d.panels)) return 'The dashboard needs a list of panels.';
    if (d.panels.length > MAX_PANELS) return 'At most ' + MAX_PANELS + ' panels per dashboard.';
    if (d.window && !ROLLUPS[d.window]) return 'The period must be 24h, 7d or 30d.';
    var vn = {};
    for (var i = 0; i < (d.variables || []).length; i++) {
      var v = d.variables[i];
      if (!v || !VAR_RE.test(v.name || '') || ['area', 'scope', 'channel', 'text'].indexOf(v.kind) < 0 || vn[v.name]) return 'Variable "' + (v && v.name) + '" needs a unique lower-case name and a kind of area, scope, channel or text.';
      vn[v.name] = true;
    }
    var ids = {};
    for (var j = 0; j < d.panels.length; j++) { var bad = validatePanel(d.panels[j], ids); if (bad) return bad; }
    var use = places() ? '' : placeUse(d);
    if (use) return 'This dashboard needs areas, which this CoreScope does not have: ' + use + '.';
    return '';
  }

  // ---- grid layout ------------------------------------------------------
  function overlaps(a, b) { return a.x < b.x + b.w && b.x < a.x + a.w && a.y < b.y + b.h && b.y < a.y + a.h; }
  // resolveLayout keeps `fixed` where it was put and moves every other
  // panel down until nothing overlaps, then lifts everything as far up as
  // it can go: panels never sit on each other or leave holes above them.
  function resolveLayout(panels, fixed) {
    var order = panels.slice().sort(function (a, b) { return (a === fixed ? -1 : b === fixed ? 1 : 0) || a.layout.y - b.layout.y || a.layout.x - b.layout.x; });
    var placed = [];
    order.forEach(function (p) {
      var l = p.layout;
      l.w = Math.max(1, Math.min(12, l.w));
      l.x = Math.max(0, Math.min(12 - l.w, l.x));
      l.y = Math.max(0, l.y);
      while (placed.some(function (q) { return overlaps(l, q.layout); })) l.y++;
      placed.push(p);
    });
    placed.sort(function (a, b) { return a.layout.y - b.layout.y || a.layout.x - b.layout.x; });
    var settled = [];
    placed.forEach(function (p) {
      var l = p.layout;
      while (l.y > 0) {
        var up = { x: l.x, y: l.y - 1, w: l.w, h: l.h };
        if (settled.some(function (q) { return overlaps(up, q.layout); })) break;
        l.y--;
      }
      settled.push(p);
    });
    return panels;
  }
  function nextFreeY(panels) { return panels.reduce(function (m, p) { return Math.max(m, p.layout.y + p.layout.h); }, 0); }

  // ---- library ----------------------------------------------------------
  function stripSource(d) { var c = clone(d); delete c.source; return c; }
  function loadMine() {
    try {
      var v = JSON.parse(localStorage.getItem(STORE_KEY) || '[]');
      return Array.isArray(v) ? v.filter(function (d) { var bad = validateDashboard(d); return !bad || /needs areas/.test(bad); }).map(function (d) { d.source = 'mine'; return d; }) : [];
    } catch (e) { return []; }
  }
  function saveMine() { try { localStorage.setItem(STORE_KEY, JSON.stringify(mine.map(stripSource))); } catch (e) { /* storage off: kept for this visit */ } }
  function all() { return (library ? library.dashboards : []).concat(mine).concat(shared ? [shared] : []); }
  function cur() { var list = all(); return list.filter(function (d) { return d.id === state.id; })[0] || list[0]; }
  function editable(d) { return d && d.source === 'mine'; }
  function uniqueId(base) { var b = base.slice(0, 56), id = b, i = 2; while (all().some(function (d) { return d.id === id; })) id = b + '-' + i++; return id; }
  function persist() { if (editable(cur())) saveMine(); }
  function snapshot() { if (!editable(cur())) return; undo.push(JSON.stringify(cur())); if (undo.length > UNDO_DEPTH) undo.shift(); }
  function undoLast() {
    var d = cur(); if (!undo.length || !editable(d)) return;
    var prev = JSON.parse(undo.pop()); prev.source = 'mine';
    mine = mine.map(function (x) { return x.id === d.id ? prev : x; });
    if (state.sel && !prev.panels.some(function (p) { return p.id === state.sel; })) state.sel = null;
    saveMine(); render(); toast('Undone');
  }
  function copyToMine(d) {
    var c = stripSource(d);
    c.id = uniqueId((d.id || 'dashboard').replace(/^shared-/, '') + '-copy');
    c.title = d.title + ' (my copy)';
    c.source = 'mine';
    mine.push(c); saveMine();
    if (d.source === 'shared') shared = null;
    state.id = c.id;
    return c;
  }

  // ---- address ------------------------------------------------------------
  function b64urlEncode(s) { return btoa(unescape(encodeURIComponent(s))).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, ''); }
  function b64urlDecode(s) { s = s.replace(/-/g, '+').replace(/_/g, '/'); while (s.length % 4) s += '='; return decodeURIComponent(escape(atob(s))); }
  function syncHash() {
    var p = new URLSearchParams(), d = cur();
    if (d && d.source === 'shared') p.set('d', b64urlEncode(JSON.stringify(exportDoc(d))));
    else if (d) p.set('dash', d.id);
    if (state.window) p.set('w', state.window);
    dashVars(d).forEach(function (v) { if (state.vars[v.name] !== undefined) p.set('v.' + v.name, state.vars[v.name]); });
    try { history.replaceState(null, '', '#/data?' + p.toString()); } catch (e) { /* not fatal */ }
  }
  function readHash() {
    var p = new URLSearchParams(location.hash.split('?')[1] || ''), keys = windowKeys();
    if (p.get('d')) {
      try {
        var doc = JSON.parse(b64urlDecode(p.get('d')));
        var bad = validateDashboard(doc);
        if (bad && /needs areas/.test(bad)) bad = '';
        if (bad) toast('The shared dashboard could not be opened: ' + bad);
        else { doc.source = 'shared'; doc.id = 'shared-' + doc.id.slice(0, 56); shared = doc; state.id = doc.id; }
      } catch (e) { toast('The shared link is damaged; it could not be read.'); }
    }
    if (!shared) state.id = p.get('dash') || library.default;
    var d = cur();
    state.window = keys.indexOf(p.get('w')) >= 0 ? p.get('w') : (keys.indexOf(d.window) >= 0 ? d.window : keys[0]);
    state.vars = {};
    p.forEach(function (v, k) { if (k.indexOf('v.') === 0) state.vars[k.slice(2)] = v; });
  }

  // ---- data -------------------------------------------------------------
  function getJSON(url) {
    return fetch(url, { headers: { Accept: 'application/json' } }).then(function (r) {
      if (!r.ok) throw new Error('status ' + r.status);
      return r.json();
    });
  }
  function windowsNeeded(d) {
    var need = {}, keys = windowKeys(); need[state.window] = true;
    d.panels.forEach(function (p) { if (p.query && keys.indexOf(p.query.window) >= 0) need[p.query.window] = true; });
    return Object.keys(need);
  }
  function ensureCubes(d) {
    var missing = windowsNeeded(d).filter(function (w) { return !cubes[w]; });
    if (!missing.length) return Promise.resolve();
    return Promise.all(missing.map(function (w) {
      if (!loading[w]) {
        loading[w] = getJSON('/api/data/cube?window=' + encodeURIComponent(w)).then(
          function (c) { cubes[w] = c; loading[w] = null; },
          function (e) { loading[w] = null; throw e; });
      }
      return loading[w];
    }));
  }
  function panelWindow(p) { return (p.query && p.query.window && windowKeys().indexOf(p.query.window) >= 0) ? p.query.window : state.window; }

  // ---- queries -----------------------------------------------------------
  function passes(c, r, filters) {
    for (var i = 0; i < filters.length; i++) {
      var f = filters[i], D = DIMS[f.d];
      if (!D) continue;
      var v = D.of(c, r);
      var hit = D.match && !f.x ? D.match(v, f.v) : f.v.indexOf(v) >= 0;
      if (hit === !!f.n) return false;
    }
    return true;
  }
  function filteredRows(c, filters) { return filters.length ? c.rows.filter(function (r) { return passes(c, r, filters); }) : c.rows; }
  function rollFor(w, roll) {
    var opts = ROLLUPS[w] || ROLLUPS['24h'], want = roll || DEFAULT_ROLL[w];
    return opts.filter(function (o) { return o[0] === want; })[0] || opts.filter(function (o) { return o[0] === DEFAULT_ROLL[w]; })[0] || opts[0];
  }
  // Buckets rolled up to the chosen step: weeks start on Monday, UTC.
  function rolledBuckets(c, step) {
    var keys = [], idx = {}, map = [];
    c.buckets.forEach(function (t, i) {
      var day = Math.floor(t / 86400);
      var k = step === 604800 ? (day - (day + 3) % 7) * 86400 : t - t % step;
      if (!(k in idx)) { idx[k] = keys.length; keys.push(k); }
      map[i] = idx[k];
    });
    return { keys: keys, map: map };
  }
  // seriesQuery builds one chart's figures: series by the split, over time
  // or across the X dimension; the six biggest (or the picked ones) on their
  // own and the rest together. Colour follows the value, never its rank.
  function seriesQuery(c, rows, q, w) {
    var D = q.split ? DIMS[q.split] : null;
    var X = q.x && q.x !== 'time' ? DIMS[q.x] : null;
    var roll = rollFor(w, q.rollup), rb = rolledBuckets(c, roll[2]);
    var zero = function () { return rb.keys.map(function () { return 0; }); };
    var raw = {}, total = 0, xTotals = {};
    rows.forEach(function (r) {
      var key = D ? D.of(c, r) : 'all', v = r[I.n];
      var s = raw[key] || (raw[key] = { key: key, label: D ? D.name(key) : 'Packets', values: zero(), byX: {}, total: 0 });
      s.values[rb.map[r[I.b]]] += v; s.total += v; total += v;
      if (X) { var xk = X.of(c, r); s.byX[xk] = (s.byX[xk] || 0) + v; xTotals[xk] = (xTotals[xk] || 0) + v; }
    });
    var list = Object.keys(raw).map(function (k) { return raw[k]; }).sort(function (a, b) { return b.total - a.total; });
    var pick = q.pick || [];
    var keep = pick.length ? function (s) { return pick.indexOf(s.key) >= 0; } : function (s, i) { return i < MAX_SERIES || list.length === MAX_SERIES + 1; };
    var kept = [], rest = null;
    list.forEach(function (s, i) {
      if (!D || keep(s, i)) { kept.push(s); return; }
      if (!rest) rest = { key: '__rest', label: 'Everything else', values: zero(), byX: {}, total: 0 };
      s.values.forEach(function (x, j) { rest.values[j] += x; });
      for (var k in s.byX) rest.byX[k] = (rest.byX[k] || 0) + s.byX[k];
      rest.total += s.total;
    });
    var used = {};
    kept.forEach(function (s) { s.color = D ? colourFor(D, s.key) : SLOTS[0]; if (s.color) used[s.color] = true; });
    var free = SLOTS.filter(function (x) { return !used[x]; });
    kept.forEach(function (s) { if (!s.color) s.color = free.length ? free.shift() : REST; });
    if (rest) { rest.color = REST; kept.push(rest); }
    var keptKeys = kept.filter(function (s) { return s.key !== '__rest'; }).map(function (s) { return s.key; });
    kept.forEach(function (s) { s.others = keptKeys; });
    var xKeys = Object.keys(xTotals).sort(function (a, b) { return xTotals[b] - xTotals[a]; });
    var res = { series: kept, total: total, buckets: rb.keys, step: roll[2], xKeys: xKeys, xTotals: xTotals, xName: X ? X.name : null, xDim: X ? q.x : 'time', pct: q.mode === 'share' };
    if (res.pct) {
      if (!X) {
        var tot = res.buckets.map(function (_, i) { var t = 0; kept.forEach(function (s) { t += s.values[i]; }); return t; });
        kept.forEach(function (s) { s.counts = s.values; s.values = s.values.map(function (v, i) { return tot[i] ? 100 * v / tot[i] : 0; }); });
      } else kept.forEach(function (s) { var pr = {}; for (var k in s.byX) pr[k] = xTotals[k] ? 100 * s.byX[k] / xTotals[k] : 0; s.byX = pr; });
    }
    return res;
  }
  function sumBy(c, rows, fn) { var m = {}; rows.forEach(function (r) { var k = fn(c, r); m[k] = (m[k] || 0) + r[I.n]; }); return m; }

  // ---- drawing ----------------------------------------------------------
  var NS = 'http://www.w3.org/2000/svg';
  function svgEl(tag, at) { var e = document.createElementNS(NS, tag); for (var k in at) e.setAttribute(k, at[k]); return e; }
  // SVG presentation attributes can't read CSS variables, so the chart
  // colours are resolved once per draw.
  function resolver() {
    var cs = getComputedStyle(root || document.documentElement);
    return function (c) { var m = /var\((--[^)]+)\)/.exec(c); return m ? cs.getPropertyValue(m[1]).trim() : c; };
  }
  function niceMax(v) {
    if (v <= 4) return 4;
    var p = Math.pow(10, Math.floor(Math.log10(v))), m = v / p;
    return (m <= 1 ? 1 : m <= 2 ? 2 : m <= 2.5 ? 2.5 : m <= 5 ? 5 : 10) * p;
  }
  function tickLabel(t, step) {
    var d = new Date(t * 1000);
    if (step >= 604800) return 'w/c ' + d.toLocaleDateString([], { day: 'numeric', month: 'short', timeZone: 'UTC' });
    if (step >= 86400) return d.toLocaleDateString([], { day: 'numeric', month: 'short' });
    if (step >= 6 * 3600) return d.toLocaleDateString([], { weekday: 'short' }) + ' ' + d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
    return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
  }
  var tipEl = null;
  function showTip(ev, title, items) {
    if (!tipEl) return;
    tipEl.textContent = '';
    tipEl.appendChild(el('div', { 'class': 'dt-tip-h', text: title }));
    items.forEach(function (it) {
      var sw = el('span', { 'class': 'dt-sw' }); sw.style.background = it[2];
      tipEl.appendChild(el('div', { 'class': 'dt-tip-row' }, [sw, el('strong', { text: it[0] }), document.createTextNode(' ' + it[1])]));
    });
    tipEl.hidden = false;
    var x = Math.min(ev.clientX + 14, innerWidth - tipEl.offsetWidth - 8), y = Math.min(ev.clientY + 14, innerHeight - tipEl.offsetHeight - 8);
    tipEl.style.left = Math.max(4, x) + 'px'; tipEl.style.top = Math.max(4, y) + 'px';
  }
  function hideTip() { if (tipEl) tipEl.hidden = true; }
  // The drawing area inside a box, without its padding.
  function inner(box) {
    var cs = getComputedStyle(box);
    return { w: box.clientWidth - (parseFloat(cs.paddingLeft) || 0) - (parseFloat(cs.paddingRight) || 0), h: box.clientHeight - (parseFloat(cs.paddingTop) || 0) - (parseFloat(cs.paddingBottom) || 0) };
  }
  function empty(box, text) { box.textContent = ''; box.appendChild(el('p', { 'class': 'dt-empty', text: text || 'Nothing matches.' })); }

  function chartTop(res, kind) {
    if (res.pct) return 100;
    if (res.xDim !== 'time') return niceMax(Math.max.apply(null, [1].concat(res.xKeys.map(function (k) { return res.xTotals[k]; }))));
    var m = 1;
    res.buckets.forEach(function (_, i) {
      if (kind === 'line') res.series.forEach(function (s) { m = Math.max(m, s.values[i]); });
      else { var t = 0; res.series.forEach(function (s) { t += s.values[i]; }); m = Math.max(m, t); }
    });
    return niceMax(m);
  }
  function yAxis(svg, top, L, R, W, y, pct) {
    [0, top / 2, top].forEach(function (v) {
      svg.appendChild(svgEl('line', { 'class': 'dt-gridline', x1: L, x2: W - R, y1: y(v), y2: y(v) }));
      var t = svgEl('text', { 'class': 'dt-tick', x: L - 5, y: y(v) + 3.5, 'text-anchor': 'end' }); t.textContent = nf.format(v) + (pct ? '%' : ''); svg.appendChild(t);
    });
  }
  function xLabels(svg, nb, xAt, y, buckets, step) {
    var picks = nb <= 2 ? (nb === 1 ? [0] : [0, 1]) : [0, Math.floor(nb / 2), nb - 1];
    picks.forEach(function (i, k) {
      var t = svgEl('text', { 'class': 'dt-tick', x: xAt(i), y: y, 'text-anchor': picks.length === 1 ? 'middle' : k === 0 ? 'start' : k === picks.length - 1 ? 'end' : 'middle' });
      t.textContent = tickLabel(buckets[i], step); svg.appendChild(t);
    });
  }
  // timeChart: bars (stacked), lines or a stacked area over the rolled-up
  // buckets. onPick(from, to, series) opens the packets behind a mark.
  function timeChart(box, res, kind, top, onPick) {
    var series = res.series, buckets = res.buckets;
    if (!series.length || !res.total) return empty(box);
    box.textContent = '';
    var sz = inner(box), W = Math.max(220, sz.w), H = Math.max(100, sz.h), L = 44, R = 8, T = 8, B = 20, col = resolver();
    var nb = buckets.length, band = (W - L - R) / nb, y = function (v) { return T + (H - T - B) * (1 - v / top); };
    var cnt = function (s, i) { return (s.counts || s.values)[i]; };
    var svg = svgEl('svg', { viewBox: '0 0 ' + W + ' ' + H, width: W, height: H, role: 'img', 'aria-label': 'Packets per period' });
    yAxis(svg, top, L, R, W, y, res.pct);
    var tipFor = function (ev, i) {
      var tot = 0; series.forEach(function (s) { tot += cnt(s, i); });
      showTip(ev, tickLabel(buckets[i], res.step) + ', ' + n(tot) + ' in all', series.slice().reverse().filter(function (s) { return cnt(s, i); }).map(function (s) {
        return [res.pct ? Math.round(s.values[i]) + '% (' + n(cnt(s, i)) + ')' : n(s.values[i]), s.label, col(s.color)];
      }));
    };
    if (kind === 'bar') {
      xLabels(svg, nb, function (i) { return L + band * i + band / 2; }, H - 5, buckets, res.step);
      var cw = Math.max(1, Math.min(22, band - (band > 4 ? 2 : 0)));
      buckets.forEach(function (b, i) {
        var g = svgEl('g', { 'class': 'dt-col', tabindex: '0', role: 'button', 'aria-label': tickLabel(b, res.step) + ': list the packets' });
        g.appendChild(svgEl('rect', { 'class': 'dt-hitr', x: L + band * i, y: T, width: band, height: H - T - B, fill: 'transparent' }));
        var base = y(0), xx = L + band * i + (band - cw) / 2;
        var segs = series.filter(function (s) { return s.values[i] > 0; });
        segs.forEach(function (s, j) {
          var h = (H - T - B) * s.values[i] / top, yt = Math.min(base - h, base - 1);
          var last = j === segs.length - 1, r = last ? Math.min(3, base - yt, cw / 2) : 0, gap = j > 0 && cw > 3 ? 1.5 : 0;
          var d = 'M' + xx + ',' + (base - gap) + 'V' + (yt + r) + (r ? 'Q' + xx + ',' + yt + ' ' + (xx + r) + ',' + yt + 'H' + (xx + cw - r) + 'Q' + (xx + cw) + ',' + yt + ' ' + (xx + cw) + ',' + (yt + r) : 'H' + (xx + cw)) + 'V' + (base - gap) + 'Z';
          var seg = svgEl('path', { 'class': 'dt-seg-r', d: d, fill: col(s.color) });
          seg.addEventListener('click', function (ev) { ev.stopPropagation(); onPick(b, b + res.step, s); });
          g.appendChild(seg);
          base = yt;
        });
        g.addEventListener('click', function () { onPick(b, b + res.step); });
        g.addEventListener('keydown', function (ev) { if (ev.key === 'Enter') onPick(b, b + res.step); });
        g.addEventListener('mousemove', function (ev) { tipFor(ev, i); });
        g.addEventListener('mouseleave', hideTip);
        svg.appendChild(g);
      });
    } else {
      var xp = function (i) { return nb === 1 ? (L + W - R) / 2 : L + (W - L - R) * i / (nb - 1); };
      xLabels(svg, nb, xp, H - 5, buckets, res.step);
      var cum = buckets.map(function () { return 0; });
      var path = function (vals) { return vals.map(function (v, i) { return (i ? 'L' : 'M') + xp(i).toFixed(1) + ',' + y(v).toFixed(1); }).join(''); };
      series.forEach(function (s) {
        var lo = cum.slice(), hi = s.values.map(function (v, i) { return kind === 'area' ? (cum[i] += v) : v; });
        var c = col(s.color);
        if (kind === 'area') svg.appendChild(svgEl('path', { d: path(hi) + lo.slice().reverse().map(function (v, j) { return 'L' + xp(nb - 1 - j).toFixed(1) + ',' + y(v).toFixed(1); }).join('') + 'Z', fill: c, 'fill-opacity': '0.8' }));
        svg.appendChild(svgEl('path', { d: path(hi), fill: 'none', stroke: c, 'stroke-width': 2, 'stroke-linejoin': 'round', 'stroke-linecap': 'round' }));
      });
      var cross = svgEl('line', { x1: 0, x2: 0, y1: T, y2: H - B, stroke: col('var(--text-muted)'), 'stroke-width': 1, visibility: 'hidden' });
      svg.appendChild(cross);
      var hitA = svgEl('rect', { 'class': 'dt-hitr', x: L, y: T, width: W - L - R, height: H - T - B, fill: 'transparent', tabindex: '0', role: 'button', 'aria-label': 'Arrow keys move along the chart; Enter lists the packets' });
      var idx = 0;
      var at = function (ev, i) { idx = i; cross.setAttribute('x1', xp(i)); cross.setAttribute('x2', xp(i)); cross.setAttribute('visibility', 'visible'); if (ev) tipFor(ev, i); };
      hitA.addEventListener('mousemove', function (ev) { var r = svg.getBoundingClientRect(), px = (ev.clientX - r.left) * W / r.width; at(ev, Math.max(0, Math.min(nb - 1, Math.round((px - L) / ((W - L - R) / Math.max(1, nb - 1)))))); });
      hitA.addEventListener('mouseleave', function () { cross.setAttribute('visibility', 'hidden'); hideTip(); });
      hitA.addEventListener('click', function () { onPick(buckets[idx], buckets[idx] + res.step); });
      hitA.addEventListener('keydown', function (ev) {
        if (ev.key === 'ArrowRight') { at(null, Math.min(nb - 1, idx + 1)); ev.preventDefault(); }
        if (ev.key === 'ArrowLeft') { at(null, Math.max(0, idx - 1)); ev.preventDefault(); }
        if (ev.key === 'Enter') onPick(buckets[idx], buckets[idx] + res.step);
      });
      svg.appendChild(hitA);
    }
    box.appendChild(svg);
  }
  // dimChart: one horizontal bar per X value, stacked by the split.
  function dimChart(box, res, top, onPick) {
    var keys = res.xKeys.slice(0, 14);
    if (!keys.length) return empty(box);
    box.textContent = '';
    var sz = inner(box), W = Math.max(220, sz.w), rowH = Math.max(20, Math.min(30, (sz.h - 24) / keys.length)), T = 4, B = 20, col = resolver();
    var labelW = Math.min(170, Math.max(80, W * 0.3)), L = labelW + 8, R = 56, H = T + B + keys.length * rowH;
    var x = function (v) { return L + (W - L - R) * v / top; };
    var svg = svgEl('svg', { viewBox: '0 0 ' + W + ' ' + H, width: W, height: H, role: 'img', 'aria-label': 'Packets by ' + DIMS[res.xDim].label.toLowerCase() });
    [0, top / 2, top].forEach(function (v, k) {
      svg.appendChild(svgEl('line', { 'class': 'dt-gridline', x1: x(v), x2: x(v), y1: T, y2: H - B }));
      var t = svgEl('text', { 'class': 'dt-tick', x: x(v), y: H - 5, 'text-anchor': k === 0 ? 'start' : k === 2 ? 'end' : 'middle' }); t.textContent = nf.format(v) + (res.pct ? '%' : ''); svg.appendChild(t);
    });
    keys.forEach(function (k, i) {
      var yy = T + i * rowH, bh = Math.min(18, rowH - 6), by = yy + (rowH - bh) / 2, full = res.xName(k);
      var lab = svgEl('text', { 'class': 'dt-tick dt-tick-lab', x: labelW, y: by + bh / 2 + 4, 'text-anchor': 'end' });
      var maxCh = Math.floor(labelW / 6.6);
      lab.textContent = full.length > maxCh ? full.slice(0, maxCh - 1) + '…' : full; svg.appendChild(lab);
      var g = svgEl('g', { 'class': 'dt-col', tabindex: '0', role: 'button', 'aria-label': full + ': list the packets' });
      g.appendChild(svgEl('rect', { 'class': 'dt-hitr', x: 0, y: yy, width: W, height: rowH, fill: 'transparent' }));
      var base = x(0);
      res.series.filter(function (s) { return s.byX[k]; }).forEach(function (s, j, arr) {
        var w = (W - L - R) * s.byX[k] / top, x0 = base + (j > 0 ? 1.5 : 0), x1 = Math.max(x0 + 1, base + w);
        var seg = svgEl('rect', { 'class': 'dt-seg-r', x: x0, y: by, width: x1 - x0, height: bh, rx: j === arr.length - 1 ? 3 : 0, fill: col(s.color) });
        seg.addEventListener('click', function (ev) { ev.stopPropagation(); onPick(null, null, s, k); });
        g.appendChild(seg); base = x1;
      });
      var val = svgEl('text', { 'class': 'dt-tick', x: Math.min(base + 5, W - R + 4), y: by + bh / 2 + 4 }); val.textContent = n(res.xTotals[k]); g.appendChild(val);
      g.addEventListener('click', function () { onPick(null, null, null, k); });
      g.addEventListener('keydown', function (ev) { if (ev.key === 'Enter') onPick(null, null, null, k); });
      g.addEventListener('mousemove', function (ev) { showTip(ev, full + ', ' + n(res.xTotals[k]) + ' in all', res.series.slice().reverse().filter(function (s) { return s.byX[k]; }).map(function (s) { return [res.pct ? Math.round(s.byX[k]) + '%' : n(s.byX[k]), s.label, col(s.color)]; })); });
      g.addEventListener('mouseleave', hideTip);
      svg.appendChild(g);
    });
    box.appendChild(svg);
  }
  function legend(list) {
    var lg = el('div', { 'class': 'dt-legend' });
    list.slice().reverse().forEach(function (s) {
      var sw = el('span', { 'class': 'dt-sw' }); sw.style.background = s.color;
      lg.appendChild(el('span', { 'class': 'dt-leg' }, [sw, s.label, el('b', { text: n(s.total) })]));
    });
    return lg;
  }
  // md renders a text panel: paragraphs, **bold** and `code`, nothing else.
  function md(text) {
    var box = el('div', { 'class': 'dt-md' });
    String(text).split(/\n{2,}/).forEach(function (para) {
      var p = el('p');
      para.split(/(\*\*[^*]+\*\*|`[^`]+`)/).forEach(function (part) {
        if (/^\*\*[^*]+\*\*$/.test(part)) p.appendChild(el('strong', { text: part.slice(2, -2) }));
        else if (/^`[^`]+`$/.test(part)) p.appendChild(el('code', { text: part.slice(1, -1) }));
        else if (part) p.appendChild(document.createTextNode(part));
      });
      box.appendChild(p);
    });
    return box;
  }

  // ---- panels -------------------------------------------------------------
  function panelTypeLabel(t) { var x = PANEL_TYPES.filter(function (p) { return p[0] === t; })[0]; return x ? x[1] : t; }
  function exactFlag(dim) { return dim === 'through' ? 1 : 0; }
  function drawStat(c, rows, w, body, pick) {
    var whole = c.rows.reduce(function (a, r) { return a + r[I.n]; }, 0), v = rows.reduce(function (a, r) { return a + r[I.n]; }, 0);
    var sp = c.buckets.map(function () { return 0; }); rows.forEach(function (r) { sp[r[I.b]] += r[I.n]; });
    var st = el('div', { 'class': 'dt-stat', role: 'button', tabindex: '0', title: 'List the packets' }, [
      el('span', { 'class': 'dt-stat-v', text: n(v) }),
      el('span', { 'class': 'dt-stat-s', text: (whole ? (100 * v / whole).toFixed(1) : '0') + '% of the ' + windowLabel(w) })
    ]);
    var mx = Math.max.apply(null, [1].concat(sp)), SW = 300, SH = 36;
    var pts = sp.map(function (x, i) { return (i ? 'L' : 'M') + (i / Math.max(1, sp.length - 1) * SW).toFixed(1) + ',' + (SH - 2 - x / mx * (SH - 6)).toFixed(1); }).join('');
    var svg = svgEl('svg', { viewBox: '0 0 ' + SW + ' ' + SH, preserveAspectRatio: 'none', 'aria-hidden': 'true', 'class': 'dt-spark' });
    svg.appendChild(svgEl('path', { 'class': 'dt-spark-a', d: pts + 'L' + SW + ',' + SH + 'L0,' + SH + 'Z' }));
    svg.appendChild(svgEl('path', { 'class': 'dt-spark-l', d: pts, 'vector-effect': 'non-scaling-stroke' }));
    st.appendChild(svg);
    body.appendChild(st);
    st.addEventListener('click', function () { pick([], undefined, undefined, 'all of it'); });
    st.addEventListener('keydown', function (ev) { if (ev.key === 'Enter') pick([], undefined, undefined, 'all of it'); });
  }
  function drawSeries(p, c, rows, w, body, frame, pick) {
    var q = p.query, kind = q.x && q.x !== 'time' ? 'dim' : p.type;
    var onPick = function (facetV) {
      return function (from, to, s, xk) {
        var extra = [], parts = [];
        if (facetV !== undefined) { extra.push({ d: q.facet, n: 0, v: [facetV], x: exactFlag(q.facet) }); parts.push(DIMS[q.facet].name(facetV)); }
        if (xk !== undefined) { extra.push({ d: q.x, n: 0, v: [xk], x: exactFlag(q.x) }); parts.push(DIMS[q.x].name(xk)); }
        if (s && q.split) {
          if (s.key !== '__rest') extra.push({ d: q.split, n: 0, v: [s.key], x: exactFlag(q.split) });
          else if (s.others.length) extra.push({ d: q.split, n: 1, v: s.others, x: exactFlag(q.split) });
          parts.push(s.label);
        }
        if (from !== null && from !== undefined) parts.unshift(tickLabel(from, rollFor(w, q.rollup)[2]));
        pick(extra, from === null ? undefined : from, to === null ? undefined : to, parts.join(', ') || 'all of it');
      };
    };
    var drawOne = function (box, rs, top, facetV, res) {
      res = res || seriesQuery(c, rs, q, w);
      if (kind === 'dim') dimChart(box, res, top === undefined ? chartTop(res, 'dim') : top, onPick(facetV));
      else timeChart(box, res, kind, top === undefined ? chartTop(res, kind) : top, onPick(facetV));
      return res;
    };
    if (q.facet && DIMS[q.facet]) {
      var F = DIMS[q.facet], fc = sumBy(c, rows, F.of), fv = Object.keys(fc).sort(function (a, b) { return fc[b] - fc[a]; }).slice(0, MAX_FACETS);
      if (!fv.length) return empty(body);
      var parts = {}; rows.forEach(function (r) { var k = F.of(c, r); (parts[k] = parts[k] || []).push(r); });
      var top = Math.max.apply(null, [1].concat(fv.map(function (v) { return chartTop(seriesQuery(c, parts[v], q, w), kind); })));
      // The legend goes in first, so the charts are sized to the space left.
      if (q.split) frame.appendChild(legend(seriesQuery(c, rows, q, w).series));
      var grid = el('div', { 'class': 'dt-facets' });
      body.appendChild(grid);
      fv.forEach(function (v) {
        var cell = el('div', { 'class': 'dt-facet' }, [el('div', { 'class': 'dt-facet-h' }, [el('strong', { text: F.name(v) }), el('span', { 'class': 'dt-vcount', text: ' ' + n(fc[v]) })])]);
        var box = el('div', { 'class': 'dt-facet-chart' }); cell.appendChild(box); grid.appendChild(cell);
        drawOne(box, parts[v], top, v);
      });
      return;
    }
    var res = seriesQuery(c, rows, q, w);
    if (q.split && res.series.length > 1) frame.appendChild(legend(res.series));
    drawOne(body, rows, undefined, undefined, res);
  }
  function drawTop(q, c, rows, body, pick) {
    var dim = q.dim || 'scope', D = DIMS[dim], m = sumBy(c, rows, D.of), keys = Object.keys(m).sort(function (a, b) { return m[b] - m[a]; }).slice(0, q.top || 5);
    if (!keys.length) return empty(body);
    var mx = m[keys[0]] || 1, list = el('div', { 'class': 'dt-top' });
    keys.forEach(function (k, i) {
      var bar = el('span', { 'class': 'dt-top-bar' }); bar.style.width = (100 * m[k] / mx).toFixed(1) + '%'; bar.style.background = colourFor(D, k) || SLOTS[i % SLOTS.length];
      var row = el('button', { type: 'button', 'class': 'dt-top-row', title: 'List the packets' }, [el('span', { 'class': 'dt-top-name', text: D.name(k) }), el('b', { text: n(m[k]) }), bar]);
      row.addEventListener('click', function () { pick([{ d: dim, n: 0, v: [k], x: exactFlag(dim) }], undefined, undefined, D.name(k)); });
      list.appendChild(row);
    });
    body.appendChild(list);
  }
  function orderValues(D, totals) {
    var vals = Object.keys(totals);
    if (D.area) {
      var ac = (cfg.areas || []).map(function (a) { return a.code; }).concat(['unknown']);
      var at = function (v) { var i = ac.indexOf(v); return i < 0 ? 999 : i; };
      vals.sort(function (a, b) { return at(a) - at(b) || totals[b] - totals[a]; });
    } else vals.sort(function (a, b) { return totals[b] - totals[a]; });
    return vals.slice(0, 16);
  }
  function drawMatrix(q, c, rows, body, pick) {
    var rd = q.rows || 'origin', cd = q.cols || 'heard', R = DIMS[rd], C = DIMS[cd];
    var mm = sumBy(c, rows, function (cc, r) { return R.of(cc, r) + '\u0000' + C.of(cc, r); });
    var rt = {}, ct = {};
    Object.keys(mm).forEach(function (k) { var a = k.split('\u0000'); rt[a[0]] = (rt[a[0]] || 0) + mm[k]; ct[a[1]] = (ct[a[1]] || 0) + mm[k]; });
    var rk = orderValues(R, rt), ck = orderValues(C, ct), diag = R.area && C.area;
    if (!rk.length) return empty(body);
    var mxv = 1; Object.keys(mm).forEach(function (k) { var a = k.split('\u0000'); if (!(diag && a[0] === a[1])) mxv = Math.max(mxv, mm[k]); });
    var t = el('table', { 'class': 'dt-mx' });
    t.appendChild(el('caption', { 'class': 'dt-sr', text: R.label + ' by ' + C.label + '; select a figure to list its packets' }));
    var hr = el('tr', null, [el('th', { scope: 'col', 'class': 'dt-corner', text: R.label + ' ↓  ' + C.label + ' →' })]);
    ck.forEach(function (cv) { hr.appendChild(el('th', { scope: 'col', text: C.name(cv) })); });
    t.appendChild(el('thead', null, [hr]));
    var tb = el('tbody');
    rk.forEach(function (rv) {
      var tr = el('tr', null, [el('th', { scope: 'row', text: R.name(rv) })]);
      ck.forEach(function (cv) {
        var v = mm[rv + '\u0000' + cv] || 0, self = diag && rv === cv;
        var td = el('td', { 'class': self ? 'dt-self' : '', tabindex: '0', role: 'button', title: R.name(rv) + ' → ' + C.name(cv) + ': ' + n(v) + ' packets', text: n(v) });
        if (!self && v) td.style.background = 'color-mix(in srgb, var(--accent) ' + Math.round(8 + 42 * Math.log(1 + v) / Math.log(1 + mxv)) + '%, transparent)';
        var go = function () { pick([{ d: rd, n: 0, v: [rv], x: 1 }, { d: cd, n: 0, v: [cv], x: 1 }], undefined, undefined, R.name(rv) + ' → ' + C.name(cv)); };
        td.addEventListener('click', go);
        td.addEventListener('keydown', function (ev) { if (ev.key === 'Enter' || ev.key === ' ') { ev.preventDefault(); go(); } });
        tr.appendChild(td);
      });
      tb.appendChild(tr);
    });
    t.appendChild(tb);
    body.appendChild(el('div', { 'class': 'dt-scroll' }, [t]));
  }
  function drawTable(q, c, rows, body) {
    var X = DIMS[q.x && q.x !== 'time' ? q.x : 'origin'], S = q.split ? DIMS[q.split] : null;
    var tm = sumBy(c, rows, function (cc, r) { return X.of(cc, r) + '\u0000' + (S ? S.of(cc, r) : 'all'); });
    var xt = {}, stt = {};
    Object.keys(tm).forEach(function (k) { var a = k.split('\u0000'); xt[a[0]] = (xt[a[0]] || 0) + tm[k]; stt[a[1]] = (stt[a[1]] || 0) + tm[k]; });
    var xs = Object.keys(xt).sort(function (a, b) { return xt[b] - xt[a]; }), ss = Object.keys(stt).sort(function (a, b) { return stt[b] - stt[a]; }).slice(0, 8);
    if (!xs.length) return empty(body);
    var tt = el('table', { 'class': 'dt-t' });
    var th = el('tr', null, [el('th', { scope: 'col', text: X.label })]);
    ss.forEach(function (s) { th.appendChild(el('th', { scope: 'col', 'class': 'dt-n', text: S ? S.name(s) : 'Packets' })); });
    if (ss.length > 1) th.appendChild(el('th', { scope: 'col', 'class': 'dt-n', text: 'Total' }));
    tt.appendChild(el('thead', null, [th]));
    var tbody = el('tbody');
    xs.forEach(function (x) {
      var tr = el('tr', null, [el('td', { text: X.name(x) })]);
      ss.forEach(function (s) { tr.appendChild(el('td', { 'class': 'dt-n', text: n(tm[x + '\u0000' + s] || 0) })); });
      if (ss.length > 1) tr.appendChild(el('td', { 'class': 'dt-n' }, [el('b', { text: n(xt[x]) })]));
      tbody.appendChild(tr);
    });
    tt.appendChild(tbody);
    body.appendChild(el('div', { 'class': 'dt-scroll' }, [tt]));
  }
  function drawPanelBody(p, d, body, frame) {
    body.textContent = '';
    var oldLeg = frame.querySelector('.dt-legend'); if (oldLeg) oldLeg.remove();
    if (p.type === 'text') { body.appendChild(md(substTitle((p.options && p.options.text) || '', d))); return; }
    if (!places() && panelPlaceUse(p)) { empty(body, 'This panel needs areas, which this CoreScope does not have (it ' + panelPlaceUse(p) + ').'); return; }
    var w = panelWindow(p), c = cubes[w];
    if (!c) { empty(body, 'Loading…'); return; }
    var filters = panelFilters(p, d), rows = filteredRows(c, filters), q = p.query || {};
    var pick = function (extra, from, to, label) { if (!state.edit) openDrill(p, d, filters.concat(extra), from, to, label, w); };
    if (p.type === 'stat') return drawStat(c, rows, w, body, pick);
    if (p.type === 'bar' || p.type === 'line' || p.type === 'area') return drawSeries(p, c, rows, w, body, frame, pick);
    if (p.type === 'top') return drawTop(q, c, rows, body, pick);
    if (p.type === 'matrix') return drawMatrix(q, c, rows, body, pick);
    if (p.type === 'table') return drawTable(q, c, rows, body);
  }
  function renderPanel(p, d) {
    var title = substTitle(p.title, d);
    var frame = el('section', { 'class': 'dt-panel dt-type-' + p.type + (state.sel === p.id ? ' dt-sel' : ''), 'data-id': p.id, 'aria-label': title });
    var l = p.layout;
    frame.style.gridColumn = (l.x + 1) + ' / span ' + l.w;
    frame.style.gridRow = (l.y + 1) + ' / span ' + l.h;
    frame.style.setProperty('--dt-h', l.h);
    var handle = el('button', { type: 'button', 'class': 'dt-handle', title: 'Drag to move; arrow keys move it, Shift + arrows resize it', 'aria-label': 'Move ' + title, text: '⠇' });
    var meta = panelTypeLabel(p.type) + (p.query && p.query.window ? ' · ' + windowLabel(p.query.window) : '');
    var head = el('header', { 'class': 'dt-ph' }, [handle, el('h3', { text: title }), el('span', { 'class': 'dt-kind', text: meta })]);
    var body = el('div', { 'class': 'dt-pb' });
    frame.appendChild(head); frame.appendChild(body);
    var rz = el('span', { 'class': 'dt-resize', title: 'Drag to resize', 'aria-hidden': 'true' });
    frame.appendChild(rz);
    frame.addEventListener('click', function () { if (!state.edit || state.sel === p.id) return; state.sel = p.id; render(); });
    wirePanelEditing(p, frame, handle, rz);
    redrawers.push(function () { drawPanelBody(p, d, body, frame); });
    return frame;
  }

  // ---- editing on the grid ------------------------------------------------
  function gridMetrics() {
    var g = $('dtBoard'), cs = getComputedStyle(g), gap = parseFloat(cs.columnGap) || 12;
    var rect = g.getBoundingClientRect();
    return { colW: (rect.width - 11 * gap) / 12, rowH: parseFloat(cs.gridAutoRows) || 64, gap: gap };
  }
  function wirePanelEditing(p, frame, handle, rz) {
    var drag = function (ev, mode) {
      if (!state.edit) return;
      ev.preventDefault(); ev.stopPropagation();
      snapshot();
      var gm = gridMetrics(), sx = ev.clientX, sy = ev.clientY, l0 = clone(p.layout);
      frame.classList.add('dt-dragging');
      var mv = function (e) {
        var dx = Math.round((e.clientX - sx) / (gm.colW + gm.gap)), dy = Math.round((e.clientY - sy) / (gm.rowH + gm.gap));
        if (mode === 'move') { p.layout.x = Math.max(0, Math.min(12 - l0.w, l0.x + dx)); p.layout.y = Math.max(0, l0.y + dy); }
        else { p.layout.w = Math.max(2, Math.min(12 - l0.x, l0.w + dx)); p.layout.h = Math.max(2, Math.min(12, l0.h + dy)); }
        frame.style.gridColumn = (p.layout.x + 1) + ' / span ' + p.layout.w;
        frame.style.gridRow = (p.layout.y + 1) + ' / span ' + p.layout.h;
      };
      var up = function () {
        removeEventListener('pointermove', mv); removeEventListener('pointerup', up);
        resolveLayout(cur().panels, p); state.sel = p.id; persist(); render();
      };
      addEventListener('pointermove', mv); addEventListener('pointerup', up);
    };
    handle.addEventListener('pointerdown', function (ev) { drag(ev, 'move'); });
    rz.addEventListener('pointerdown', function (ev) { drag(ev, 'size'); });
    handle.addEventListener('keydown', function (ev) {
      if (!state.edit) return;
      var k = { ArrowLeft: [-1, 0], ArrowRight: [1, 0], ArrowUp: [0, -1], ArrowDown: [0, 1] }[ev.key];
      if (!k) return;
      ev.preventDefault(); snapshot();
      if (ev.shiftKey) { p.layout.w = Math.max(2, Math.min(12 - p.layout.x, p.layout.w + k[0])); p.layout.h = Math.max(2, Math.min(12, p.layout.h + k[1])); }
      else { p.layout.x = Math.max(0, Math.min(12 - p.layout.w, p.layout.x + k[0])); p.layout.y = Math.max(0, p.layout.y + k[1]); }
      resolveLayout(cur().panels, p); persist(); state.sel = p.id; render();
      var h = root.querySelector('.dt-panel[data-id="' + p.id + '"] .dt-handle'); if (h) h.focus();
    });
  }

  // ---- page skeleton --------------------------------------------------------
  function pageHtml() {
    return '' +
      '<div class="dt-page" id="dtPage">' +
        '<header class="dt-bar">' +
          '<h2 class="dt-title">Data</h2>' +
          '<label class="dt-pick"><span class="dt-sr">Dashboard</span><select id="dtDash"></select></label>' +
          '<span class="dt-src" id="dtSrc"></span>' +
          '<div class="dt-seg" role="group" aria-label="Period, for the whole dashboard" id="dtWindows"></div>' +
          '<div class="dt-vars" id="dtVars"></div>' +
          '<span class="dt-bar-actions">' +
            '<button type="button" class="dt-btn" id="dtUndo" hidden>Undo</button>' +
            '<button type="button" class="dt-btn" id="dtNew">New</button>' +
            '<button type="button" class="dt-btn" id="dtImport">Import</button>' +
            '<button type="button" class="dt-btn" id="dtExport">Export</button>' +
            '<button type="button" class="dt-btn" id="dtShare">Share</button>' +
            '<button type="button" class="dt-btn dt-btn-primary" id="dtEdit" aria-pressed="false">Edit</button>' +
            '<button type="button" class="dt-btn" id="dtFill" aria-pressed="false" title="Hide the site navigation">Full screen</button>' +
          '</span>' +
        '</header>' +
        '<div class="dt-body">' +
          '<aside class="dt-rail" id="dtRail" aria-label="Dashboard editor" hidden></aside>' +
          '<div class="dt-main">' +
            '<div class="dt-dhead"><h2 id="dtDTitle"></h2><p id="dtDDesc"></p></div>' +
            '<p class="dt-note" id="dtNote" hidden></p>' +
            '<div class="dt-board" id="dtBoard"></div>' +
          '</div>' +
        '</div>' +
        '<div class="dt-tip" id="dtTip" role="status" hidden></div>' +
      '</div>' +
      '<dialog class="dt-modal" id="dtModal" aria-labelledby="dtModalTitle"><div class="dt-modal-in" id="dtModalIn"></div></dialog>' +
      '<div class="dt-toast" id="dtToast" role="status" hidden></div>';
  }

  // ---- top bar --------------------------------------------------------------
  var SOURCES = [['built-in', 'Built-in'], ['operator', 'From this site'], ['mine', 'Your dashboards'], ['shared', 'Shared with you']];
  function varOptions(v) {
    var c = cubes[state.window];
    if (v.kind === 'area') return (cfg.areas || []).map(function (a) { return [a.code, a.label]; });
    if (v.kind === 'scope' && c) return c.scopes.map(function (s) { return [s.key, s.key]; });
    if (v.kind === 'channel' && c) return c.channels.filter(Boolean).map(function (ch) { return [ch, DIMS.channel.name(ch)]; });
    return null;
  }
  function renderBar(d) {
    var sel = $('dtDash'); sel.textContent = '';
    SOURCES.forEach(function (g) {
      var list = all().filter(function (x) { return x.source === g[0]; });
      if (!list.length) return;
      var og = el('optgroup', { label: g[1] });
      list.forEach(function (x) { var o = el('option', { value: x.id, text: x.title }); if (x.id === d.id) o.selected = true; og.appendChild(o); });
      sel.appendChild(og);
    });
    var src = $('dtSrc');
    src.textContent = { 'built-in': 'Built-in', operator: 'From this site', mine: 'Yours', shared: 'Shared' }[d.source] || '';
    src.className = 'dt-src dt-src-' + d.source;
    var wb = $('dtWindows'); wb.textContent = '';
    (cfg.windows || []).forEach(function (w) {
      var b = el('button', { type: 'button', 'aria-pressed': String(w.key === state.window), text: w.label });
      b.addEventListener('click', function () { if (state.window === w.key) return; state.window = w.key; syncHash(); render(); });
      wb.appendChild(b);
    });
    var vb = $('dtVars'); vb.textContent = '';
    dashVars(d).forEach(function (v) {
      var id = 'dtVar-' + v.name, control, options = varOptions(v), cv = varValue(v);
      if (options) {
        control = el('select', { id: id });
        if (cv && !options.some(function (o) { return o[0] === cv; })) options.unshift([cv, cv]);
        options.forEach(function (o) { var op = el('option', { value: o[0], text: o[1] }); if (o[0] === cv) op.selected = true; control.appendChild(op); });
      } else control = el('input', { id: id, 'class': 'dt-inp', value: cv });
      control.addEventListener('change', function () { state.vars[v.name] = control.value; syncHash(); render(); });
      vb.appendChild(el('label', { 'class': 'dt-var', 'for': id }, [el('span', { text: v.label || ('$' + v.name) }), control]));
    });
    var eb = $('dtEdit');
    eb.setAttribute('aria-pressed', String(state.edit));
    eb.textContent = state.edit ? 'Done' : (editable(d) ? 'Edit' : 'Edit a copy');
    $('dtUndo').hidden = !state.edit || !undo.length;
    $('dtExport').disabled = !d.panels.length;
    $('dtShare').disabled = !d.panels.length;
  }

  // ---- editor rail ------------------------------------------------------------
  function field(label, control) { return el('label', { 'class': 'dt-field' }, [el('span', { text: label }), control]); }
  function selectOf(id, options, value, onChange) {
    var s = el('select', { id: id });
    options.forEach(function (o) { var op = el('option', { value: o[0], text: o[1] }); if (o[0] === value) op.selected = true; s.appendChild(op); });
    s.addEventListener('change', function () { onChange(s.value); });
    return s;
  }
  function dimOptions(withNone, withTime) { return (withNone ? [['', 'Nothing']] : []).concat(withTime ? [['time', 'Time']] : []).concat(DIM_ORDER.filter(hasDim).map(function (k) { return [k, DIMS[k].label]; })); }
  function change(fn) { snapshot(); fn(); persist(); render(); }
  function filterText(f, d) {
    var D = DIMS[f.d], vals = f.v.map(function (v) { return v.charAt(0) === '$' ? substTitle(v, d) + ' (' + v + ')' : D.name(v); });
    var list = vals.length <= 2 ? vals.join(' or ') : vals.slice(0, 2).join(', ') + ' or ' + (vals.length - 2) + ' more';
    return D.label + (f.n ? ' is not ' : ' is ') + list;
  }
  function railSection(rail, title) { rail.appendChild(el('h3', { 'class': 'dt-k', text: title })); }
  function renderRail(d) {
    var rail = $('dtRail');
    rail.hidden = !state.edit;
    rail.textContent = '';
    if (!state.edit) return;
    var p = d.panels.filter(function (x) { return x.id === state.sel; })[0];
    if (!p) return renderDashSettings(rail, d);
    var q = p.query = p.query || {};
    var back = el('button', { type: 'button', 'class': 'dt-link', text: '← Dashboard settings' });
    back.addEventListener('click', function () { state.sel = null; render(); });
    rail.appendChild(back);
    railSection(rail, 'Panel');
    var title = el('input', { id: 'dtPTitle', 'class': 'dt-inp', value: p.title, maxlength: '120' });
    title.addEventListener('change', function () { change(function () { p.title = title.value || 'Untitled'; }); });
    rail.appendChild(field('Title ($variables are filled in)', title));
    rail.appendChild(field('Type', selectOf('dtPType', PANEL_TYPES.map(function (t) { return [t[0], t[1]]; }), p.type, function (v) {
      change(function () { p.type = v; if (v === 'text' && !p.options) p.options = { text: 'Write a note for readers of this dashboard.' }; if ((v === 'line' || v === 'area') && q.x && q.x !== 'time') q.x = 'time'; });
    })));
    if (p.type !== 'text') {
      rail.appendChild(field('Period', selectOf('dtPWin', [['', 'The dashboard’s']].concat((cfg.windows || []).map(function (w) { return [w.key, w.label]; })), q.window || '', function (v) { change(function () { if (v) q.window = v; else delete q.window; }); })));
      railSection(rail, 'Filters');
      var chips = el('div', { 'class': 'dt-chips' });
      (q.filters || []).forEach(function (f, i) {
        var x = el('button', { type: 'button', 'class': 'dt-fchip-x', 'aria-label': 'Remove filter: ' + filterText(f, d), text: '×' });
        x.addEventListener('click', function () { change(function () { q.filters.splice(i, 1); }); });
        chips.appendChild(el('span', { 'class': 'dt-fchip' }, [el('span', { 'class': 'dt-fchip-edit', text: filterText(f, d) }), x]));
      });
      var add = el('button', { type: 'button', 'class': 'dt-chip dt-add', text: '+ Add filter' });
      chips.appendChild(add);
      rail.appendChild(chips);
      var fe = el('div', { 'class': 'dt-editor', hidden: '' });
      rail.appendChild(fe);
      add.addEventListener('click', function () { filterEditor(fe, p, d); });
    }
    if (p.type === 'bar' || p.type === 'line' || p.type === 'area') {
      railSection(rail, 'Chart');
      var two = el('div', { 'class': 'dt-two' });
      two.appendChild(field('X axis', selectOf('dtPX', p.type === 'bar' ? dimOptions(false, true) : [['time', 'Time']], q.x || 'time', function (v) { change(function () { q.x = v; }); })));
      if (!q.x || q.x === 'time') two.appendChild(field('Group time by', selectOf('dtPRoll', (ROLLUPS[panelWindow(p)] || []).map(function (o) { return [o[0], o[1]]; }), rollFor(panelWindow(p), q.rollup)[0], function (v) { change(function () { q.rollup = v; }); })));
      two.appendChild(field('Split by', selectOf('dtPSplit', dimOptions(true), q.split || '', function (v) { change(function () { if (v) q.split = v; else delete q.split; delete q.pick; }); })));
      two.appendChild(field('Separate charts by', selectOf('dtPFacet', dimOptions(true), q.facet || '', function (v) { change(function () { if (v) q.facet = v; else delete q.facet; }); })));
      two.appendChild(field('Show', selectOf('dtPMode', [['count', 'Count'], ['share', 'Share (%)']], q.mode || 'count', function (v) { change(function () { if (v === 'count') delete q.mode; else q.mode = v; }); })));
      rail.appendChild(two);
      if (q.split && cubes[panelWindow(p)]) rail.appendChild(pickEditor(p, d));
    }
    if (p.type === 'matrix') {
      railSection(rail, 'Matrix');
      var mt = el('div', { 'class': 'dt-two' });
      mt.appendChild(field('Rows', selectOf('dtPRows', dimOptions(false), q.rows || addDefaults('matrix').rows, function (v) { change(function () { q.rows = v; }); })));
      mt.appendChild(field('Columns', selectOf('dtPCols', dimOptions(false), q.cols || addDefaults('matrix').cols, function (v) { change(function () { q.cols = v; }); })));
      rail.appendChild(mt);
    }
    if (p.type === 'table') {
      railSection(rail, 'Table');
      var tt = el('div', { 'class': 'dt-two' });
      tt.appendChild(field('Rows', selectOf('dtPTX', dimOptions(false), q.x && q.x !== 'time' ? q.x : addDefaults('table').x, function (v) { change(function () { q.x = v; }); })));
      tt.appendChild(field('Columns', selectOf('dtPTS', dimOptions(true), q.split || '', function (v) { change(function () { if (v) q.split = v; else delete q.split; }); })));
      rail.appendChild(tt);
    }
    if (p.type === 'top') {
      railSection(rail, 'Top list');
      var tp = el('div', { 'class': 'dt-two' });
      tp.appendChild(field('Of', selectOf('dtPDim', dimOptions(false), q.dim || 'scope', function (v) { change(function () { q.dim = v; }); })));
      tp.appendChild(field('How many', selectOf('dtPTop', [3, 5, 8, 10, 15, 20].map(function (v) { return [String(v), String(v)]; }), String(q.top || 5), function (v) { change(function () { q.top = +v; }); })));
      rail.appendChild(tp);
    }
    if (p.type === 'text') {
      var ta = el('textarea', { id: 'dtPText', 'class': 'dt-inp', rows: '7', maxlength: '4000' });
      ta.value = (p.options && p.options.text) || '';
      ta.addEventListener('change', function () { change(function () { p.options = { text: ta.value }; }); });
      rail.appendChild(field('Text: **bold**, `code`; $variables are filled in', ta));
    }
    railSection(rail, 'Size');
    var sz = el('div', { 'class': 'dt-two' });
    sz.appendChild(field('Width (of 12)', selectOf('dtPW', ['2', '3', '4', '5', '6', '7', '8', '9', '10', '12'].map(function (v) { return [v, v]; }), String(p.layout.w), function (v) { change(function () { p.layout.w = +v; resolveLayout(d.panels, p); }); })));
    sz.appendChild(field('Height', selectOf('dtPH', ['2', '3', '4', '5', '6', '8', '10'].map(function (v) { return [v, v]; }), String(p.layout.h), function (v) { change(function () { p.layout.h = +v; resolveLayout(d.panels, p); }); })));
    rail.appendChild(sz);
    var dup = el('button', { type: 'button', 'class': 'dt-btn', text: 'Duplicate' });
    dup.addEventListener('click', function () {
      if (d.panels.length >= MAX_PANELS) return toast('A dashboard holds at most ' + MAX_PANELS + ' panels.');
      change(function () { var c = clone(p); c.id = newPanelId(d); c.title = p.title + ' (copy)'; c.layout.y = p.layout.y + p.layout.h; d.panels.push(c); resolveLayout(d.panels, c); state.sel = c.id; });
    });
    var del = el('button', { type: 'button', 'class': 'dt-btn', text: 'Delete panel' });
    del.addEventListener('click', function () { change(function () { d.panels = d.panels.filter(function (x) { return x !== p; }); resolveLayout(d.panels); state.sel = null; }); toast('Panel deleted. Undo brings it back.'); });
    rail.appendChild(el('div', { 'class': 'dt-two' }, [dup, del]));
  }
  function renderDashSettings(rail, d) {
    railSection(rail, 'Dashboard');
    var t = el('input', { id: 'dtDashTitle', 'class': 'dt-inp', value: d.title, maxlength: '120' });
    t.addEventListener('change', function () { change(function () { d.title = t.value || 'Untitled'; }); });
    rail.appendChild(field('Title', t));
    var de = el('input', { id: 'dtDashDesc', 'class': 'dt-inp', value: d.description || '', maxlength: '400' });
    de.addEventListener('change', function () { change(function () { d.description = de.value; }); });
    rail.appendChild(field('Description', de));
    rail.appendChild(field('Opens on', selectOf('dtDashWin', (cfg.windows || []).map(function (w) { return [w.key, w.label]; }), d.window || state.window, function (v) { change(function () { d.window = v; }); })));
    railSection(rail, 'Variables');
    rail.appendChild(el('p', { 'class': 'dt-hint', text: 'A variable is picked in the bar above and written into titles and filters as $name, so one dashboard works for any area or scope.' }));
    var vchips = el('div', { 'class': 'dt-chips' });
    dashVars(d).forEach(function (v, i) {
      var x = el('button', { type: 'button', 'class': 'dt-fchip-x', 'aria-label': 'Remove $' + v.name, text: '×' });
      x.addEventListener('click', function () { change(function () { d.variables.splice(i, 1); }); });
      vchips.appendChild(el('span', { 'class': 'dt-fchip' }, [el('span', { 'class': 'dt-fchip-edit', text: '$' + v.name + ' · ' + v.kind + (v.default ? ' · ' + v.default : '') }), x]));
    });
    rail.appendChild(vchips);
    var vname = el('input', { id: 'dtVarName', 'class': 'dt-inp', placeholder: 'home', maxlength: '32' });
    var vkinds = (places() ? [['area', 'Area']] : []).concat([['scope', 'Scope'], ['channel', 'Channel'], ['text', 'Text']]);
    var vkind = selectOf('dtVarKind', vkinds, vkinds[0][0], function () { /* read on add */ });
    var vadd = el('button', { type: 'button', 'class': 'dt-btn', text: 'Add variable' });
    vadd.addEventListener('click', function () {
      var nm = vname.value.trim().toLowerCase();
      if (!VAR_RE.test(nm) || varOf(d, nm)) return toast('A variable needs a new lower-case name, such as home.');
      change(function () { (d.variables = d.variables || []).push({ name: nm, kind: vkind.value }); });
    });
    rail.appendChild(el('div', { 'class': 'dt-two' }, [field('Name', vname), field('Kind', vkind)]));
    rail.appendChild(vadd);
    railSection(rail, 'Panels');
    rail.appendChild(el('p', { 'class': 'dt-hint', text: 'Select a panel to change it. Drag ⠇ to move it and its corner to resize it, or use the arrow keys on ⠇.' }));
    var addp = el('button', { type: 'button', 'class': 'dt-btn dt-btn-primary', text: '+ Add panel' });
    addp.addEventListener('click', openAdd);
    rail.appendChild(addp);
    var deld = el('button', { type: 'button', 'class': 'dt-btn dt-btn-quiet', text: 'Delete this dashboard' });
    deld.addEventListener('click', function () {
      confirmDialog('Delete “' + d.title + '”?', 'It is removed from this browser. An exported copy can be imported again.', 'Delete', function () {
        mine = mine.filter(function (x) { return x.id !== d.id; }); saveMine(); state.edit = false; state.sel = null; state.id = library.default; undo = []; syncHash(); render(); toast('Dashboard deleted');
      });
    });
    rail.appendChild(deld);
  }
  function filterEditor(box, p, d) {
    var c = cubes[panelWindow(p)];
    if (!c) return;
    box.hidden = false;
    var f = { d: hasDim('heard') ? 'heard' : 'scope', n: 0, v: [] };
    var draw = function () {
      box.textContent = '';
      var D = DIMS[f.d];
      box.appendChild(el('div', { 'class': 'dt-two' }, [
        field('Dimension', selectOf('dtFDim', dimOptions(false), f.d, function (v) { f.d = v; f.v = []; draw(); })),
        field('Match', selectOf('dtFN', [['0', 'is'], ['1', 'is not']], String(f.n), function (v) { f.n = +v; }))
      ]));
      var counts = sumBy(c, filteredRows(c, panelFilters(p, d)), D.of);
      var vals = D.values(c).slice().sort(function (a, b) { return (counts[b] || 0) - (counts[a] || 0); });
      dashVars(d).forEach(function (v) { if ((D.area && v.kind === 'area') || (f.d === v.kind)) vals.unshift('$' + v.name); });
      var list = el('div', { 'class': 'dt-vchips' });
      vals.forEach(function (v) {
        var on = f.v.indexOf(v) >= 0, isVar = v.charAt(0) === '$';
        var b = el('button', { type: 'button', 'class': 'dt-vchip' + (counts[v] || isVar ? '' : ' is-empty'), 'aria-pressed': String(on) }, [
          el('span', { text: isVar ? v + ' (' + substTitle(v, d) + ')' : D.name(v) }),
          el('span', { 'class': 'dt-vcount', text: isVar ? '' : n(counts[v] || 0) })
        ]);
        b.addEventListener('click', function () { f.v = on ? f.v.filter(function (x) { return x !== v; }) : f.v.concat([v]); draw(); });
        list.appendChild(b);
      });
      box.appendChild(list);
      var ok = el('button', { type: 'button', 'class': 'dt-btn dt-btn-primary', text: 'Add filter' });
      ok.addEventListener('click', function () { if (!f.v.length) { box.hidden = true; return; } change(function () { (p.query.filters = p.query.filters || []).push(f); }); });
      var cancel = el('button', { type: 'button', 'class': 'dt-btn', text: 'Cancel' });
      cancel.addEventListener('click', function () { box.hidden = true; box.textContent = ''; });
      box.appendChild(el('div', { 'class': 'dt-two' }, [ok, cancel]));
    };
    draw();
  }
  function pickEditor(p, d) {
    var q = p.query, c = cubes[panelWindow(p)], D = DIMS[q.split];
    var counts = sumBy(c, filteredRows(c, panelFilters(p, d)), D.of);
    var vals = Object.keys(counts).sort(function (a, b) { return counts[b] - counts[a]; }).slice(0, 30);
    var box = el('div', { 'class': 'dt-picks' }, [el('p', { 'class': 'dt-hint', text: (q.pick && q.pick.length) ? 'Shown on their own; the rest are counted together:' : 'The six biggest are shown on their own. Pick values to choose them instead:' })]);
    var list = el('div', { 'class': 'dt-vchips' });
    vals.forEach(function (v) {
      var on = (q.pick || []).indexOf(v) >= 0;
      var sw = el('span', { 'class': 'dt-sw' }); sw.style.background = colourFor(D, v) || REST;
      var b = el('button', { type: 'button', 'class': 'dt-vchip', 'aria-pressed': String(on) }, [sw, el('span', { text: D.name(v) }), el('span', { 'class': 'dt-vcount', text: n(counts[v]) })]);
      b.addEventListener('click', function () { change(function () { q.pick = on ? q.pick.filter(function (x) { return x !== v; }) : (q.pick || []).concat([v]); if (!q.pick.length) delete q.pick; }); });
      list.appendChild(b);
    });
    box.appendChild(list);
    return box;
  }
  function newPanelId(d) { var i = d.panels.length + 1, id; do { id = 'panel-' + i++; } while (d.panels.some(function (p) { return p.id === id; })); return id; }

  // ---- dialogs ------------------------------------------------------------------
  function modalOpen() { var dlg = $('dtModal'); return !!dlg && dlg.hasAttribute('open'); }
  function closeModal() { var dlg = $('dtModal'); if (!dlg) return; if (dlg.close) dlg.close(); else dlg.removeAttribute('open'); }
  function dialog(build) {
    var dlg = $('dtModal'), box = $('dtModalIn');
    box.textContent = '';
    build(box, closeModal);
    if (!modalOpen()) { if (dlg.showModal) dlg.showModal(); else dlg.setAttribute('open', ''); }
  }
  function acts(kids) { return el('div', { 'class': 'dt-acts' }, kids); }
  function closeBtn(close, label) { var b = el('button', { type: 'button', 'class': 'dt-btn', text: label || 'Close' }); b.addEventListener('click', close); return b; }
  function confirmDialog(title, text, okLabel, fn) {
    dialog(function (box, close) {
      var ok = el('button', { type: 'button', 'class': 'dt-btn dt-btn-primary', text: okLabel });
      ok.addEventListener('click', function () { close(); fn(); });
      box.appendChild(el('h3', { id: 'dtModalTitle', text: title }));
      box.appendChild(el('p', { 'class': 'dt-modal-sum', text: text }));
      box.appendChild(acts([closeBtn(close, 'Cancel'), ok]));
    });
  }
  var toastTimer = null;
  function toast(msg) { var t = $('dtToast'); if (!t) return; t.textContent = msg; t.hidden = false; clearTimeout(toastTimer); toastTimer = setTimeout(function () { t.hidden = true; }, 3200); }
  function copyText(text, area) {
    var fallback = function () { area.select(); toast('Selected: press Ctrl+C to copy'); };
    if (navigator.clipboard && navigator.clipboard.writeText) navigator.clipboard.writeText(text).then(function () { toast('Copied'); }, fallback); else fallback();
  }
  function exportDoc(d) { var c = stripSource(d); if (c.id.indexOf('shared-') === 0) c.id = c.id.slice(7); return c; }
  function openExport() {
    var d = cur(), json = JSON.stringify(exportDoc(d), null, 2);
    dialog(function (box, close) {
      var ta = el('textarea', { 'class': 'dt-code', readonly: '', 'aria-label': 'Dashboard JSON' }); ta.value = json;
      var cp = el('button', { type: 'button', 'class': 'dt-btn dt-btn-primary', text: 'Copy JSON' });
      cp.addEventListener('click', function () { copyText(json, ta); });
      var dl = el('a', { 'class': 'dt-btn', href: 'data:application/json;charset=utf-8,' + encodeURIComponent(json), download: d.id.replace(/^shared-/, '') + '.json', text: 'Save file' });
      box.appendChild(el('h3', { id: 'dtModalTitle', text: 'Export “' + d.title + '”' }));
      box.appendChild(el('p', { 'class': 'dt-modal-sum', text: 'Anyone can import this file into the Data tab. A site operator can put it in the file named by data.dashboardsFile to offer it to every visitor.' }));
      box.appendChild(ta);
      box.appendChild(acts([closeBtn(close), dl, cp]));
    });
  }
  function openShare() {
    var d = cur(), url = location.href.split('#')[0] + '#/data?d=' + b64urlEncode(JSON.stringify(exportDoc(d)));
    dialog(function (box, close) {
      box.appendChild(el('h3', { id: 'dtModalTitle', text: 'Share a link' }));
      if (url.length > MAX_LINK) {
        box.appendChild(el('p', { 'class': 'dt-modal-sum', text: 'This layout is too big for a link every browser will open (' + n(url.length) + ' characters). Export it as a file instead.' }));
        box.appendChild(acts([closeBtn(close)]));
        return;
      }
      var ta = el('textarea', { 'class': 'dt-code dt-code-short', readonly: '', 'aria-label': 'Share link' }); ta.value = url;
      var cp = el('button', { type: 'button', 'class': 'dt-btn dt-btn-primary', text: 'Copy link' });
      cp.addEventListener('click', function () { copyText(url, ta); });
      box.appendChild(el('p', { 'class': 'dt-modal-sum', text: 'The whole layout travels in the link, so whoever opens it sees these panels. Nothing is stored on the server.' }));
      box.appendChild(ta);
      box.appendChild(acts([closeBtn(close), cp]));
    });
  }
  function importDocs(text) {
    var doc;
    try { doc = JSON.parse(text); } catch (e) { return 'That is not valid JSON. Paste the whole exported file.'; }
    var list = Array.isArray(doc) ? doc : (doc && Array.isArray(doc.dashboards) ? doc.dashboards : [doc]);
    if (!list.length) return 'The file holds no dashboards.';
    for (var i = 0; i < list.length; i++) { var bad = validateDashboard(list[i]); if (bad) return (list.length > 1 ? 'Dashboard ' + (i + 1) + ': ' : '') + bad; }
    list.forEach(function (x) { delete x.source; x.id = uniqueId(x.id); x.source = 'mine'; mine.push(x); });
    saveMine();
    state.id = list[list.length - 1].id; state.edit = false; state.sel = null; undo = [];
    return list;
  }
  function openImport() {
    dialog(function (box, close) {
      var ta = el('textarea', { 'class': 'dt-code', 'aria-label': 'Dashboard JSON to import', placeholder: '{ "schema": 1, "id": "…", "title": "…", "panels": [ … ] }' });
      var file = el('input', { type: 'file', accept: 'application/json,.json', 'aria-label': 'Choose a dashboard file' });
      file.addEventListener('change', function () { var f = file.files && file.files[0]; if (!f) return; var rd = new FileReader(); rd.onload = function () { ta.value = String(rd.result); }; rd.readAsText(f); });
      var err = el('p', { 'class': 'dt-err', role: 'alert' });
      var ok = el('button', { type: 'button', 'class': 'dt-btn dt-btn-primary', text: 'Import' });
      ok.addEventListener('click', function () {
        var res = importDocs(ta.value);
        if (typeof res === 'string') { err.textContent = res; return; }
        close(); syncHash(); render();
        toast(res.length > 1 ? 'Imported ' + res.length + ' dashboards' : 'Imported “' + res[0].title + '”');
      });
      box.appendChild(el('h3', { id: 'dtModalTitle', text: 'Import a dashboard' }));
      box.appendChild(el('p', { 'class': 'dt-modal-sum', text: 'Choose an exported .json file or paste it. It is checked before anything changes, then added to Your dashboards in this browser.' }));
      box.appendChild(file); box.appendChild(ta); box.appendChild(err);
      box.appendChild(acts([closeBtn(close, 'Cancel'), ok]));
    });
  }
  var ADD_DEFAULTS = {
    bar: [{ x: 'time', split: 'kind' }, 'Packets by kind', 6, 4],
    line: [{ x: 'time', split: 'heard' }, 'Packets by receiving area', 6, 4, 'Packets by kind'],
    area: [{ x: 'time', split: 'kind' }, 'Packets over time', 6, 4],
    matrix: [{ rows: 'origin', cols: 'heard' }, 'Where it started and where it was heard', 6, 4, 'Scope by kind'],
    stat: [{}, 'Packets heard', 3, 2],
    table: [{ x: 'origin', split: 'kind' }, 'Packets by start and kind', 6, 4, 'Packets by scope and kind'],
    top: [{ dim: 'scope', top: 8 }, 'Busiest scopes', 4, 4],
    text: [{}, 'Note', 4, 2]
  };
  // addDefaults: the query a new panel starts with, on dimensions that exist.
  function addDefaults(type) {
    var q = clone(ADD_DEFAULTS[type][0]);
    if (places()) return q;
    if (type === 'line') q.split = 'kind';
    if (type === 'matrix') { q.rows = 'scope'; q.cols = 'kind'; }
    if (type === 'table') { q.x = 'scope'; q.split = 'kind'; }
    return q;
  }
  function openAdd() {
    var d = cur();
    if (!editable(d)) { startEdit(); d = cur(); }
    if (d.panels.length >= MAX_PANELS) return toast('A dashboard holds at most ' + MAX_PANELS + ' panels.');
    dialog(function (box, close) {
      box.appendChild(el('h3', { id: 'dtModalTitle', text: 'Add a panel' }));
      box.appendChild(el('p', { 'class': 'dt-modal-sum', text: 'Every panel counts the same packets. Choose its filters and chart after adding it.' }));
      var types = el('div', { 'class': 'dt-types' });
      PANEL_TYPES.forEach(function (t) {
        var b = el('button', { type: 'button' }, [el('b', { text: t[1] }), el('span', { text: t[2] })]);
        b.addEventListener('click', function () {
          close();
          var def = ADD_DEFAULTS[t[0]];
          change(function () {
            var p = { id: newPanelId(d), title: places() ? def[1] : def[4] || def[1], type: t[0], query: addDefaults(t[0]), layout: { x: 0, y: nextFreeY(d.panels), w: def[2], h: def[3] } };
            if (t[0] === 'text') p.options = { text: 'Write a note for readers of this dashboard.' };
            d.panels.push(p); resolveLayout(d.panels, p); state.sel = p.id;
          });
          toast('Panel added');
        });
        types.appendChild(b);
      });
      box.appendChild(types);
      box.appendChild(acts([closeBtn(close, 'Cancel')]));
    });
  }
  function startEdit() {
    var d = cur();
    if (!editable(d)) { copyToMine(d); toast('Editing your copy. The original stays as it is for everyone else.'); }
    state.edit = true; state.sel = null; undo = []; syncHash(); render();
  }

  // ---- packets behind a figure (the drill-down) ----------------------------------
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
    origin: { label: 'From', get: function (r) { return areaName(r.origin); } },
    basis: { label: 'Start placed by', get: function (r) { return BASIS[r.basis] || r.basis; } },
    heard: { label: 'Heard in', get: function (r) { return areaName(r.heard); } },
    through: { label: 'Through', get: function (r) { return DIMS.through.name(r.through); } },
    how: { label: 'How it arrived', get: function (r) { return HOW_LABEL[r.how] || r.how; } },
    observers: { label: 'Observers', get: function (r) { return r.observers; }, num: true },
    heardFrom: { label: 'Heard from', get: function (r) { return r.heardFrom || ''; } },
    boundary: { label: 'Boundary repeater', get: function (r) { return r.boundary || ''; } }
  };
  var PLACE_COLS = { border: true, origin: true, basis: true, heard: true, through: true, how: true, heardFrom: true, boundary: true };
  function colOk(k) { return places() || !PLACE_COLS[k]; }
  var COL_ORDER = Object.keys(COLS), DEFAULT_COLS = ['at', 'typeName', 'scope', 'origin', 'heard', 'how', 'hash'], COLS_KEY = 'corescope-data-cols';
  function loadCols() {
    try { var c = JSON.parse(localStorage.getItem(COLS_KEY) || 'null'); if (Array.isArray(c) && c.length && c.every(function (k) { return COLS[k]; })) return c; } catch (e) { /* defaults */ }
    return DEFAULT_COLS.slice();
  }
  var cols = loadCols();
  function saveCols() { try { localStorage.setItem(COLS_KEY, JSON.stringify(cols)); } catch (e) { /* not fatal */ } }
  var drill = { rows: [], total: 0, sort: 'at', dir: -1, shown: 200 };
  function openDrill(p, d, filters, from, to, label, w) {
    var my = gen;
    drill = { rows: [], total: 0, sort: 'at', dir: -1, shown: 200 };
    dialog(function (box, close) {
      var head = el('div', { 'class': 'dt-modal-head' }, [el('div', null, [
        el('h3', { id: 'dtModalTitle', text: substTitle(p.title, d) + ': ' + label }),
        el('p', { 'class': 'dt-modal-sum', id: 'dtDrillSum', text: 'Loading the packets behind this figure…' })
      ]), closeBtn(close)]);
      box.appendChild(head);
      box.appendChild(el('div', { 'class': 'dt-chips dt-modal-cols', id: 'dtDrillCols' }));
      box.appendChild(el('div', { 'class': 'dt-modal-table', id: 'dtDrillTable' }));
      renderColChips();
    });
    var params = 'window=' + encodeURIComponent(w) + '&f=' + encodeURIComponent(JSON.stringify(filters)) + (from !== undefined ? '&from=' + from + '&to=' + to : '');
    getJSON('/api/data/packets?' + params).then(function (res) {
      if (my !== gen) return;
      drill.rows = res.rows || []; drill.total = res.total || 0;
      var s = $('dtDrillSum');
      if (s) s.textContent = n(drill.total) + ' packets over the ' + windowLabel(w) + '.' + (drill.total > drill.rows.length ? ' Showing the ' + n(drill.rows.length) + ' newest; narrow the selection to see the rest.' : '') + (drill.rows.length ? ' Select a packet to open it.' : '');
      renderDrillTable();
    }).catch(function () { var s = $('dtDrillSum'); if (s) s.textContent = 'The packets could not be loaded. Try again shortly.'; });
  }
  function renderColChips() {
    var box = $('dtDrillCols'); if (!box) return;
    box.textContent = '';
    box.appendChild(el('span', { 'class': 'dt-hint', text: 'Columns' }));
    cols.forEach(function (k, i) {
      if (!colOk(k)) return;
      var x = el('button', { type: 'button', 'class': 'dt-fchip-x', 'aria-label': 'Remove the ' + COLS[k].label + ' column', text: '×' });
      if (cols.length === 1) x.disabled = true;
      x.addEventListener('click', function () { cols.splice(i, 1); saveCols(); renderColChips(); renderDrillTable(); });
      box.appendChild(el('span', { 'class': 'dt-fchip' }, [el('span', { 'class': 'dt-fchip-edit', text: COLS[k].label }), x]));
    });
    var free = COL_ORDER.filter(function (k) { return cols.indexOf(k) < 0 && colOk(k); });
    if (!free.length) return;
    var s = el('select', { 'aria-label': 'Add a column', 'class': 'dt-addcol' });
    s.appendChild(el('option', { value: '', text: '+ Add column' }));
    free.forEach(function (k) { s.appendChild(el('option', { value: k, text: COLS[k].label })); });
    s.addEventListener('change', function () { if (s.value) { cols.push(s.value); saveCols(); renderColChips(); renderDrillTable(); } });
    box.appendChild(s);
  }
  function renderDrillTable() {
    var box = $('dtDrillTable'); if (!box) return;
    box.textContent = '';
    if (!drill.rows.length) return;
    var C = COLS[drill.sort] || COLS.at;
    var rows = drill.rows.slice().sort(function (a, b) { var x1 = C.get(a), x2 = C.get(b); return C.num ? (x1 - x2) * drill.dir : String(x1).localeCompare(String(x2), undefined, { numeric: true }) * drill.dir; });
    var t = el('table', { 'class': 'dt-t dt-drill-t' }), hr = el('tr'), shown = cols.filter(colOk);
    shown.forEach(function (k) {
      var on = drill.sort === k;
      var th = el('th', { scope: 'col', 'aria-sort': on ? (drill.dir > 0 ? 'ascending' : 'descending') : 'none', 'class': COLS[k].num ? 'dt-n' : '' });
      var b = el('button', { type: 'button', 'class': 'dt-sort', text: COLS[k].label + (on ? (drill.dir > 0 ? ' ↑' : ' ↓') : '') });
      b.addEventListener('click', function () { if (drill.sort === k) drill.dir = -drill.dir; else { drill.sort = k; drill.dir = COLS[k].num ? -1 : 1; } renderDrillTable(); });
      th.appendChild(b); hr.appendChild(th);
    });
    t.appendChild(el('thead', null, [hr]));
    var tb = el('tbody');
    rows.slice(0, drill.shown).forEach(function (r) {
      var href = '#/packets/' + encodeURIComponent(r.hash);
      var tr = el('tr', { 'class': 'dt-link-row' });
      shown.forEach(function (k) {
        var C2 = COLS[k], text = C2.show ? C2.show(r) : String(C2.get(r));
        var td = el('td', { 'class': (C2.num ? 'dt-n ' : '') + (k === 'hash' ? 'dt-code-cell' : '') });
        if (k === 'hash') td.appendChild(el('a', { href: href, text: text })); else td.textContent = text;
        tr.appendChild(td);
      });
      tr.addEventListener('click', function (ev) { closeModal(); if (ev.target.tagName !== 'A') location.hash = href; });
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

  // ---- page ---------------------------------------------------------------------
  function render() {
    if (!root) return;
    var d = cur();
    redrawers = [];
    renderBar(d);
    $('dtDTitle').textContent = d.title;
    $('dtDDesc').textContent = substTitle(d.description || '', d);
    root.classList.toggle('dt-editing', state.edit);
    var note = $('dtNote');
    note.hidden = !(state.edit || d.source === 'shared');
    note.textContent = state.edit ? 'Edit mode. Changes are saved in this browser as you make them; Undo (Ctrl+Z) steps back.'
      : d.source === 'shared' ? 'A dashboard shared with you. Use Edit a copy to keep it in Your dashboards.' : '';
    renderRail(d);
    var board = $('dtBoard'); board.textContent = '';
    if (!d.panels.length) {
      board.appendChild(el('div', { 'class': 'dt-empty-dash' }, [el('strong', { text: 'This dashboard has no panels yet.' }), el('span', { text: state.edit ? ' Add the first one.' : ' Choose Edit, then Add panel.' })]));
    }
    d.panels.forEach(function (p) { board.appendChild(renderPanel(p, d)); });
    if (state.edit && d.panels.length < MAX_PANELS) {
      var add = el('button', { type: 'button', 'class': 'dt-addtile', text: '+ Add panel' });
      add.style.gridColumn = '1 / span 3';
      add.style.gridRow = (nextFreeY(d.panels) + 1) + ' / span 2';
      add.addEventListener('click', openAdd);
      board.appendChild(add);
    }
    var my = gen;
    ensureCubes(d).then(function () {
      if (my !== gen || !root) return;
      renderBar(cur()); if (state.edit) renderRail(cur()); redrawAll();
    }, function () { if (my === gen) toast('The figures for this period could not be loaded. Try again shortly.'); });
    redrawAll();
  }
  function redrawAll() { requestAnimationFrame(function () { redrawers.forEach(function (f) { f(); }); }); }
  function setFull(on) {
    document.body.classList.toggle('dt-full', on);
    var b = $('dtFill'); if (b) { b.setAttribute('aria-pressed', String(on)); b.textContent = on ? 'Exit full screen' : 'Full screen'; }
    redrawAll();
  }
  function wireBar() {
    $('dtDash').addEventListener('change', function (e) {
      state.id = e.target.value; state.sel = null; undo = [];
      var d = cur();
      if (windowKeys().indexOf(d.window) >= 0) state.window = d.window;
      if (!editable(d)) state.edit = false;
      syncHash(); render();
    });
    $('dtEdit').addEventListener('click', function () { if (state.edit) { state.edit = false; state.sel = null; render(); toast('Saved in this browser'); } else startEdit(); });
    $('dtUndo').addEventListener('click', undoLast);
    $('dtNew').addEventListener('click', function () {
      var d = { schema: 1, id: uniqueId('my-dashboard'), title: 'My dashboard', window: state.window, panels: [], source: 'mine' };
      mine.push(d); saveMine(); state.id = d.id; state.edit = true; state.sel = null; undo = []; syncHash(); render(); toast('New dashboard. Add a panel to start.');
    });
    $('dtImport').addEventListener('click', openImport);
    $('dtExport').addEventListener('click', openExport);
    $('dtShare').addEventListener('click', openShare);
    $('dtFill').addEventListener('click', function () { setFull(!document.body.classList.contains('dt-full')); });
    $('dtModal').addEventListener('click', function (ev) { if (ev.target === ev.currentTarget) closeModal(); });
  }
  function showOff(container, text) {
    container.textContent = '';
    container.appendChild(el('div', { 'class': 'dt-off' }, [el('h2', { text: 'Data' }), el('p', { text: text })]));
  }

  function init(container) {
    gen++;
    root = null; cfg = null; library = null; shared = null; cubes = {}; loading = {}; redrawers = []; undo = [];
    state = { id: '', window: '', vars: {}, edit: false, sel: null };
    var my = gen;
    container.textContent = '';
    container.appendChild(el('div', { 'class': 'dt-off', text: 'Loading…' }));
    getJSON('/api/data/config').then(function (c) {
      if (my !== gen) return;
      if (!c.enabled || !(c.windows || []).length) { showOff(container, 'The Data tab is not switched on for this CoreScope. An operator turns it on with data.enabled and an areas file in config.json; see config.example.json.'); return; }
      cfg = c;
      areaSlot = {}; (cfg.areas || []).forEach(function (a, i) { if (i < SLOTS.length) areaSlot[a.code] = i; });
      return getJSON('/api/data/dashboards').then(function (lib) {
        if (my !== gen) return;
        library = lib;
        mine = loadMine();
        container.innerHTML = pageHtml();
        root = $('dtPage');
        tipEl = $('dtTip');
        readHash();
        wireBar();
        syncHash();
        render();
      });
    }).catch(function () {
      if (my !== gen) return;
      showOff(container, 'The Data tab could not be loaded. Try again shortly.');
    });
    resizeHandler = function () { clearTimeout(resizeTimer); resizeTimer = setTimeout(redrawAll, 150); };
    window.addEventListener('resize', resizeHandler);
    keyHandler = function (ev) {
      if (!root) return;
      if (modalOpen()) return;
      if ((ev.ctrlKey || ev.metaKey) && ev.key === 'z' && state.edit && !/^(INPUT|TEXTAREA|SELECT)$/.test(ev.target.tagName)) { ev.preventDefault(); undoLast(); return; }
      if (ev.key !== 'Escape') return;
      if (state.edit && state.sel) { state.sel = null; render(); return; }
      if (document.body.classList.contains('dt-full')) setFull(false);
    };
    document.addEventListener('keydown', keyHandler);
  }
  function destroy() {
    gen++;
    root = null; tipEl = null;
    document.body.classList.remove('dt-full');
    if (resizeHandler) window.removeEventListener('resize', resizeHandler);
    if (keyHandler) document.removeEventListener('keydown', keyHandler);
    resizeHandler = keyHandler = null;
    clearTimeout(resizeTimer);
  }

  if (typeof window !== 'undefined') {
    // For the tests: the pieces whose answers must agree with the server.
    window.__corescopeDataInternals = {
      DIMS: DIMS, rolledBuckets: rolledBuckets, niceMax: niceMax, validateDashboard: validateDashboard,
      resolveLayout: resolveLayout, seriesQuery: seriesQuery, passes: passes,
      b64urlEncode: b64urlEncode, b64urlDecode: b64urlDecode,
      setConfig: function (c) { cfg = c; },
      subst: subst
    };
  }

  registerPage('data', { init: init, destroy: destroy });
})();
