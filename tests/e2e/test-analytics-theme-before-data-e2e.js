#!/usr/bin/env node
/**
 * Analytics: a theme-refresh that lands before the data must not throw.
 *
 * Every load fires 'theme-refresh' about 300ms after the theme resolves. On a
 * slow server the analytics answers land later, and re-rendering the Overview
 * from the empty data object threw. Holds /api/analytics/rf back, dispatches
 * the event, then lets the answer through.
 *
 * Usage: BASE_URL=http://localhost:13581 CHROMIUM_PATH=... node test-analytics-theme-before-data-e2e.js
 */
'use strict';
const { chromium } = require('playwright');

const BASE = process.env.BASE_URL || 'http://localhost:13581';
const fail = (msg) => { console.error('  ✗ ' + msg); process.exit(1); };

(async () => {
  const browser = await chromium.launch({ executablePath: process.env.CHROMIUM_PATH || undefined, args: ['--no-sandbox', '--disable-gpu'] });
  const page = await browser.newPage();
  const errors = [];
  page.on('pageerror', (e) => errors.push(e.message));
  let release, asked;
  const held = new Promise((r) => { release = r; });
  const requested = new Promise((r) => { asked = r; });
  await page.route(/\/api\/analytics\/rf(\?|$)/, async (route) => { asked(); await held; await route.continue(); });

  await page.goto(BASE + '/#/analytics');
  await requested;
  await page.evaluate(() => window.dispatchEvent(new CustomEvent('theme-refresh')));
  await page.waitForTimeout(300);
  if (errors.length) fail('theme-refresh before the data threw: ' + errors.join(' | '));
  console.log('  ✓ theme-refresh before the data does not throw');

  release();
  await page.waitForFunction(() => /Packets \//.test(document.getElementById('analyticsContent').textContent), null, { timeout: 15000 })
    .catch(() => fail('the Overview did not render once the data landed'));
  if (errors.length) fail('page error after the data landed: ' + errors.join(' | '));
  console.log('  ✓ the Overview renders once the data lands');
  await browser.close();
})().catch((e) => { console.error(e); process.exit(1); });
