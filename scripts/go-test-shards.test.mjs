import test from "node:test";
import assert from "node:assert/strict";
import { parseInventory, partitionTests, selectionPattern } from "./go-test-shards.mjs";

test("every discovered test, example and fuzz parent occurs exactly once", () => {
  const names = ["TestAlpha", "TestBeta", "ExampleCopy", "FuzzDecode", "Test中文"];
  const shards = partitionTests(names, 4);
  assert.deepEqual(shards.flat().sort(), [...names].sort());
  assert.equal(new Set(shards.flat()).size, names.length);
});
test("bare Test, Example and Fuzz names are not silently omitted", () => {
  const names = ["Test", "Example", "Fuzz"];
  const stdout = names.map(name => JSON.stringify({ Action: "output", Package: "p", Output: `${name}\n` })).join("\n");
  assert.deepEqual(parseInventory(stdout), names);
  assert.deepEqual(partitionTests(names, 4).flat().sort(), [...names].sort());
});
test("assignment is deterministic and independent of listing order", () => {
  const names = Array.from({ length: 1000 }, (_, i) => `TestCase${i}`);
  assert.deepEqual(partitionTests(names, 4), partitionTests([...names].reverse(), 4));
});
test("same-name tests across packages use the same selection", () => {
  assert.deepEqual(partitionTests(["TestSame", "TestSame"], 4), partitionTests(["TestSame"], 4));
});
test("empty inventories and invalid counts fail closed", () => {
  assert.throws(() => partitionTests([], 4));
  assert.throws(() => partitionTests(["TestX"], 0));
  assert.throws(() => partitionTests(["TestX"], 1.5));
  assert.throws(() => partitionTests(["BenchmarkX"], 4));
});
test("inventory comes only from package JSON output, not status or benchmark lines", () => {
  const stdout = [
    { Action: "output", Package: "p", Output: "TestOne\n" },
    { Action: "output", Package: "p", Output: "ExampleTwo\n" },
    { Action: "output", Package: "p", Output: "FuzzThree\n" },
    { Action: "output", Package: "p", Output: "BenchmarkIgnored\n" },
    { Action: "output", Output: "TestNoPackage\n" },
    { Action: "pass", Package: "p" },
  ].map(event => JSON.stringify(event)).join("\r\n");
  assert.deepEqual(parseInventory(stdout), ["TestOne", "ExampleTwo", "FuzzThree"]);
  assert.throws(() => parseInventory("not JSON"));
});
test("selection is anchored and regex metacharacters are literal", () => {
  const pattern = new RegExp(selectionPattern(["TestA", "ExampleB", "Test.X"]));
  assert.ok(pattern.test("TestA"));
  assert.ok(pattern.test("Test.X"));
  assert.equal(pattern.test("TestAX"), false);
  assert.equal(pattern.test("TestaX"), false);
  assert.equal(new RegExp(selectionPattern([])).test("TestA"), false);
});
