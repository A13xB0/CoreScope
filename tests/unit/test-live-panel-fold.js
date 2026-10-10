/**
 * Live feed and legend: fold, close and reopen. Source checks on
 * public/live.js and public/live.css, in the style of
 * test-issue-1532-live-fullscreen.js (runs in the JS unit gate).
 * Behaviour is covered by tests/e2e/test-live-panel-fold-e2e.js.
 *
 * The bug: closing the feed or the legend left a reopen button pinned
 * bottom-right, where the legend (z-index 1000) is drawn over it (500).
 * The fix moves the reopen controls into the MESH LIVE header.
 */
'use strict';
const REPO_ROOT = require('path').resolve(__dirname, '..', '..');
const fs = require('fs');
const path = require('path');

let passed = 0, failed = 0;
function assert(cond, msg) {
  if (cond) { passed++; console.log('  ✓ ' + msg); }
  else { failed++; console.error('  ✗ ' + msg); }
}

const js = fs.readFileSync(path.join(REPO_ROOT, 'public', 'live.js'), 'utf8');
const css = fs.readFileSync(path.join(REPO_ROOT, 'public', 'live.css'), 'utf8').replace(/\/\*[\s\S]*?\*\//g, '');

// The init() template: from `app.innerHTML = \`` to the closing backtick.
const tplStart = js.indexOf('app.innerHTML = `');
const tpl = tplStart >= 0 ? js.slice(tplStart, js.indexOf('`;', tplStart)) : '';
function between(src, startMarker, endMarker) {
  const a = src.indexOf(startMarker);
  if (a < 0) return '';
  const b = src.indexOf(endMarker, a + startMarker.length);
  return src.slice(a, b < 0 ? undefined : b);
}

console.log('\n=== reopen controls live in the MESH LIVE header ===');
const header = between(tpl, 'id="liveHeader"', 'id="liveControls"');
assert(/id="livePanelToggles"/.test(header), '#livePanelToggles is inside #liveHeader');
assert(/id="liveToggleFeed"[^>]*aria-pressed=/.test(header), '#liveToggleFeed is a pressed-state button in the header');
assert(/id="liveToggleLegend"[^>]*aria-pressed=/.test(header), '#liveToggleLegend is a pressed-state button in the header');
assert(!/feedShowBtn|feed-show-btn/.test(js + css), 'the old bottom-right show-feed button is gone (it was drawn under the legend)');
assert(!/legendToggleBtn|legend-toggle-btn/.test(js + css), 'the old bottom-right legend button is gone (it was drawn under the legend)');

console.log('\n=== both panels fold from a title button; the legend can close ===');
const feed = between(tpl, 'id="liveFeed"', 'class="panel-content"');
const legend = between(tpl, 'id="liveLegend"', 'class="panel-content"');
assert(/class="panel-fold"[^>]*aria-expanded=/.test(feed), 'the feed header has a .panel-fold title button with aria-expanded');
assert(/class="panel-fold"[^>]*aria-expanded=/.test(legend), 'the legend header has a .panel-fold title button with aria-expanded');
assert(/id="legendCloseBtn"/.test(legend), 'the legend header has a close button');
assert(/id="legendDescBtn"[^>]*aria-pressed=/.test(tpl), 'the legend has a descriptions button with aria-pressed');
for (const key of ['live-feed-folded', 'live-legend-folded', 'live-legend-descriptions', 'live-feed-hidden', 'live-legend-hidden']) {
  assert(js.indexOf("'" + key + "'") !== -1, 'state is remembered under ' + key);
}

console.log('\n=== CSS ===');
assert(/\.is-folded\s+\.panel-content\s*\{[^}]*display\s*:\s*none/.test(css), 'a folded panel hides its content');
assert(/\.legend-short\s+\.legend-long\s*\{[^}]*display\s*:\s*none/.test(css), 'short mode hides the descriptions');
assert(/\.live-legend\s+\.panel-fold[^{]*\{[^}]*pointer-events\s*:\s*auto/.test(css) || /\.live-legend\s+\.panel-header[^{]*\{[^}]*pointer-events\s*:\s*auto/.test(css),
  'the legend header stays clickable (the legend surface lets clicks through to the map)');
assert(/#legendDescBtn[^{]*\{[^}]*pointer-events\s*:\s*auto/.test(css) || /\.legend-desc-btn[^{]*\{[^}]*pointer-events\s*:\s*auto/.test(css),
  'the descriptions button stays clickable');
// Every @media (max-width: 640px) block, brace-matched.
function mediaBlocks(src, query) {
  const out = [];
  let i = src.indexOf(query);
  while (i >= 0) {
    const open = src.indexOf('{', i);
    let depth = 0, j = open;
    for (; j < src.length; j++) { if (src[j] === '{') depth++; else if (src[j] === '}' && --depth === 0) break; }
    out.push(src.slice(open + 1, j));
    i = src.indexOf(query, j);
  }
  return out;
}
assert(mediaBlocks(css, '@media (max-width: 640px)').some((b) => /\.live-panel-toggles\s*\{[^}]*display\s*:\s*none/.test(b)),
  'phones (<=640px), where both panels stay off, do not show the toggles');
assert(/body\.live-fullscreen[^{]*\.live-panel-toggles/.test(css), 'fullscreen hides the toggles along with the panels');
const tg = (css.match(/\.live-panel-toggles\s*\{([^}]*)\}/) || [])[1] || '';
assert(!/#(?:[0-9a-fA-F]{8}|[0-9a-fA-F]{6}|[0-9a-fA-F]{3})\b/.test(tg), '.live-panel-toggles uses theme variables, no raw hex');

console.log(`\nResults: ${passed} passed, ${failed} failed`);
process.exit(failed > 0 ? 1 : 0);
