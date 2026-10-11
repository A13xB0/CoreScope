/* test-companion-link.js — the MeshCore companion protocol layer behind
 * Tools › Companion Contacts (public/companion-link.js), against a fake
 * companion (tests/helpers/fake-companion.js) that answers as the firmware's
 * examples/companion_radio/MyMesh.cpp does. */
'use strict';
const path = require('path');
const fs = require('fs');
const vm = require('vm');
const assert = require('assert');
const REPO_ROOT = path.resolve(__dirname, '..', '..');
const FakeCompanion = require(path.join(REPO_ROOT, 'tests/helpers/fake-companion.js'));

const ctx = { window: {}, console, TextEncoder, TextDecoder, Uint8Array, DataView, ArrayBuffer, Promise, setTimeout, clearTimeout, Math, JSON, Date };
ctx.window = ctx;
vm.createContext(ctx);
vm.runInContext(fs.readFileSync(path.join(REPO_ROOT, 'public/companion-link.js'), 'utf8'), ctx);
const CL = ctx.CompanionLink;

let passed = 0, failed = 0;
const pending = [];
function test(name, fn) {
  pending.push((async () => {
    try { await fn(); passed++; console.log('  ✓ ' + name); }
    catch (e) { failed++; console.error('  ✗ ' + name + ': ' + e.message); }
  })());
}
const KEY_A = 'aa' + '11'.repeat(31);
const KEY_B = 'bb' + '22'.repeat(31);
const KEY_C = 'cc' + '33'.repeat(31);

// A transport wired straight to a fake device: each sent payload is handled
// and the replies delivered asynchronously, as a real link would.
function fakeTransport(fake) {
  const t = {
    onFrame: null,
    send(payload) { const out = fake.handle(payload); setTimeout(() => out.forEach((f) => t.onFrame && t.onFrame(f)), 0); return Promise.resolve(); },
    close() { return Promise.resolve(); },
  };
  return t;
}

test('USB frames: "<" + little-endian length + payload', () => {
  const f = CL.encodeSerial(Uint8Array.of(9, 1, 2));
  assert.deepStrictEqual(Array.from(f), [0x3c, 3, 0, 9, 1, 2]);
});

test('USB decoder: reassembles split frames, skips noise, splits joined frames', () => {
  const d = new CL.SerialDecoder();
  const out = [];
  out.push(...d.push(Uint8Array.of(0x00, 0x3e, 2)));      // noise, then a header split across chunks
  out.push(...d.push(Uint8Array.of(0, 7, 8, 0x3e, 1, 0, 5))); // rest of frame 1 + all of frame 2
  assert.deepStrictEqual(out.map((f) => Array.from(f)), [[7, 8], [5]]);
});

test('APP_START is 0x01, 7 reserved bytes, then the app name', () => {
  const f = CL.appStart('CoreScope');
  assert.strictEqual(f[0], 1);
  assert.deepStrictEqual(Array.from(f.subarray(1, 8)), [0, 0, 0, 0, 0, 0, 0]);
  assert.strictEqual(new TextDecoder().decode(f.subarray(8)), 'CoreScope');
});

test('ADD_UPDATE_CONTACT matches the firmware layout (148 bytes, unknown path)', () => {
  const f = CL.addUpdateContact({ key: KEY_A, type: 2, name: 'Hill Top', lastAdvert: 1760000000, lat: 55.95, lon: -3.19 });
  assert.strictEqual(f.length, 1 + 32 + 3 + 64 + 32 + 4 + 8);
  assert.strictEqual(f[0], 9);
  assert.strictEqual(f[33], 2);          // type
  assert.strictEqual(f[35], 0xff);       // out_path_len: OUT_PATH_UNKNOWN
  const dv = new DataView(f.buffer, f.byteOffset);
  assert.strictEqual(dv.getUint32(1 + 32 + 3 + 64 + 32, true), 1760000000);
  assert.strictEqual(dv.getInt32(1 + 32 + 3 + 64 + 32 + 4, true), 55950000);
  assert.strictEqual(dv.getInt32(1 + 32 + 3 + 64 + 32 + 8, true), -3190000);
});

test('names are cut to 31 UTF-8 bytes on a character boundary', () => {
  const f = CL.addUpdateContact({ key: KEY_A, type: 1, name: 'é'.repeat(20) });
  const nameBytes = f.subarray(1 + 32 + 3 + 64, 1 + 32 + 3 + 64 + 32);
  const end = nameBytes.indexOf(0);
  assert.strictEqual(end, 30, 'fifteen 2-byte characters fit in 31 bytes');
  assert.strictEqual(new TextDecoder().decode(nameBytes.subarray(0, end)), 'é'.repeat(15));
});

test('roles map to advert types and back', () => {
  assert.strictEqual(CL.roleToType('repeater'), 2);
  assert.strictEqual(CL.roleToType('room'), 3);
  assert.strictEqual(CL.roleToType('room_server'), 3);
  assert.strictEqual(CL.roleToType('sensor'), 4);
  assert.strictEqual(CL.roleToType('companion'), 1);
  assert.strictEqual(CL.typeToRole(2), 'repeater');
});

test('client: start reads the device name and contact capacity', async () => {
  const fake = new FakeCompanion({ name: 'Bench Radio', maxContacts: 350 });
  const c = new CL.Client(fakeTransport(fake));
  const info = await c.start();
  assert.strictEqual(info.name, 'Bench Radio');
  assert.strictEqual(info.maxContacts, 350);
  assert.strictEqual(info.firmware, 'v1.9.1');
  assert.deepStrictEqual(fake.log.slice(0, 2), [1, 22]);
});

test('client: lists the device contacts with positions', async () => {
  const fake = new FakeCompanion({ contacts: [{ key: KEY_A, type: 2, name: 'Hill Top', lat: 55.5, lon: -3.25, lastAdvert: 1759000000 }, { key: KEY_B, name: 'Pocket' }] });
  const c = new CL.Client(fakeTransport(fake));
  await c.start();
  const list = await c.listContacts();
  assert.strictEqual(list.length, 2);
  assert.strictEqual(list[0].key, KEY_A);
  assert.strictEqual(list[0].name, 'Hill Top');
  assert.strictEqual(list[0].type, 2);
  assert.strictEqual(list[0].lat, 55.5);
  assert.strictEqual(list[0].lon, -3.25);
  assert.strictEqual(list[0].lastAdvert, 1759000000);
});

test('client: adds contacts one by one, reporting each result, and stops when told', async () => {
  const fake = new FakeCompanion({ failAddKeys: [KEY_B] });
  const c = new CL.Client(fakeTransport(fake));
  await c.start();
  const seen = [];
  const res = await c.addContacts([
    { key: KEY_A, type: 2, name: 'A' }, { key: KEY_B, type: 2, name: 'B' }, { key: KEY_C, type: 3, name: 'C' },
  ], { onProgress: (r) => seen.push(r.key + ':' + r.status) });
  assert.deepStrictEqual(res.map((r) => r.status), ['added', 'full', 'added']);
  assert.deepStrictEqual(seen, [KEY_A + ':added', KEY_B + ':full', KEY_C + ':added']);
  assert.ok(fake.contactView(KEY_A) && !fake.contactView(KEY_B) && fake.contactView(KEY_C));

  const fake2 = new FakeCompanion();
  const c2 = new CL.Client(fakeTransport(fake2));
  await c2.start();
  let n = 0;
  const res2 = await c2.addContacts([{ key: KEY_A, name: 'A' }, { key: KEY_B, name: 'B' }], { shouldStop: () => n++ >= 1 });
  assert.strictEqual(res2.length, 1, 'stopped after the first');
});

test('client: updating a contact keeps the path and flags the device already has', async () => {
  const fake = new FakeCompanion({ contacts: [{ key: KEY_A, type: 2, name: 'Old', flags: 1, outPathLen: 2 }] });
  const c = new CL.Client(fakeTransport(fake));
  await c.start();
  const existing = await c.listContacts();
  await c.addContacts([{ key: KEY_A, type: 2, name: 'New name', lat: 1, lon: 2 }], { existing });
  const v = fake.contactView(KEY_A);
  assert.strictEqual(v.name, 'New name');
  assert.strictEqual(v.outPathLen, 2, 'path kept');
  assert.strictEqual(v.flags, 1, 'flags (e.g. favourite) kept');
});

test('client: removes contacts and reports ones the device does not have', async () => {
  const fake = new FakeCompanion({ contacts: [{ key: KEY_A, name: 'A' }, { key: KEY_B, name: 'B' }] });
  const c = new CL.Client(fakeTransport(fake));
  await c.start();
  const res = await c.removeContacts([KEY_A, KEY_C]);
  assert.deepStrictEqual(res.map((r) => r.status), ['removed', 'missing']);
  assert.strictEqual(fake.contacts.length, 1);
});

test('client: a command with no reply times out instead of hanging', async () => {
  const silent = { onFrame: null, send() { return Promise.resolve(); }, close() { return Promise.resolve(); } };
  const c = new CL.Client(silent, { timeoutMs: 30 });
  await assert.rejects(c.start(), /timed out/);
});

test('backup round-trips every field, path included', async () => {
  const fake = new FakeCompanion({ contacts: [{ key: KEY_A, type: 3, name: 'Room', flags: 1, outPathLen: 2, lat: 54.6, lon: -5.9, lastAdvert: 1758000000 }] });
  const c = new CL.Client(fakeTransport(fake));
  await c.start();
  const list = await c.listContacts();
  const json = CL.toBackup(list, { device: 'Test' });
  const back = CL.fromBackup(json);
  assert.strictEqual(back.length, 1);
  const fake2 = new FakeCompanion();
  const c2 = new CL.Client(fakeTransport(fake2));
  await c2.start();
  await c2.addContacts(back);
  const v = fake2.contactView(KEY_A);
  assert.strictEqual(v.outPathLen, 2);
  assert.strictEqual(v.flags, 1);
  assert.strictEqual(v.name, 'Room');
  assert.strictEqual(v.lat, 54.6);
});

test('contactFromNode builds a contact from a CoreScope node', () => {
  const ct = CL.contactFromNode({ public_key: KEY_A.toUpperCase(), name: 'Hill Top', role: 'repeater', lat: 55.9, lon: -3.2, last_seen: '2026-10-10T12:00:00Z' });
  assert.strictEqual(ct.key, KEY_A);
  assert.strictEqual(ct.type, 2);
  assert.strictEqual(ct.lastAdvert, Math.floor(Date.parse('2026-10-10T12:00:00Z') / 1000));
  assert.strictEqual(CL.contactFromNode({ public_key: 'zz', name: 'x' }), null, 'a malformed key is refused');
});

Promise.all(pending).then(() => {
  console.log(`\n${passed} passed, ${failed} failed`);
  process.exit(failed ? 1 : 0);
});
