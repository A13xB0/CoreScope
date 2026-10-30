// Damaged-copy filter fields (server damage.go) and the server pushdown test.
'use strict';
const assert = require('assert');
const PF = require('../../public/packet-filter.js');

const damaged = { payload_type: 5, damage: { of: 'abcd1234', kind: 'body', bits: 2, offsets: [27] } };
const clean = { payload_type: 5 };
const f = (expr) => PF.compile(expr).filter;

assert.strictEqual(f('damaged')(damaged), true);
assert.strictEqual(f('damaged')(clean), false);
assert.strictEqual(f('!damaged')(clean), true);
assert.strictEqual(f('damage == body')(damaged), true);
assert.strictEqual(f('damage == tail')(damaged), false);
assert.strictEqual(f('damaged_of == abcd1234')(damaged), true);

assert.strictEqual(PF.requires('damaged', 'damaged'), true);
assert.strictEqual(PF.requires('damaged == true', 'damaged'), true);
assert.strictEqual(PF.requires('type == GRP_TXT && damaged', 'damaged'), true);
assert.strictEqual(PF.requires('damaged || hops > 3', 'damaged'), false);
assert.strictEqual(PF.requires('!damaged', 'damaged'), false);
assert.strictEqual(PF.requires('damaged == false', 'damaged'), false);
assert.strictEqual(PF.requires('', 'damaged'), false);
assert.ok(PF.FIELDS.some((x) => x.name === 'damaged'));
console.log('test-packet-filter-damaged: ok');
