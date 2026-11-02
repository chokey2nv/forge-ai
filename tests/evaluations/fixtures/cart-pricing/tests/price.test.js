const test = require("node:test");
const assert = require("node:assert");
const { applyDiscount } = require("../src/price.js");

test("10% off 200 is 180", () => assert.strictEqual(applyDiscount(200, 10), 180));
test("0% off leaves the price unchanged", () => assert.strictEqual(applyDiscount(50, 0), 50));
test("25% off 19.99 rounds to cents", () => assert.strictEqual(applyDiscount(19.99, 25), 14.99));
