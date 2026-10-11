/* companion-contacts.js — Tools › Companion Contacts.
 *
 * Add the nodes CoreScope has heard to a MeshCore companion's contacts, and
 * tidy up the contacts it already has, over USB (Web Serial) or Bluetooth
 * (Web Bluetooth). The radio protocol is companion-link.js; this file is the
 * page: a map to draw an area on (circle, rectangle or free shape), filters,
 * and the add / remove flows with progress and a confirmation.
 *
 * Everything happens in the browser: nodes come from /api/nodes, and the
 * server never talks to the radio. Deep link: #/tools/companion-contacts
 * ?tab=add|manage.
 */
(function () {
  'use strict';

  var CL = window.CompanionLink;
  var ROLES = ['repeater', 'room', 'companion', 'sensor'];
  var ROLE_LABEL = { repeater: 'Repeaters', room: 'Room servers', companion: 'Companions', sensor: 'Sensors' };
  var HEARD = { '24h': 86400, '7d': 7 * 86400, '30d': 30 * 86400, any: 0 };

  var st = null; // page state, reset on every init

  function roleOf(r) {
    var s = String(r || '').toLowerCase();
    if (s.indexOf('repeater') >= 0) return 'repeater';
    if (s.indexOf('room') >= 0) return 'room';
    if (s.indexOf('sensor') >= 0) return 'sensor';
    if (s.indexOf('companion') >= 0 || s.indexOf('chat') >= 0) return 'companion';
    return null;
  }
  // cssColour resolves a theme variable to a value: the map draws on a
  // canvas, which cannot resolve var().
  function cssColour(name, fallback) {
    var v = getComputedStyle(document.documentElement).getPropertyValue(name).trim();
    return v || fallback;
  }
  function roleColour(role) { return cssColour('--role-' + role, '#888'); }
  function heardAt(n) {
    var t = Date.parse(n.last_heard || n.last_seen || '');
    return isNaN(t) ? 0 : t / 1000;
  }
  function ago(sec) {
    if (!sec) return 'never';
    var d = Date.now() / 1000 - sec;
    if (d < 3600) return Math.max(1, Math.round(d / 60)) + ' min ago';
    if (d < 86400) return Math.round(d / 3600) + ' h ago';
    return Math.round(d / 86400) + ' d ago';
  }
  function plural(n, one, many) { return n + ' ' + (n === 1 ? one : many); }
  function $(id) { return document.getElementById(id); }

  function tabFromHash() {
    var q = (location.hash.split('?')[1] || '');
    var t = new URLSearchParams(q).get('tab');
    return t === 'manage' ? 'manage' : 'add';
  }

  // ---- markup -------------------------------------------------------------

  function render(container) {
    var roleBoxes = ROLES.map(function (r) {
      var on = r === 'repeater' || r === 'room';
      return '<label class="cc-chip"><input type="checkbox" value="' + r + '"' + (on ? ' checked' : '') + '>' + ROLE_LABEL[r] + '</label>';
    }).join('');
    container.innerHTML =
      '<div class="cc-page">' +
        '<div class="cc-head">' +
          '<div>' +
            '<div class="cc-crumb"><a href="#/tools">Tools</a> › Companion Contacts</div>' +
            '<h2>Companion Contacts</h2>' +
            '<p class="cc-sub">Add the nodes CoreScope has heard to your MeshCore companion, or tidy up the contacts it already has.</p>' +
          '</div>' +
          '<div class="cc-tabs" role="tablist" aria-label="Companion Contacts">' +
            '<button type="button" role="tab" id="ccTabAdd" aria-controls="ccAddPane">Add from map</button>' +
            '<button type="button" role="tab" id="ccTabManage" aria-controls="ccManagePane">Manage device contacts</button>' +
          '</div>' +
        '</div>' +
        '<section class="cc-conn" aria-label="Companion connection">' +
          '<span class="cc-dot" id="ccDot" aria-hidden="true"></span>' +
          '<div class="cc-conn-text" id="ccStatus" role="status">Not connected</div>' +
          '<div class="cc-cap" aria-hidden="true"><div id="ccCapBar"></div></div>' +
          '<div class="cc-conn-actions">' +
            '<button type="button" class="btn" id="ccConnectUsb">Connect over USB</button>' +
            '<button type="button" class="btn" id="ccConnectBle">Connect over Bluetooth</button>' +
            '<button type="button" class="btn" id="ccDisconnect" hidden>Disconnect</button>' +
          '</div>' +
          '<p class="cc-note" id="ccUnsupported" hidden>This browser can\'t talk to a radio. Use Chrome or Edge on a computer (USB or Bluetooth), or Chrome on Android (Bluetooth). Safari, Firefox and iPhone browsers don\'t support Web Serial or Web Bluetooth.</p>' +
          '<p class="cc-note" id="ccConnError" hidden></p>' +
        '</section>' +
        '<div class="cc-body">' +
          '<div class="cc-map-wrap">' +
            '<div id="ccMap" class="cc-map"></div>' +
            '<div class="cc-tools" role="toolbar" aria-label="Select an area">' +
              '<button type="button" id="ccToolCircle" aria-label="Select with a circle" aria-pressed="false"><svg aria-hidden="true" viewBox="0 0 24 24"><circle cx="12" cy="12" r="8"/></svg></button>' +
              '<button type="button" id="ccToolRect" aria-label="Select with a rectangle" aria-pressed="false"><svg aria-hidden="true" viewBox="0 0 24 24"><rect x="4" y="6" width="16" height="12" rx="1"/></svg></button>' +
              '<button type="button" id="ccToolPoly" aria-label="Select by drawing an area (double-click to finish)" aria-pressed="false"><svg aria-hidden="true" viewBox="0 0 24 24"><path d="M5 7l6-3 8 4-2 9-9 2z"/></svg></button>' +
              '<button type="button" id="ccToolClear" aria-label="Clear the selection"><svg aria-hidden="true" viewBox="0 0 24 24"><path d="M6 6l12 12M18 6L6 18"/></svg></button>' +
            '</div>' +
            '<div class="cc-hint" id="ccHint" hidden></div>' +
            '<div class="cc-legend" id="ccLegend"></div>' +
          '</div>' +
          '<aside class="cc-side">' +
            '<div id="ccAddPane" role="tabpanel" aria-labelledby="ccTabAdd">' +
              '<section class="cc-card">' +
                '<h3>Which nodes</h3>' +
                '<fieldset id="ccRoles" class="cc-chips"><legend>Roles</legend>' + roleBoxes + '</fieldset>' +
                '<label class="cc-field" id="ccAreaField" hidden>Area<select id="ccArea"></select></label>' +
                '<label class="cc-field">Heard within<select id="ccHeard"><option value="7d">Last 7 days</option><option value="24h">Last 24 hours</option><option value="30d">Last 30 days</option><option value="any">Any time</option></select></label>' +
              '</section>' +
              '<section class="cc-card">' +
                '<h3>Selection</h3>' +
                '<div class="cc-counts">' +
                  '<div><strong id="ccCountArea">0</strong><span>in area</span></div>' +
                  '<div><strong id="ccCountNew" class="cc-accent">0</strong><span>new</span></div>' +
                  '<div><strong id="ccCountHave">0</strong><span>already added</span></div>' +
                '</div>' +
                '<p class="cc-fit" id="ccFitNote">Draw an area on the map, or pick one from the list, to choose nodes.</p>' +
                '<ul class="cc-list" id="ccAddList"></ul>' +
                '<button type="button" class="btn btn-primary cc-go" id="ccAddBtn" disabled>Connect a companion to add contacts</button>' +
                '<label class="cc-check"><input type="checkbox" id="ccUpdateExisting"> Also update the ones already on the device (name, position)</label>' +
              '</section>' +
            '</div>' +
            '<div id="ccManagePane" role="tabpanel" aria-labelledby="ccTabManage" hidden>' +
              '<section class="cc-card">' +
                '<div class="cc-card-head"><h3>Narrow it down</h3><button type="button" class="btn" id="ccFindStale">Find stale contacts</button></div>' +
                '<label class="cc-field">Type<select id="ccMType"><option value="">All types</option><option value="repeater">Repeaters</option><option value="companion">Companions</option><option value="room">Room servers</option><option value="sensor">Sensors</option></select></label>' +
                '<label class="cc-field" id="ccMAreaField" hidden>Area<select id="ccMArea"></select></label>' +
                '<label class="cc-field">Not heard for<select id="ccMStale"><option value="0">Any time</option><option value="30">30 days or more</option><option value="60">60 days or more</option><option value="90">90 days or more</option><option value="180">180 days or more</option></select></label>' +
                '<p class="cc-fit">Heard means its newest advert on the device, or CoreScope hearing it, whichever is later.</p>' +
                '<label class="cc-check"><input type="checkbox" id="ccMNoPos"> Include contacts with no position when drawing</label>' +
              '</section>' +
              '<section class="cc-card">' +
                '<h3 id="ccRemoveTitle">Nothing selected</h3>' +
                '<p class="cc-fit" id="ccManageNote">Connect a companion, then draw round the contacts you want to remove, pick an area, or find stale ones.</p>' +
                '<ul class="cc-list" id="ccRemoveList"></ul>' +
                '<button type="button" class="btn cc-go cc-danger" id="ccRemoveBtn" disabled>Remove contacts…</button>' +
              '</section>' +
            '</div>' +
          '</aside>' +
        '</div>' +
        '<section class="cc-progress" id="ccProgress" hidden aria-live="polite">' +
          '<div class="cc-progress-card">' +
            '<h3 id="ccProgressTitle"></h3>' +
            '<div class="cc-bar" role="progressbar" id="ccProgressBar" aria-valuemin="0" aria-valuenow="0"><div id="ccProgressFill"></div></div>' +
            '<p class="cc-fit" id="ccProgressNote"></p>' +
            '<ul class="cc-list cc-results" id="ccProgressList"></ul>' +
            '<div class="cc-row cc-end">' +
              '<button type="button" class="btn" id="ccStopBtn">Stop after this one</button>' +
              '<button type="button" class="btn btn-primary" id="ccDoneBtn" hidden>Done</button>' +
            '</div>' +
          '</div>' +
        '</section>' +
        '<dialog class="cc-dialog" id="ccConfirm" aria-labelledby="ccConfirmTitle">' +
          '<h3 id="ccConfirmTitle"></h3>' +
          '<p>They are deleted from the device, with any known path to them. They come back if you add them from the map, or if auto-add is on and the device hears their advert again.</p>' +
          '<ul class="cc-list" id="ccConfirmList"></ul>' +
          '<div class="cc-row cc-end">' +
            '<button type="button" class="btn" id="ccConfirmCancel">Cancel</button>' +
            '<button type="button" class="btn cc-danger" id="ccConfirmGo"></button>' +
          '</div>' +
        '</dialog>' +
      '</div>';
  }

  // ---- connection -----------------------------------------------------------

  function setStatus() {
    var s = $('ccStatus'), dot = $('ccDot'), bar = $('ccCapBar');
    var connected = !!st.client && !!st.info;
    dot.classList.toggle('is-on', connected);
    $('ccConnectUsb').hidden = connected;
    $('ccConnectBle').hidden = connected;
    $('ccDisconnect').hidden = !connected;
    if (!connected) {
      s.innerHTML = '<strong>Not connected</strong><span>Plug the companion in by USB, or connect over Bluetooth. Close the MeshCore app first: the radio takes one connection at a time.</span>';
      bar.style.width = '0';
    } else {
      var used = st.deviceContacts.length, max = st.info.maxContacts || 0;
      s.innerHTML = '<strong>Connected over ' + (st.kind === 'usb' ? 'USB' : 'Bluetooth') + ' · ' + escapeHtml(st.info.name || 'companion') + '</strong>' +
        '<span>' + (st.info.firmware ? 'Firmware ' + escapeHtml(st.info.firmware) + ' · ' : '') +
        (st.info.manufacturer ? escapeHtml(st.info.manufacturer) + ' · ' : '') +
        'contacts ' + used + ' of ' + max + ' used</span>';
      bar.style.width = max ? Math.min(100, Math.round(used / max * 100)) + '%' : '0';
    }
  }

  function connErr(msg) {
    var e = $('ccConnError');
    e.textContent = msg || '';
    e.hidden = !msg;
  }

  function connect(kind) {
    connErr('');
    var opener = kind === 'usb' ? CL.connectSerial : CL.connectBluetooth;
    var my = st;
    opener().then(function (t) {
      if (my !== st) { t.close(); return null; }
      st.kind = kind;
      st.client = new CL.Client(t);
      t.onClose = function () {
        if (my !== st) return;
        st.client = null; st.info = null; st.deviceContacts = [];
        connErr('The companion disconnected.');
        refresh();
      };
      return st.client.start().then(function (info) {
        st.info = info;
        return reloadDevice();
      });
    }).catch(function (e) {
      if (e && e.name === 'NotFoundError') return; // the user closed the picker
      if (st.client) { st.client.close(); st.client = null; st.info = null; }
      connErr('Could not connect: ' + (e && e.message || e) + '. Is it a ' + (kind === 'usb' ? 'USB' : 'Bluetooth') + ' companion build, and is nothing else connected to it?');
      refresh();
    });
  }

  function disconnect() {
    if (st.client) st.client.close();
    st.client = null; st.info = null; st.deviceContacts = [];
    refresh();
  }

  function reloadDevice() {
    return st.client.listContacts().then(function (list) {
      st.deviceContacts = list;
      st.areaMemo = {}; // positions may have changed
      refresh();
    });
  }

  // ---- map and drawing ----------------------------------------------------

  function initMap() {
    var map = L.map('ccMap', { preferCanvas: true, zoomControl: true, attributionControl: false });
    if (typeof window._applyTilesToNodeMap === 'function') window._applyTilesToNodeMap(map);
    else L.tileLayer('https://tile.openstreetmap.org/{z}/{x}/{y}.png', { maxZoom: 19 }).addTo(map);
    map.setView([0, 0], 2);
    st.map = map;
    st.markers = L.layerGroup().addTo(map);
    st.shapeLayer = L.layerGroup().addTo(map);
    st.areaLayer = L.layerGroup().addTo(map);
    var el = map.getContainer();
    el.addEventListener('pointerdown', onDown);
    el.addEventListener('pointermove', onMove);
    el.addEventListener('pointerup', onUp);
    el.addEventListener('dblclick', onDblClick);
  }

  function latlngOf(e) { return st.map.mouseEventToLatLng(e); }

  function setTool(tool) {
    st.tool = st.tool === tool ? null : tool;
    ['Circle', 'Rect', 'Poly'].forEach(function (t) {
      $('ccTool' + t).setAttribute('aria-pressed', String(st.tool === t.toLowerCase()));
    });
    var drawing = !!st.tool;
    if (drawing) { st.map.dragging.disable(); st.map.doubleClickZoom.disable(); }
    else { st.map.dragging.enable(); st.map.doubleClickZoom.enable(); }
    st.map.getContainer().classList.toggle('is-drawing', drawing);
    var hint = $('ccHint');
    hint.hidden = !drawing;
    hint.textContent = st.tool === 'circle' ? 'Press at the centre and drag out the radius.'
      : st.tool === 'rect' ? 'Press at one corner and drag to the other.'
      : st.tool === 'poly' ? 'Click each corner; double-click (or click the first point) to finish.' : '';
    st.polyPts = [];
  }

  function onDown(e) {
    if (!st.tool || e.button > 0) return;
    if (e.target.closest && e.target.closest('.leaflet-control')) return;
    var ll = latlngOf(e);
    if (st.tool === 'poly') {
      if (st.polyPts.length >= 3) {
        var first = st.map.latLngToContainerPoint(st.polyPts[0]);
        var here = st.map.latLngToContainerPoint(ll);
        if (first.distanceTo(here) < 12) { finishPoly(); return; }
      }
      st.polyPts.push(ll);
      drawShape({ kind: 'poly', latlngs: st.polyPts.slice(), open: true });
      return;
    }
    st.dragStart = ll;
    try { e.target.setPointerCapture(e.pointerId); } catch (x) { /* not capturable */ }
    e.preventDefault();
  }

  function onMove(e) {
    if (!st.dragStart) return;
    var ll = latlngOf(e);
    if (st.tool === 'circle') drawShape({ kind: 'circle', center: st.dragStart, radiusM: st.map.distance(st.dragStart, ll) });
    else if (st.tool === 'rect') drawShape({ kind: 'rect', bounds: L.latLngBounds(st.dragStart, ll) });
  }

  function onUp(e) {
    if (!st.dragStart) return;
    var start = st.dragStart;
    st.dragStart = null;
    var ll = latlngOf(e);
    var shape = st.tool === 'circle' ? { kind: 'circle', center: start, radiusM: st.map.distance(start, ll) }
      : { kind: 'rect', bounds: L.latLngBounds(start, ll) };
    if ((shape.kind === 'circle' && shape.radiusM < 1) || (shape.kind === 'rect' && start.equals(ll))) return;
    setShape(shape);
    setTool(null);
  }

  function onDblClick(e) {
    if (st.tool === 'poly') { e.preventDefault(); finishPoly(); }
  }

  function finishPoly() {
    if (st.polyPts.length < 3) return;
    setShape({ kind: 'poly', latlngs: st.polyPts.slice() });
    setTool(null);
  }

  function drawShape(shape) {
    st.shapeLayer.clearLayers();
    if (!shape) return;
    var colour = st.tab === 'manage' ? cssColour('--palette-red-500', '#ef4444') : cssColour('--accent', '#3b82f6');
    var o = { color: colour, weight: 2, fillOpacity: 0.12, dashArray: st.tab === 'manage' ? '6 4' : null };
    if (shape.kind === 'circle') L.circle(shape.center, Object.assign({ radius: shape.radiusM }, o)).addTo(st.shapeLayer);
    else if (shape.kind === 'rect') L.rectangle(shape.bounds, o).addTo(st.shapeLayer);
    else if (shape.open) L.polyline(shape.latlngs, o).addTo(st.shapeLayer);
    else L.polygon(shape.latlngs, o).addTo(st.shapeLayer);
  }

  function setShape(shape) {
    st.shape = shape;
    st.excluded = {};
    drawShape(shape);
    refresh();
  }

  function inside(shape, lat, lon) {
    if (!shape) return false;
    if (shape.kind === 'circle') return st.map.distance(shape.center, [lat, lon]) <= shape.radiusM;
    if (shape.kind === 'rect') return shape.bounds.contains([lat, lon]);
    // Ray casting over the polygon's vertices (lat/lon as plane coordinates;
    // fine at the sizes people draw round a region).
    var pts = shape.latlngs, c = false;
    for (var i = 0, j = pts.length - 1; i < pts.length; j = i++) {
      var yi = pts[i].lat, xi = pts[i].lng, yj = pts[j].lat, xj = pts[j].lng;
      if (((yi > lat) !== (yj > lat)) && (lon < (xj - xi) * (lat - yi) / (yj - yi) + xi)) c = !c;
    }
    return c;
  }

  // ---- add from map -------------------------------------------------------

  function deviceKeySet() {
    var s = {};
    st.deviceContacts.forEach(function (c) { s[c.key] = c; });
    return s;
  }

  function addSelection() {
    var roles = {};
    Array.prototype.forEach.call(document.querySelectorAll('#ccRoles input'), function (cb) { roles[cb.value] = cb.checked; });
    var window_ = HEARD[$('ccHeard').value] || 0;
    var area = $('ccArea').value;
    var now = Date.now() / 1000;
    var have = deviceKeySet();
    var inArea = [], fresh = [], already = [];
    st.nodes.forEach(function (n) {
      if (!n._placed || !n._role || !roles[n._role]) return;
      if (!st.shape && !area) return;
      if (window_ && (now - n._heard) > window_) return;
      if (st.shape && !inside(st.shape, n.lat, n.lon)) return;
      if (!areaPicked(area, nodeAreas(n))) return;
      inArea.push(n);
      (have[n._key] ? already : fresh).push(n);
    });
    fresh.sort(function (a, b) { return b._heard - a._heard; });
    var connected = !!st.info;
    var free = connected ? Math.max(0, (st.info.maxContacts || 0) - st.deviceContacts.length) : null;
    var toAdd = connected ? fresh.slice(0, free) : [];
    var toUpdate = connected && $('ccUpdateExisting').checked ? already : [];
    return { inArea: inArea, fresh: fresh, already: already, free: free, toAdd: toAdd, toUpdate: toUpdate };
  }

  function renderAdd() {
    var sel = addSelection();
    st.lastAdd = sel;
    $('ccCountArea').textContent = sel.inArea.length;
    $('ccCountNew').textContent = sel.fresh.length;
    $('ccCountHave').textContent = sel.already.length;
    var note = $('ccFitNote');
    if (!st.shape && !$('ccArea').value) note.textContent = 'Draw an area on the map, or pick one from the list, to choose nodes.';
    else if (!st.info) note.textContent = 'Connect your companion to see which are already on it and how many fit.';
    else if (sel.fresh.length > sel.free) note.textContent = 'Only ' + sel.free + ' fit: the device has ' + plural(sel.free, 'free slot', 'free slots') + '. The most recently heard are added first.';
    else note.textContent = sel.fresh.length ? plural(sel.fresh.length, 'new contact fits', 'new contacts fit') + ': ' + (sel.free - sel.fresh.length) + ' slots free after this.' : 'Nothing new in this area.';

    var rows = sel.toAdd.slice(0, 50).map(function (n) {
      return '<li><span>' + escapeHtml(n.name || n._key.slice(0, 8)) + '</span><span class="cc-muted">' + n._role + areaSuffix(nodeAreas(n)) + ' · ' + ago(n._heard) + '</span></li>';
    }).concat(sel.already.slice(0, 10).map(function (n) {
      return '<li class="cc-dim"><span>' + escapeHtml(n.name || n._key.slice(0, 8)) + ' · on device</span><span>' + (sel.toUpdate.length ? 'update' : 'skipped') + '</span></li>';
    }));
    var more = sel.toAdd.length - 50;
    if (more > 0) rows.push('<li class="cc-dim"><span>and ' + more + ' more</span><span></span></li>');
    $('ccAddList').innerHTML = rows.join('');

    var btn = $('ccAddBtn');
    var count = sel.toAdd.length + sel.toUpdate.length;
    if (!st.info) { btn.disabled = true; btn.textContent = 'Connect a companion to add contacts'; }
    else if (!count) { btn.disabled = true; btn.textContent = sel.free === 0 && sel.fresh.length ? 'The device is full' : 'Nothing to add'; }
    else {
      btn.disabled = false;
      btn.textContent = (sel.toAdd.length ? 'Add ' + plural(sel.toAdd.length, 'contact', 'contacts') : 'Update ' + plural(sel.toUpdate.length, 'contact', 'contacts')) +
        (sel.toAdd.length && sel.toUpdate.length ? ' (+' + sel.toUpdate.length + ' updated)' : '') + ' to ' + (st.info.name || 'the companion');
    }
  }

  function startAdd(items, title) {
    var existing = st.deviceContacts;
    runOp(title, items, function (onProgress, shouldStop) {
      return st.client.addContacts(items, { existing: existing, onProgress: onProgress, shouldStop: shouldStop });
    }, function (results) {
      var added = results.filter(function (r) { return r.status === 'added'; }).length;
      var updated = results.filter(function (r) { return r.status === 'updated'; }).length;
      var full = results.filter(function (r) { return r.status === 'full'; }).length;
      var parts = [plural(added, 'contact', 'contacts') + ' added'];
      if (updated) parts.push(updated + ' updated');
      if (full) parts.push(full + ' not added: the device is full');
      return parts.join(' · ');
    });
  }

  function onAddClick() {
    var sel = st.lastAdd;
    var items = sel.toAdd.concat(sel.toUpdate).map(function (n) { return CL.contactFromNode(n); }).filter(Boolean);
    if (!items.length) return;
    startAdd(items, 'Adding ' + plural(items.length, 'contact', 'contacts') + ' to ' + (st.info.name || 'the companion'));
  }

  // ---- areas ----------------------------------------------------------------
  // The server's areas (/api/config/areas), placed by geometry where the
  // server gives some: the areas file's GeoJSON, else config.json's polygon or
  // box. An area with neither falls back to the nodes the server puts in it.

  function getJSON(url) {
    return fetch(url).then(function (r) { return r.ok ? r.json() : null; }).catch(function () { return null; });
  }

  // ringsHit is even-odd over every ring ([lon, lat] points), so holes count.
  function ringsHit(rings, lat, lon) {
    var c = false;
    rings.forEach(function (pts) {
      for (var i = 0, j = pts.length - 1; i < pts.length; j = i++) {
        var yi = pts[i][1], xi = pts[i][0], yj = pts[j][1], xj = pts[j][0];
        if (((yi > lat) !== (yj > lat)) && (lon < (xj - xi) * (lat - yi) / (yj - yi) + xi)) c = !c;
      }
    });
    return c;
  }

  function geoArea(g) {
    var polys = null;
    if (g && g.type === 'Polygon') polys = [g.coordinates];
    else if (g && g.type === 'MultiPolygon') polys = g.coordinates;
    if (!polys) return null;
    return {
      test: function (lat, lon) { return polys.some(function (rings) { return ringsHit(rings, lat, lon); }); },
      outline: function () { return L.geoJSON(g); },
    };
  }

  function configArea(a) {
    if (a && a.polygon && a.polygon.length >= 3) {
      var ring = a.polygon.map(function (p) { return [p[1], p[0]]; });
      return { test: function (lat, lon) { return ringsHit([ring], lat, lon); }, outline: function () { return L.polygon(a.polygon); } };
    }
    if (a && a.latMin != null && a.latMax != null && a.lonMin != null && a.lonMax != null) {
      return {
        test: function (lat, lon) { return lat >= a.latMin && lat <= a.latMax && lon >= a.lonMin && lon <= a.lonMax; },
        outline: function () { return L.rectangle([[a.latMin, a.lonMin], [a.latMax, a.lonMax]]); },
      };
    }
    return null;
  }

  function loadAreas() {
    var my = st;
    return Promise.all([getJSON('/api/config/areas'), getJSON('/api/config/areas/polygons'), getJSON('/api/config/areas/geojson')]).then(function (r) {
      if (my !== st || !Array.isArray(r[0]) || !r[0].length) return null;
      var polys = {}, geo = {};
      (Array.isArray(r[1]) ? r[1] : []).forEach(function (a) { polys[a.key] = a; });
      ((r[2] && r[2].features) || []).forEach(function (f) {
        var k = f.properties && (f.properties.code || f.properties.key);
        if (k) geo[k] = f.geometry;
      });
      st.areas = r[0].map(function (a) {
        var placed = geoArea(geo[a.key]) || configArea(polys[a.key]) || {};
        return { key: a.key, label: a.label || a.key, test: placed.test || null, outline: placed.outline || null, keys: null };
      });
      return Promise.all(st.areas.filter(function (a) { return !a.test; }).map(function (a) {
        return getJSON('/api/nodes?area=' + encodeURIComponent(a.key) + '&limit=2000').then(function (res) {
          a.keys = {};
          ((res && res.nodes) || []).forEach(function (n) { a.keys[String(n.public_key || '').toLowerCase()] = true; });
        });
      }));
    }).then(function () {
      if (my !== st) return;
      st.areaMemo = {};
      fillAreaSelects();
      refresh();
    });
  }

  function fillAreaSelects() {
    var has = st.areas.length > 0;
    var opts = st.areas.map(function (a) { return '<option value="' + escapeAttr(a.key) + '">' + escapeHtml(a.label) + '</option>'; }).join('');
    $('ccArea').innerHTML = '<option value="">Any area</option>' + opts;
    $('ccMArea').innerHTML = '<option value="">Any area</option>' + opts + '<option value="none">In no area</option>';
    $('ccAreaField').hidden = !has;
    $('ccMAreaField').hidden = !has;
  }

  // areasOf names the areas a key at a position stands in (the position may
  // be null), remembered per key until the areas reload.
  function areasOf(key, place) {
    if (st.areaMemo[key]) return st.areaMemo[key];
    var out = st.areas.filter(function (a) {
      if (a.test) return !!place && a.test(place.lat, place.lon);
      return !!(a.keys && a.keys[key]);
    }).map(function (a) { return a.key; });
    st.areaMemo[key] = out;
    return out;
  }

  function areaPicked(pick, list) {
    if (!pick) return true;
    if (pick === 'none') return list.length === 0;
    return list.indexOf(pick) >= 0;
  }

  function areaSuffix(list) { return list.length ? ' · ' + list.join(', ') : ''; }
  function nodeAreas(n) { return areasOf(n._key, n._placed ? { lat: n.lat, lon: n.lon } : null); }
  function contactAreas(c) { return areasOf(c.key, placeOf(c)); }

  // showArea outlines the picked area and brings it into view.
  function showArea(key) {
    st.areaLayer.clearLayers();
    var a = st.areas.filter(function (x) { return x.key === key; })[0];
    if (!a || !a.outline) return;
    var layer = a.outline();
    layer.setStyle({ color: cssColour('--accent', '#3b82f6'), weight: 2, fill: false, dashArray: '6 4' });
    layer.addTo(st.areaLayer);
    try { st.map.fitBounds(layer.getBounds(), { padding: [20, 20], animate: false }); } catch (e) { /* empty geometry */ }
  }

  function onAreaPick() {
    st.excluded = {};
    showArea(this.value);
    refresh();
  }

  // ---- manage ---------------------------------------------------------------

  // placeOf positions a device contact: its own advertised position, else the
  // node CoreScope has for the same key, else none.
  function placeOf(c) {
    if (c.hasPosition) return { lat: c.lat, lon: c.lon, from: 'contact' };
    var n = st.nodeByKey[c.key];
    if (n && n._placed) return { lat: n.lat, lon: n.lon, from: 'corescope' };
    return null;
  }

  // lastHeardOf is the later of the contact's newest advert on the device and
  // CoreScope last hearing the same key.
  function lastHeardOf(c) {
    var n = st.nodeByKey[c.key];
    return Math.max(c.lastAdvert || 0, n ? n._heard : 0);
  }

  // manageSelection needs something to go on (a drawn shape, an area or a
  // stale age): with nothing chosen, nothing is selected.
  function manageSelection() {
    var type = $('ccMType').value;
    var area = $('ccMArea').value;
    var staleDays = +$('ccMStale').value || 0;
    var noPos = $('ccMNoPos').checked;
    if (!st.shape && !area && !staleDays) return [];
    var now = Date.now() / 1000;
    var out = [];
    st.deviceContacts.forEach(function (c) {
      if (type && CL.typeToRole(c.type) !== type) return;
      if (staleDays && now - lastHeardOf(c) < staleDays * 86400) return;
      var p = placeOf(c);
      if (st.shape && !(p ? inside(st.shape, p.lat, p.lon) : noPos)) return;
      if (!areaPicked(area, contactAreas(c))) return;
      out.push(c);
    });
    out.sort(function (a, b) { return lastHeardOf(a) - lastHeardOf(b); });
    return out;
  }

  function findStale() {
    if (!(+$('ccMStale').value)) $('ccMStale').value = '30';
    $('ccMArea').value = '';
    showArea('');
    setTool(null);
    setShape(null);
  }

  function renderManage() {
    var sel = st.info ? manageSelection() : [];
    var chosen = sel.filter(function (c) { return !st.excluded[c.key]; });
    st.lastRemove = chosen;
    $('ccRemoveTitle').textContent = chosen.length ? plural(chosen.length, 'contact', 'contacts') + ' selected to remove' : 'Nothing selected';
    var placedOwn = 0, placedCS = 0, none = 0;
    st.deviceContacts.forEach(function (c) { var p = placeOf(c); if (!p) none++; else if (p.from === 'contact') placedOwn++; else placedCS++; });
    $('ccManageNote').textContent = !st.info ? 'Connect a companion, then draw round the contacts you want to remove, pick an area, or find stale ones.'
      : st.deviceContacts.length + ' on the device. Positions: ' + placedOwn + ' from the contact, ' + placedCS + ' from CoreScope' + (none ? ', ' + none + ' with none (tick "Include contacts with no position" to pick those)' : '') + '.';
    $('ccRemoveList').innerHTML = sel.slice(0, 200).map(function (c) {
      return '<li><label class="cc-row-check"><input type="checkbox" data-key="' + c.key + '"' + (st.excluded[c.key] ? '' : ' checked') + ' aria-label="Remove ' + escapeAttr(c.name || c.key.slice(0, 8)) + '"><span>' + escapeHtml(c.name || c.key.slice(0, 8)) + '</span></label><span class="cc-muted">' + CL.typeToRole(c.type) + areaSuffix(contactAreas(c)) + ' · last heard ' + ago(lastHeardOf(c)) + '</span></li>';
    }).join('');
    var btn = $('ccRemoveBtn');
    btn.disabled = !chosen.length;
    btn.textContent = chosen.length ? 'Remove ' + plural(chosen.length, 'contact', 'contacts') + '…' : 'Remove contacts…';
  }

  function escapeAttr(s) { return escapeHtml(s).replace(/"/g, '&quot;'); }

  function openConfirm() {
    var chosen = st.lastRemove || [];
    if (!chosen.length) return;
    $('ccConfirmTitle').textContent = 'Remove ' + plural(chosen.length, 'contact', 'contacts') + ' from ' + (st.info.name || 'the companion') + '?';
    $('ccConfirmList').innerHTML = chosen.slice(0, 20).map(function (c) {
      return '<li><span>' + escapeHtml(c.name || c.key.slice(0, 8)) + '</span><span class="cc-muted">' + CL.typeToRole(c.type) + '</span></li>';
    }).join('') + (chosen.length > 20 ? '<li class="cc-dim"><span>and ' + (chosen.length - 20) + ' more</span><span></span></li>' : '');
    $('ccConfirmGo').textContent = 'Remove ' + chosen.length;
    var d = $('ccConfirm');
    if (typeof d.showModal === 'function') d.showModal(); else d.setAttribute('open', '');
    $('ccConfirmCancel').focus();
  }

  function closeConfirm() {
    var d = $('ccConfirm');
    if (typeof d.close === 'function' && d.open) d.close(); else d.removeAttribute('open');
  }

  function doRemove() {
    var chosen = (st.lastRemove || []).slice();
    closeConfirm();
    var keys = chosen.map(function (c) { return c.key; });
    var names = {};
    chosen.forEach(function (c) { names[c.key] = c.name; });
    runOp('Removing ' + plural(keys.length, 'contact', 'contacts') + ' from ' + (st.info.name || 'the companion'), chosen, function (onProgress, shouldStop) {
      return st.client.removeContacts(keys, { onProgress: function (r, done, total) { r.name = names[r.key]; onProgress(r, done, total); }, shouldStop: shouldStop });
    }, function (results) {
      var removed = results.filter(function (r) { return r.status === 'removed'; }).length;
      return plural(removed, 'contact', 'contacts') + ' removed';
    });
  }

  // ---- progress -------------------------------------------------------------

  var STATUS_TEXT = { added: 'added', updated: 'updated', removed: 'removed', full: 'device is full — not added', missing: 'not on the device', error: 'the device refused it' };

  function runOp(title, items, work, summary) {
    var panel = $('ccProgress'), list = $('ccProgressList');
    var my = st;
    st.stop = false;
    panel.hidden = false;
    panel.classList.add('is-running');
    $('ccProgressTitle').textContent = title;
    $('ccProgressNote').textContent = 'Keep this page open and the companion connected. Each contact is sent and confirmed one at a time' + (st.kind === 'bluetooth' ? ' (Bluetooth is slower than USB).' : '.');
    $('ccProgressBar').setAttribute('aria-valuemax', String(items.length));
    $('ccProgressFill').style.width = '0';
    $('ccStopBtn').hidden = false;
    $('ccDoneBtn').hidden = true;
    list.innerHTML = '';
    work(function (r, done, total) {
      if (my !== st) return;
      var li = document.createElement('li');
      li.className = 'cc-' + r.status;
      li.innerHTML = '<span>' + escapeHtml(r.name || r.key.slice(0, 8)) + '</span><span class="cc-muted">' + (STATUS_TEXT[r.status] || r.status) + '</span>';
      list.insertBefore(li, list.firstChild);
      $('ccProgressBar').setAttribute('aria-valuenow', String(done));
      $('ccProgressFill').style.width = Math.round(done / total * 100) + '%';
    }, function () { return st.stop; }).then(function (results) {
      if (my !== st) return null;
      $('ccProgressTitle').textContent = summary(results);
      return reloadDevice();
    }).catch(function (e) {
      if (my !== st) return;
      $('ccProgressTitle').textContent = 'Stopped: ' + (e && e.message || e);
    }).then(function () {
      if (my !== st) return;
      panel.classList.remove('is-running');
      $('ccStopBtn').hidden = true;
      $('ccDoneBtn').hidden = false;
      // The device writes its contact list a few seconds after the last change.
      $('ccProgressNote').textContent = 'Saving on the device: wait a few seconds before unplugging…';
      st.saveTimer = setTimeout(function () { if (my === st) $('ccProgressNote').textContent = 'Saved on the device.'; }, CL.SAVE_DELAY_MS + 500);
      $('ccDoneBtn').focus();
    });
  }

  // ---- markers and refresh --------------------------------------------------

  function drawMarkers() {
    st.markers.clearLayers();
    var n = 0;
    if (st.tab === 'add') {
      var have = deviceKeySet();
      st.nodes.forEach(function (node) {
        if (!node._placed || !node._role) return;
        var on = !!have[node._key];
        L.circleMarker([node.lat, node.lon], {
          radius: 5, weight: on ? 0 : 1.5, color: '#fff', fillColor: roleColour(node._role), fillOpacity: on ? 0.4 : 0.95,
        }).bindTooltip(escapeHtml(node.name || node._key.slice(0, 8)) + (on ? ' (on your companion)' : '')).addTo(st.markers);
        n++;
      });
    } else {
      var chosen = {};
      (st.lastRemove || []).forEach(function (c) { chosen[c.key] = true; });
      st.deviceContacts.forEach(function (c) {
        var p = placeOf(c);
        if (!p) return;
        var role = CL.typeToRole(c.type);
        L.circleMarker([p.lat, p.lon], {
          radius: 5, weight: chosen[c.key] ? 3 : 1.5, color: chosen[c.key] ? cssColour('--palette-red-400', '#f87171') : '#fff',
          fillColor: roleColour(role), fillOpacity: 0.95,
        }).bindTooltip(escapeHtml(c.name || c.key.slice(0, 8))).addTo(st.markers);
        n++;
      });
    }
    st.markerCount = n;
  }

  function renderLegend() {
    $('ccLegend').innerHTML = ROLES.map(function (r) {
      return '<div><span style="background:' + roleColour(r) + '"></span>' + ROLE_LABEL[r].replace(/s$/, '') + '</div>';
    }).join('') + (st.tab === 'add' ? '<div class="cc-dim"><span style="background:' + roleColour('repeater') + ';opacity:.4"></span>Already on your companion</div>' : '');
  }

  function refresh() {
    if (!st) return;
    setStatus();
    var add = st.tab === 'add';
    $('ccTabAdd').setAttribute('aria-selected', String(add));
    $('ccTabManage').setAttribute('aria-selected', String(!add));
    $('ccAddPane').hidden = !add;
    $('ccManagePane').hidden = add;
    if (add) renderAdd(); else renderManage();
    drawMarkers();
    renderLegend();
  }

  function setTab(tab) {
    if (st.tab === tab) return;
    st.tab = tab;
    st.shape = null;
    st.excluded = {};
    drawShape(null);
    var base = location.hash.split('?')[0];
    history.replaceState(null, '', base + (tab === 'manage' ? '?tab=manage' : ''));
    refresh();
  }

  function loadNodes() {
    var my = st;
    return fetchAllNodes('', { pageSize: 2000 }).then(function (res) {
      if (my !== st) return;
      st.nodes = (res.nodes || []).map(function (n) {
        n._key = String(n.public_key || '').toLowerCase();
        n._role = roleOf(n.role);
        n._heard = heardAt(n);
        n._placed = typeof n.lat === 'number' && typeof n.lon === 'number' && (n.lat !== 0 || n.lon !== 0);
        return n;
      }).filter(function (n) { return /^[0-9a-f]{64}$/.test(n._key); });
      st.nodeByKey = {};
      st.nodes.forEach(function (n) { st.nodeByKey[n._key] = n; });
      var pts = st.nodes.filter(function (n) { return n._placed; }).map(function (n) { return [n.lat, n.lon]; });
      if (pts.length) st.map.fitBounds(pts, { padding: [30, 30], maxZoom: 10, animate: false });
      refresh();
    });
  }

  // ---- page -----------------------------------------------------------------

  function init(container) {
    st = { tab: tabFromHash(), nodes: [], nodeByKey: {}, deviceContacts: [], excluded: {}, client: null, info: null, shape: null, tool: null, polyPts: [], areas: [], areaMemo: {} };
    render(container);
    var sup = CL.supported();
    $('ccConnectUsb').disabled = !sup.serial;
    $('ccConnectBle').disabled = !sup.bluetooth;
    if (!sup.serial) $('ccConnectUsb').title = 'This browser has no Web Serial';
    if (!sup.bluetooth) $('ccConnectBle').title = 'This browser has no Web Bluetooth';
    $('ccUnsupported').hidden = sup.serial || sup.bluetooth;

    $('ccConnectUsb').addEventListener('click', function () { connect('usb'); });
    $('ccConnectBle').addEventListener('click', function () { connect('bluetooth'); });
    $('ccDisconnect').addEventListener('click', disconnect);
    $('ccTabAdd').addEventListener('click', function () { setTab('add'); });
    $('ccTabManage').addEventListener('click', function () { setTab('manage'); });
    $('ccToolCircle').addEventListener('click', function () { setTool('circle'); });
    $('ccToolRect').addEventListener('click', function () { setTool('rect'); });
    $('ccToolPoly').addEventListener('click', function () { setTool('poly'); });
    $('ccToolClear').addEventListener('click', function () { setTool(null); setShape(null); });
    $('ccRoles').addEventListener('change', refresh);
    $('ccHeard').addEventListener('change', refresh);
    $('ccUpdateExisting').addEventListener('change', refresh);
    $('ccAddBtn').addEventListener('click', onAddClick);
    ['ccMType', 'ccMStale', 'ccMNoPos'].forEach(function (id) { $(id).addEventListener('change', function () { st.excluded = {}; refresh(); }); });
    $('ccArea').addEventListener('change', onAreaPick);
    $('ccMArea').addEventListener('change', onAreaPick);
    $('ccFindStale').addEventListener('click', findStale);
    $('ccRemoveList').addEventListener('change', function (e) {
      var k = e.target && e.target.getAttribute('data-key');
      if (!k) return;
      if (e.target.checked) delete st.excluded[k]; else st.excluded[k] = true;
      refresh();
    });
    $('ccRemoveBtn').addEventListener('click', openConfirm);
    $('ccConfirmCancel').addEventListener('click', closeConfirm);
    $('ccConfirmGo').addEventListener('click', doRemove);
    $('ccStopBtn').addEventListener('click', function () { st.stop = true; this.textContent = 'Stopping…'; });
    $('ccDoneBtn').addEventListener('click', function () { $('ccProgress').hidden = true; $('ccStopBtn').textContent = 'Stop after this one'; });

    initMap();
    refresh();
    loadNodes().then(loadAreas);

    // Test hooks (no behaviour of their own).
    window._companionContacts = {
      state: function () {
        var a = st.lastAdd || {};
        return {
          tab: st.tab, shape: st.shape ? st.shape.kind : null, nodeMarkers: st.tab === 'add' ? st.markerCount : 0,
          inArea: (a.inArea || []).length, fresh: (a.fresh || []).length, already: (a.already || []).length,
          free: a.free, deviceContacts: st.deviceContacts.length, toRemove: (st.lastRemove || []).length,
        };
      },
      selectCircle: function (lat, lon, radiusM) { setShape({ kind: 'circle', center: L.latLng(lat, lon), radiusM: radiusM }); },
      centreOn: function (lat, lon, zoom) { st.map.setView([lat, lon], zoom, { animate: false }); },
    };
  }

  function destroy() {
    if (!st) return;
    if (st.saveTimer) clearTimeout(st.saveTimer);
    if (st.client) st.client.close();
    if (st.map) st.map.remove();
    st = null;
    delete window._companionContacts;
  }

  registerPage('companion-contacts', { init: init, destroy: destroy });
})();
