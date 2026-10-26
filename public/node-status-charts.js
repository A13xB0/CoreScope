/* === CoreScope: node-status-charts.js === */
'use strict';
// Repeater status on the node analytics page: the rest of the status reply an
// observer polling the node directly publishes with its battery (MeshCore
// RepeaterStats). Data: GET /api/nodes/{pubkey}/status-history?days=N, one
// series per observer; the one with most samples that is not stale is drawn.
// The card stays hidden when no observer has published any, or when every
// series is stale (newest sample older than neighborBattery.hideAfterHours),
// as for a repeater now polled by telemetry only.
(function () {
  let charts = [];

  function destroy() {
    charts.forEach(c => { try { c.destroy(); } catch (_e) { /* already gone */ } });
    charts = [];
  }

  function css(name, fallback) {
    const v = getComputedStyle(document.documentElement).getPropertyValue(name).trim();
    return v || fallback;
  }

  // points turns samples into chart points for one value; skip leaves out
  // samples where get returns null (missing field, or no rate after a reboot).
  function points(samples, get) {
    const out = [];
    for (const s of samples) {
      const y = get(s);
      if (y == null || Number.isNaN(y)) continue;
      out.push({ x: new Date(s.timestamp).getTime(), y: y });
    }
    return out;
  }

  const val = key => s => (s.values && s.values[key] != null ? Number(s.values[key]) : null);
  const rate = key => s => (s.per_hour && s.per_hour[key] != null ? Number(s.per_hour[key]) : null);

  // The charts, in the order shown. Each line names its value and colour
  // token; a line with axis 'y1' goes on a right-hand axis with its own unit.
  function chartDefs() {
    const accent = css('--accent', '#4a9eff');
    const second = css('--status-orange', '#f97316');
    const red = css('--status-red', '#ef4444');
    return [
      { id: 'uptime', title: 'Uptime', desc: 'Days since the repeater last started. A drop is a reboot, marked in red.',
        unit: 'days', lines: [{ label: 'Uptime', get: s => { const u = val('uptime_secs')(s); return u == null ? null : u / 86400; }, color: accent }],
        reboots: true, fmt: v => v.toFixed(1) + ' d' },
      { id: 'noise', title: 'Noise floor', desc: 'Background RF level the repeater hears. Lower is quieter.',
        unit: 'dBm', lines: [{ label: 'Noise floor', get: val('noise_floor'), color: accent }], fmt: v => v.toFixed(0) + ' dBm' },
      { id: 'signal', title: 'Last heard packet', desc: 'RSSI and SNR of the last packet the repeater received.',
        unit: 'dBm', unit1: 'dB', lines: [
          { label: 'RSSI (dBm)', get: val('last_rssi'), color: accent },
          { label: 'SNR (dB)', get: val('last_snr'), color: second, axis: 'y1' }], fmt: v => v.toFixed(0) },
      { id: 'airtime', title: 'Airtime per hour', desc: 'Seconds per hour spent transmitting and receiving.',
        unit: 's / h', lines: [
          { label: 'RX', get: rate('rx_air_secs'), color: accent },
          { label: 'TX', get: rate('tx_air_secs'), color: second }], fmt: v => v.toFixed(0) + ' s' },
      { id: 'packets', title: 'Packets received per hour', desc: 'Flood and direct packets received.',
        unit: 'flood / h', unit1: 'direct / h', lines: [
          { label: 'Flood', get: rate('recv_flood'), color: accent },
          { label: 'Direct (right axis)', get: rate('recv_direct'), color: second, axis: 'y1' }], fmt: v => v.toFixed(0) },
      { id: 'sent', title: 'Packets sent per hour', desc: 'Flood and direct packets the repeater transmitted.',
        unit: 'flood / h', unit1: 'direct / h', lines: [
          { label: 'Flood', get: rate('sent_flood'), color: accent },
          { label: 'Direct (right axis)', get: rate('sent_direct'), color: second, axis: 'y1' }], fmt: v => v.toFixed(0) },
      { id: 'dups', title: 'Flood duplicate rate', desc: 'Share of flood packets received that the repeater had already heard.',
        unit: '%', lines: [{ label: 'Duplicates', get: s => (s.dup_rate == null ? null : s.dup_rate * 100), color: accent }],
        min: 0, max: 100, fmt: v => v.toFixed(0) + '%' },
      { id: 'errors', title: 'Receive errors per hour', desc: 'Packets the radio picked up but could not decode.',
        unit: 'per h', lines: [{ label: 'Errors', get: rate('recv_errors'), color: red }], fmt: v => v.toFixed(0) },
      { id: 'queue', title: 'TX queue', desc: 'Packets waiting to be sent when the repeater answered.',
        unit: 'packets', lines: [{ label: 'Queued', get: val('tx_queue'), color: accent }], min: 0, fmt: v => v.toFixed(0) },
    ];
  }

  // latest is the newest value of a chart's first line, for its heading.
  function latest(def, samples) {
    for (let i = samples.length - 1; i >= 0; i--) {
      const v = def.lines[0].get(samples[i]);
      if (v != null) return def.fmt(v);
    }
    return '';
  }

  function tickLabel(days) {
    return v => {
      const d = new Date(v);
      return (typeof formatChartAxisLabel === 'function')
        ? formatChartAxisLabel(d, days <= 3)
        : (days <= 3 ? d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
                     : d.toLocaleDateString([], { month: 'short', day: 'numeric' }));
    };
  }

  function drawChart(canvas, def, samples, days) {
    const datasets = def.lines.map(ln => ({
      label: ln.label, data: points(samples, ln.get), borderColor: ln.color, backgroundColor: ln.color,
      yAxisID: ln.axis || 'y', tension: 0.2, pointRadius: 2, fill: false,
    }));
    if (def.reboots) {
      const reboots = samples.filter(s => s.reboot);
      if (reboots.length) {
        datasets.push({ label: 'Reboot', data: points(reboots, def.lines[0].get), showLine: false,
          pointRadius: 5, pointStyle: 'triangle', borderColor: css('--status-red', '#ef4444'), backgroundColor: css('--status-red', '#ef4444') });
      }
    }
    // Pin the time axis to the samples; a lone sample still needs a span.
    const xs = samples.map(s => new Date(s.timestamp).getTime());
    const pad = Math.min.apply(null, xs) === Math.max.apply(null, xs) ? 3600e3 : 0;
    const scales = {
      x: { type: 'linear', min: Math.min.apply(null, xs) - pad, max: Math.max.apply(null, xs) + pad,
        ticks: { callback: tickLabel(days), maxTicksLimit: 6, maxRotation: 0 } },
      y: { title: { display: true, text: def.unit }, min: def.min, max: def.max },
    };
    if (def.unit1) scales.y1 = { position: 'right', title: { display: true, text: def.unit1 }, grid: { drawOnChartArea: false } };
    return new Chart(canvas, {
      type: 'line',
      data: { datasets: datasets },
      options: {
        responsive: true,
        parsing: false,
        plugins: {
          legend: { display: datasets.length > 1, position: 'bottom' },
          tooltip: { callbacks: { title: items => items.length ? new Date(items[0].parsed.x).toLocaleString() : '' } },
        },
        scales: scales,
      },
    });
  }

  // render fills the card from a status-history response. It returns false
  // (and leaves the card hidden) when there is nothing to show.
  function render(card, data, days) {
    const series = ((data && data.series) || []).filter(s => !s.stale);
    const first = series[0];
    if (!card || !first || !first.samples || !first.samples.length) {
      if (card) card.hidden = true;
      return false;
    }
    const samples = first.samples;
    const defs = chartDefs().filter(d => d.lines.some(ln => points(samples, ln.get).length));
    if (!defs.length) return false;
    const by = card.querySelector('.ns-by');
    if (by) {
      const others = series.length - 1;
      // Status decoded under the shared key is credited to the shared key
      // itself when no poller claimed it: not a real observer, so no link.
      const who = first.observer_name === 'shared key'
        ? 'Via the shared key'
        : 'Reported by <a href="#/observers/' + encodeURIComponent(String(first.observer_id).toUpperCase()) + '">'
          + escapeHtml(first.observer_name || String(first.observer_id).slice(0, 8)) + '</a>';
      by.innerHTML = who + ' · ' + samples.length + ' status repl' + (samples.length === 1 ? 'y' : 'ies')
        + (others > 0 ? ' · ' + others + ' other observer' + (others === 1 ? '' : 's') + ' not shown' : '');
    }
    const grid = card.querySelector('.ns-grid');
    grid.innerHTML = defs.map(d => '<div class="ns-chart" data-ns="' + d.id + '">'
      + '<div class="ns-head"><b>' + escapeHtml(d.title) + '</b><span>' + escapeHtml(latest(d, samples)) + '</span></div>'
      + '<div class="analytics-chart-desc">' + escapeHtml(d.desc) + '</div>'
      + '<canvas role="img" aria-label="' + escapeHtml(d.title) + ' chart"></canvas></div>').join('');
    if (typeof Chart === 'function') {
      for (const d of defs) {
        const canvas = grid.querySelector('[data-ns="' + d.id + '"] canvas');
        if (canvas) charts.push(drawChart(canvas, d, samples, days));
      }
    }
    card.hidden = false;
    return true;
  }

  async function load(card, pubkey, days, isCurrent) {
    destroy();
    if (!card) return;
    card.hidden = true;
    let data;
    try {
      data = await api('/nodes/' + encodeURIComponent(pubkey) + '/status-history?days=' + days);
    } catch (_e) {
      return; // no section rather than an error: the stats are optional
    }
    if (isCurrent && !isCurrent()) return;
    render(card, data, days);
  }

  window.NodeStatusCharts = { load: load, render: render, destroy: destroy, chartDefs: chartDefs, points: points };
})();
