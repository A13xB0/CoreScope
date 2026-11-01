/* === CoreScope — map-power.js ===
 * The map's Battery & temperature layer. Repeaters and rooms that observers
 * have polled show a tag with their charge (percentage and voltage), their
 * enclosure temperature, or both, and how long ago each was read. Filters keep only the ones with a
 * battery reading or with a temperature reading, and a "Captured within"
 * limit treats older readings as missing.
 *
 * Both values come from the /api/nodes rows the map already loads
 * (node_battery_list.go): battery {mv, ts, status, stale} and temperature
 * {c, ts}, the newest reading of each in the last week.
 *
 * Charge is estimated from the voltage with a typical single-cell Li-ion
 * curve, so it is always shown with "≈" and the voltage beside it.
 */
'use strict';

(function () {
  var KEY = 'meshcore-map-power';
  var SHOW = ['off', 'battery', 'temperature', 'both'];
  var WITHIN = [0, 3, 6, 24, 168];

  var state = readState();

  function readState() {
    var s = { show: 'off', onlyBattery: false, onlyTemperature: false, within: 0 };
    try {
      var saved = JSON.parse(localStorage.getItem(KEY) || '{}');
      if (SHOW.indexOf(saved.show) >= 0) s.show = saved.show;
      s.onlyBattery = !!saved.onlyBattery;
      s.onlyTemperature = !!saved.onlyTemperature;
      if (WITHIN.indexOf(saved.within) >= 0) s.within = saved.within;
    } catch (_) { /* defaults */ }
    return s;
  }
  function saveState() {
    try { localStorage.setItem(KEY, JSON.stringify(state)); } catch (_) { /* per-viewer only */ }
  }

  var esc = function (s) {
    return (typeof escapeHtml === 'function') ? escapeHtml(s) : String(s).replace(/[&<>"]/g, function (c) {
      return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' }[c];
    });
  };

  // ── Values ──────────────────────────────────────────────────────────────
  var CURVE = [[4200, 100], [4150, 95], [4100, 90], [4050, 85], [4000, 78], [3950, 72], [3900, 65],
    [3850, 58], [3800, 52], [3750, 45], [3700, 38], [3650, 30], [3600, 22], [3500, 10], [3400, 5],
    [3300, 3], [3000, 0]];

  // percent estimates Li-ion charge from millivolts; null when there is none.
  function percent(mv) {
    if (mv == null || !(mv > 0)) return null;
    if (mv >= CURVE[0][0]) return 100;
    if (mv <= CURVE[CURVE.length - 1][0]) return 0;
    for (var i = 0; i < CURVE.length - 1; i++) {
      var a = CURVE[i], b = CURVE[i + 1];
      if (mv <= a[0] && mv >= b[0]) return Math.round(b[1] + (mv - b[0]) / (a[0] - b[0]) * (a[1] - b[1]));
    }
    return null;
  }

  function ageHours(ts, now) {
    var t = Date.parse(ts);
    return isFinite(t) ? ((now || Date.now()) - t) / 3600000 : Infinity;
  }

  // shortAge: "12m", "3h", "2d".
  function shortAge(ts, now) {
    var h = ageHours(ts, now);
    if (!isFinite(h)) return '';
    var m = Math.max(0, Math.round(h * 60));
    if (m < 60) return m + 'm';
    if (m < 48 * 60) return Math.floor(m / 60) + 'h';
    return Math.round(m / 1440) + 'd';
  }

  function withinLimit(ts, now) {
    return !state.within || ageHours(ts, now) <= state.within;
  }

  // battery returns the node's reading when it counts: there is one, a
  // battery is fitted, and it is inside the Captured within limit.
  function battery(n, now) {
    var b = n && n.battery;
    if (!b || !(b.mv > 0) || b.status === 'no_battery') return null;
    return withinLimit(b.ts, now) ? b : null;
  }

  function temperature(n, now) {
    var t = n && n.temperature;
    if (!t || !isFinite(t.c)) return null;
    return withinLimit(t.ts, now) ? t : null;
  }

  // ── Glyphs (inline SVG, coloured by currentColor) ───────────────────────
  function batteryGlyph(pct) {
    var h = Math.round((pct || 0) / 100 * 9);
    return '<svg class="mp-batt" viewBox="0 0 8 14" aria-hidden="true"><rect x="2.5" y="0.5" width="3" height="1.5" rx=".5"/>' +
      '<rect class="mp-batt-case" x="0.75" y="2" width="6.5" height="11.25" rx="1.2"/>' +
      '<rect class="mp-batt-fill" x="2" y="' + (12 - h) + '" width="4" height="' + h + '"/></svg>';
  }
  var THERMO = '<svg class="mp-thermo" viewBox="0 0 8 15" aria-hidden="true">' +
    '<path class="mp-thermo-case" d="M2.5 9V2.5a1.5 1.5 0 0 1 3 0V9a2.75 2.75 0 1 1-3 0z"/>' +
    '<circle cx="4" cy="11.25" r="1.4"/><path d="M4 10V5" stroke-width="1.2"/></svg>';

  function level(pct) { return pct == null ? 'none' : pct >= 60 ? 'ok' : pct >= 30 ? 'low' : 'critical'; }

  // ── Layer API used by map.js ────────────────────────────────────────────
  function showsBattery() { return state.show === 'battery' || state.show === 'both'; }
  function showsTemperature() { return state.show === 'temperature' || state.show === 'both'; }
  function active() { return state.show !== 'off'; }

  // narrows: an "Only" filter is on, so the map shows a subset of repeaters
  // (map.js then stands the plain observer layer down, as for other filters).
  function narrows() { return active() && (state.onlyBattery || state.onlyTemperature); }

  // accepts is the layer's part of nodePassesMapFilters.
  function accepts(n, now) {
    if (!narrows()) return true;
    if (state.onlyBattery && !battery(n, now)) return false;
    if (state.onlyTemperature && !temperature(n, now)) return false;
    return true;
  }

  // tag returns the marker for a node, or null to keep its normal marker.
  // w/h are its size for map.js's label deconfliction.
  function tag(n, isStale, now) {
    if (!active() || !n || (n.role !== 'repeater' && n.role !== 'room')) return null;
    // Any reading earns a tag, so a repeater does not fall back to its plain
    // marker just because the value being shown is the one it lacks ("—").
    if (!battery(n, now) && !temperature(n, now)) return null;
    var b = showsBattery() ? battery(n, now) : null;
    var t = showsTemperature() ? temperature(n, now) : null;
    var parts = [], aria = [n.name || 'Repeater'], chars = 0;
    if (showsBattery()) {
      var pct = b ? percent(b.mv) : null;
      parts.push('<span class="mp-part mp-level-' + level(pct) + '">' + batteryGlyph(pct) +
        '<b>' + (pct == null ? '—' : pct + '%') + '</b>' +
        (b ? '<span class="mp-volts">' + (b.mv / 1000).toFixed(2) + 'V</span>' : '') + '</span>');
      aria.push(pct == null ? 'no battery reading' : 'battery about ' + pct + ' percent, ' + (b.mv / 1000).toFixed(2) + ' volts');
      chars += b ? 11 : 5;
    }
    if (showsTemperature()) {
      var c = t ? Math.round(t.c) : null;
      parts.push('<span class="mp-part mp-temp">' + THERMO + '<b>' + (c == null ? '—' : c + '°') + '</b></span>');
      aria.push(c == null ? 'no temperature reading' : 'enclosure ' + c + ' degrees');
      chars += 5;
    }
    // The age is of the older value shown, so the tag never looks fresher
    // than what is on it.
    var shown = [b && b.ts, t && t.ts].filter(Boolean).sort();
    var age = shown.length ? shortAge(shown[0], now) : '';
    if (age) { parts.push('<span class="mp-age">' + age + '</span>'); aria.push('read ' + age + ' ago'); chars += age.length + 1; }
    var old = (b && b.stale) || isStale;
    return {
      html: '<div class="mp-tag' + (old ? ' mp-old' : '') + '" role="img" aria-label="' + esc(aria.join(', ')) + '">' +
        '<i class="mp-dot mp-level-' + (b ? level(percent(b.mv)) : 'none') + '"></i>' + parts.join('') + '</div>',
      w: 26 + chars * 8 + (showsTemperature() ? 10 : 0) + (showsBattery() ? 10 : 0),
      h: 24,
    };
  }

  function icon(n, isStale, Leaflet, now) {
    var t = tag(n, isStale, now);
    if (!t) return null;
    return { icon: Leaflet.divIcon({ html: t.html, className: 'meshcore-marker mp-marker', iconSize: null, iconAnchor: [0, 0], popupAnchor: [0, -14] }), w: t.w, h: t.h };
  }

  function row(label, value) {
    return '<dt style="color:var(--text-muted);float:left;clear:left;width:80px;padding:2px 0;">' + label + '</dt>' +
      '<dd style="margin-left:88px;padding:2px 0;">' + value + '</dd>';
  }

  // popupRows adds Battery, Enclosure and Monitoring to a node's map popup.
  function popupRows(n, now) {
    if (!n || (n.role !== 'repeater' && n.role !== 'room')) return '';
    var out = '';
    var b = n.battery;
    if (b && b.status === 'no_battery') {
      out += row('Battery', '<span style="color:var(--text-muted)">No battery fitted</span>');
    } else if (b && b.mv > 0) {
      out += row('Battery', '≈' + percent(b.mv) + '% <span style="color:var(--text-muted)">(' + (b.mv / 1000).toFixed(2) + ' V) · ' +
        esc(shortAge(b.ts, now)) + ' ago</span>');
    }
    var t = n.temperature;
    if (t && isFinite(t.c)) {
      out += row('Enclosure', Number(t.c).toFixed(1) + ' °C <span style="color:var(--text-muted)">· ' + esc(shortAge(t.ts, now)) + ' ago</span>');
    }
    // Community polling: how many observers monitor it, and who polls it
    // (node-monitors.js, from the row's monitored object).
    if (window.NodeMonitors) out += window.NodeMonitors.popupRows(n, row);
    return out;
  }

  // ── Controls ────────────────────────────────────────────────────────────
  function sectionHtml() {
    var opt = function (v, label) { return '<option value="' + v + '"' + (state.show === v ? ' selected' : '') + '>' + label + '</option>'; };
    var within = function (v, label) { return '<option value="' + v + '"' + (state.within === v ? ' selected' : '') + '>' + label + '</option>'; };
    return '<fieldset class="mc-section">' +
      '<legend class="mc-label">Battery &amp; Temperature</legend>' +
      '<select id="mpShow" aria-label="Show battery or temperature on repeaters">' +
        opt('off', 'Off') + opt('battery', 'Battery') + opt('temperature', 'Temperature') + opt('both', 'Battery and temperature') +
      '</select>' +
      '<div id="mpOpts" class="mp-opts">' +
        '<label for="mpOnlyBattery"><input type="checkbox" id="mpOnlyBattery"' + (state.onlyBattery ? ' checked' : '') + '> Only with a battery reading</label>' +
        '<label for="mpOnlyTemperature"><input type="checkbox" id="mpOnlyTemperature"' + (state.onlyTemperature ? ' checked' : '') + '> Only with a temperature reading</label>' +
        '<label for="mpWithin" class="mp-within-label">Captured within</label>' +
        '<select id="mpWithin">' + within(0, 'Any time (last 7 days)') + within(3, '3 hours') + within(6, '6 hours') +
          within(24, '24 hours') + within(168, '7 days') + '</select>' +
        '<div class="mp-legend" aria-hidden="true"><span><i class="mp-dot mp-level-ok"></i>60%+</span><span><i class="mp-dot mp-level-low"></i>30–59%</span><span><i class="mp-dot mp-level-critical"></i>&lt;30%</span></div>' +
        '<div class="mp-note">Charge is estimated from the voltage. Temperature is the repeater\'s own enclosure sensor.</div>' +
      '</div>' +
    '</fieldset>';
  }

  function syncControls(root) {
    var opts = root.querySelector('#mpOpts');
    if (opts) opts.hidden = !active();
  }

  // bind wires the controls; onChange re-renders the markers.
  function bind(root, onChange) {
    var show = root.querySelector('#mpShow');
    if (!show) return;
    show.addEventListener('change', function (e) {
      state.show = SHOW.indexOf(e.target.value) >= 0 ? e.target.value : 'off';
      saveState(); syncControls(root); onChange();
    });
    [['#mpOnlyBattery', 'onlyBattery'], ['#mpOnlyTemperature', 'onlyTemperature']].forEach(function (p) {
      var el = root.querySelector(p[0]);
      if (el) el.addEventListener('change', function (e) { state[p[1]] = e.target.checked; saveState(); onChange(); });
    });
    var w = root.querySelector('#mpWithin');
    if (w) w.addEventListener('change', function (e) { state.within = Number(e.target.value) || 0; saveState(); onChange(); });
    syncControls(root);
  }

  window.MapPower = {
    sectionHtml: sectionHtml, bind: bind,
    active: active, narrows: narrows, accepts: accepts, icon: icon, tag: tag, popupRows: popupRows,
    percent: percent,
    // tests
    _setState: function (s) { for (var k in s) state[k] = s[k]; },
  };
})();
