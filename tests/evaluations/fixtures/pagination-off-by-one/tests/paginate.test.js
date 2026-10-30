const assert = require("node:assert");
const { paginate } = require("../src/paginate.js");

let failed = 0;
function test(name, fn) {
  try { fn(); console.log(`ok - ${name}`); }
  catch (e) { failed++; console.error(`not ok - ${name}: ${e.message}`); }
}

test("first page",         () => assert.deepStrictEqual(paginate([1,2,3,4,5], 1, 2), [1,2]));
test("last page (partial)",() => assert.deepStrictEqual(paginate([1,2,3,4,5], 3, 2), [5]));
test("last page (full)",   () => assert.deepStrictEqual(paginate([1,2,3,4],   2, 2), [3,4]));

process.exit(failed ? 1 : 0);