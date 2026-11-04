import test from "node:test";
import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";
import { parseInventory, parseShardArgs, partitionTests, selectionPattern } from "./go-test-shards.mjs";

test("CLI arguments are validated before invoking Go", () => {
  for (const argv of [
    [], ["", "4", "./p"], ["0", "", "./p"], ["0", "NaN", "./p"],
    ["0", "0", "./p"], ["0", "65", "./p"], ["0", "1.5", "./p"],
    ["-1", "4", "./p"], ["4", "4", "./p"], ["1.5", "4", "./p"],
    ["0", "4"], ["0", "4", "--list-only"], ["0", "4", "--unknown"],
    ["0", "4", " "], ["0", "4", "./p", "-run", "TestX"],
  ]) assert.throws(() => parseShardArgs(argv), /Usage:/, JSON.stringify(argv));
});

test("CLI preserves package selections and optional list-only mode", () => {
  assert.deepEqual(parseShardArgs(["0", "4", "./p/...", "./q"]), {
    index: 0, count: 4, listOnly: false, packages: ["./p/...", "./q"],
  });
  assert.deepEqual(parseShardArgs(["63", "64", "--list-only", "./p"]), {
    index: 63, count: 64, listOnly: true, packages: ["./p"],
  });
});

test("invalid CLI configuration fails without needing Go on PATH", () => {
  const result = spawnSync(process.execPath, [
    fileURLToPath(new URL("./go-test-shards.mjs", import.meta.url)), "0", "65", "./p",
  ], { encoding: "utf8", env: { ...process.env, PATH: "", Path: "" }, timeout: 10_000 });
  assert.ifError(result.error);
  assert.equal(result.status, 1);
  assert.equal(result.stdout, "");
  assert.match(result.stderr, /Usage: node scripts\/go-test-shards\.mjs/);
  assert.doesNotMatch(result.stderr, /spawnSync go|ENOENT/);
});

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
