const test = require('node:test');
const assert = require('node:assert');
const { applyDiscount, oneMinute } = require('../src/price');

test('the package installed from the registry works', () => assert.equal(oneMinute, 60000));
test('applies a percentage', () => assert.equal(applyDiscount(200, 10), 180));
