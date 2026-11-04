// Split by discovered top-level names, never by a hand-maintained allowlist.
// Same-named tests in different packages stay together: Go applies -run to
// every package. Subtests and fuzz seed cases remain with their parent.
import { createHash } from "node:crypto";
import { spawnSync } from "node:child_process";
import { resolve } from "node:path";
import { pathToFileURL } from "node:url";

export function partitionTests(names, count) {
  if (!Number.isInteger(count) || count < 1 || count > 64) throw new Error("Invalid shard count");
  const inventory = [...new Set(names)].sort();
  if (!inventory.length || inventory.some(name => !/^(Test|Example|Fuzz)\S*$/.test(name))) {
    throw new Error("Empty or invalid test inventory");
  }
  const shards = Array.from({ length: count }, () => []);
  for (const name of inventory) {
    const index = createHash("sha256").update(name).digest().readUInt32BE(0) % count;
    shards[index].push(name);
  }
  const flattened = shards.flat();
  if (flattened.length !== inventory.length || new Set(flattened).size !== inventory.length) {
    throw new Error("Shard inventory is incomplete or overlapping");
  }
  return shards;
}

export function parseInventory(stdout) {
  const names = [];
  for (const line of stdout.split(/\r?\n/).filter(Boolean)) {
    const event = JSON.parse(line);
    if (event.Action !== "output" || !event.Package) continue;
    const name = event.Output?.trim();
    if (/^(Test|Example|Fuzz)\S*$/.test(name ?? "")) names.push(name);
  }
  return names;
}

export function selectionPattern(names) {
  const escaped = names.map(name => name.replace(/[.*+?^${}()|[\]\\]/g, "\\$&"));
  return escaped.length ? `^(${escaped.join("|")})$` : "^$";
}

export function parseShardArgs(argv) {
  const [rawIndex, rawCount, ...args] = argv;
  const index = Number(rawIndex);
  const count = Number(rawCount);
  const listOnly = args[0] === "--list-only";
  const packages = listOnly ? args.slice(1) : args;
  if (!/^\d+$/.test(rawIndex ?? "") || !/^\d+$/.test(rawCount ?? "") ||
      !Number.isInteger(count) || count < 1 || count > 64 ||
      !Number.isInteger(index) || index < 0 || index >= count ||
      !packages.length || packages.some(p => !p.trim() || p.startsWith("-"))) {
    throw new Error("Usage: node scripts/go-test-shards.mjs INDEX COUNT [--list-only] PACKAGE...");
  }
  return { index, count, listOnly, packages };
}

function main() {
  const { index, count, listOnly, packages } = parseShardArgs(process.argv.slice(2));
  // Match the build configuration of execution, including race-only files.
  const listed = spawnSync("go", ["test", "-race", "-list", ".", "-json", ...packages], {
    encoding: "utf8", maxBuffer: 32 * 1024 * 1024,
  });
  if (listed.error) throw listed.error;
  if (listed.status !== 0) {
    process.stderr.write(listed.stderr ?? "");
    process.stderr.write(listed.stdout ?? "");
    process.exit(listed.status ?? 1);
  }
  const shards = partitionTests(parseInventory(listed.stdout), count);
  console.log(`Race shards: ${shards.map(shard => shard.length).join(" / ")} unique top-level names; selected ${index + 1}/${count}`);
  if (listOnly) return;
  const result = spawnSync("go", ["test", "-race", ...packages, "-count=1", "-timeout", "20m", "-run", selectionPattern(shards[index])], {
    stdio: "inherit",
  });
  if (result.error) throw result.error;
  process.exit(result.status ?? 1);
}

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) main();
