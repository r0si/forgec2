# Plugin authoring

A ForgeC2 plugin is a directory: a `manifest.yaml` plus an entry script (or a
`.wasm` module). The server loads it from its plugins tree or through the
install API. This document is the whole contract — layout, manifest, runtime
behaviour, tier limits, trust — and it is written so that a human **or an
agent** can produce a plugin the server accepts without reading the Go source.

Everything below is enforced by code, not by convention:

| where | what |
| --- | --- |
| [`internal/plugin/manifest.go`](../internal/plugin/manifest.go) | manifest fields and `Validate()` |
| [`docs/schemas/plugin-manifest.schema.json`](schemas/plugin-manifest.schema.json) | machine-readable manifest schema |
| [`docs/PLUGIN_TIERS.md`](PLUGIN_TIERS.md) | tier guarantees, quotas and trust policy |
| [`internal/plugin/executor.go`](../internal/plugin/executor.go) | how a run actually happens |
| [`internal/plugin/wasm.go`](../internal/plugin/wasm.go) | the WASM ABI v1 host side |

## The loop

```sh
# 1. generate a package that is valid by construction
go run ./cmd/plugin-init -name my-plugin -type command -interpreter python3 -dir ./my-plugin

# 2. edit the generated files

# 3. validate: static checks, then one real execution with a sample input
go run ./cmd/plugin-check ./my-plugin -run

# 4. machine-readable form (for agents and CI)
go run ./cmd/plugin-check ./my-plugin -run -json

# 5. stamp the package before installing it (see Trust below)
PLUGIN_SIGNING_KEY=<64-hex-seed> go run ./cmd/sign-plugin ./my-plugin
```

`plugin-init` flags: `-name`, `-type`, `-interpreter` (default: the first
interpreter that actually runs on this host), `-dir`, `-description`,
`-timeout`, `-force`, `-list`.

`plugin-check` flags: `-run`, `-input '<json>'`, `-timeout <seconds>`,
`-root <tree root>`, `-key <hex>[,<hex>…]`, `-require-signed`, `-json`.
It exits `0` when there are no error-level findings; warnings do not fail.
Only pass `-run` for packages you trust: every tier except `wasm` executes
real code as a child process.

## Package layout

```text
my-plugin/
├── manifest.yaml     # required, always this name
├── main.py           # the entry the manifest points at
└── lib/helpers.py    # optional; add "includes: [lib]" so it is part of the digest
```

`manifest.yaml` may use any of the fields in
[the schema](schemas/plugin-manifest.schema.json); `name`, `version`, `type`,
`entry` and `interpreter` are required, and `entry` must be a relative path
without `..`. `digest`/`signature` are written by `cmd/sign-plugin` — never by
hand.

## Runtime contract (native tiers)

`python`, `python3`, `powershell`, `pwsh`, `bash`, `sh` and `go` run the entry
as a **child process of the server** with the server account's reach. There is
no sandbox: a native plugin can do anything the server process can do. Untrusted
code belongs in the `wasm` tier.

| | |
| --- | --- |
| command line | `python main.py`, `powershell -File main.ps1`, `bash main.sh`, `go run main.go`, … |
| working directory | the plugin's own directory |
| stdin | one JSON object, see below (written once, then closed) |
| stdout | **exactly one** JSON object; anything else is a bug the server reports |
| stderr | diagnostics; captured, capped and shown on failure. Put logs here, never on stdout |
| environment | minimal: `PATH` (absolute entries only), `LANG`, `HOME`, `TMPDIR`, `TMP`, `TEMP` (all pointing at the OS temp directory), `PYTHONDONTWRITEBYTECODE=1`, `GOCACHE`, `GOPATH`, `PLUGIN_NAME`, `PLUGIN_VERSION`. No server secrets, no inherited env |
| output caps | 2 MiB per stream, truncated beyond that |
| wall clock | `manifest.timeout` (default 60s), clamped to 300s; a grandchild holding the pipes open is cut off 5s later |
| CPU budget | `plugins.max_cpu_seconds` (default 120s) of CPU time across the whole process tree |
| write budget | `plugins.max_write_mb` (default 1024 MiB) written by the tree — files, pipes and console alike |
| concurrency | bounded by `plugins.max_concurrent` (default 4) |

### Input

| type | stdin JSON |
| --- | --- |
| `command` | `{"agent_id": "…", "params": {…}, "config": {…}}` |
| `report` | `{"params": {…}, "config": {…}}` |
| `hook` | `{"event": {"type": "agent.connect", "timestamp": "…", "agent_id": "…", "payload": {…}}, "config": {…}}` |

`params` carries the operator's form values (the manifest declares them with
`params:`), `config` the install's configuration, `agent_id` the target agent
when the run is aimed at one. Treat every field as optional — a missing key is
normal.

### Output

`command` and `hook` plugins print a **Result**:

```json
{"success": true, "output": "human-readable summary", "data": {"any": "json"}, "error": ""}
```

`success` is the only meaningful field; `output` is what the operator sees,
`data` is stored with the task result, `error` explains `success: false`.
Hooks may print nothing at all (the server treats empty stdout as success); if
they do print, it must be a Result.

`report` plugins print a **Report**:

```json
{"title": "Asset inventory", "format": "markdown", "content": "IyBBc3NldCBJbnZlbnRvcnkK"}
```

`format` is `markdown`, `html` or `json`, and `content` is the document
**base64-encoded** (it is `[]byte` on the wire). Reporting is the one place
where a plugin's whole output is shown; keep it well-formed.

## Runtime contract (WASM tier)

`interpreter: wasm` runs a compiled module **inside** the server process,
sandboxed by the runtime: no child process, no WASI (no files, sockets,
environment or clock), a 16 MiB linear-memory cap, a 16 MiB module cap, capped
logs and result size. ABI v1:

```text
exports:
  memory            linear memory (the host writes its input past the module's
                    initial data, so static data is never clobbered)
  run(ptr, len) -> i64
                    ptr/len address the input JSON the host wrote. The i64
                    result locates the reply: ptr | (len << 32); 0 means
                    "no result". The reply is the same JSON object a native
                    plugin would print on stdout.
imports (optional):
  forgec2.log(ptr, len)   one diagnostic line, capped by the host
```

`run` **must** have exactly that signature: returning a two-field struct makes
clang/Rust return it through a hidden sret pointer, which the host does not
call, so ABI v1 packs the pair into one `i64`.

A ready-made module is in
[`examples/wasm/hello-report`](../examples/wasm/hello-report), and
`plugin-init -interpreter wasm` writes a C skeleton plus a `build.sh` that
works with clang (including wasi-sdk) or zig:

```sh
sh build.sh                                        # clang from PATH
CC="zig cc" TARGET=wasm32-freestanding sh build.sh # zig
```

The skeleton defines the only two libc calls a freestanding module needs
(`memcpy`/`memset`, which clang lowers copy loops to) and builds with
`-nostdlib -ffreestanding`; keep those definitions if you rewrite the file, or
wasm-ld fails with `undefined symbol: memcpy` on a plain clang (zig hides the
problem because it links its own runtime).

## Diagnostics

`plugin-check` prints one line per finding and, with `-json`, the same findings
as machine-readable JSON (`level`, `code`, `message`). Codes are stable; the
message carries the detail.

| code | meaning / what to do |
| --- | --- |
| `package.no_dir`, `package.missing_dir` | the directory argument is wrong or unreadable |
| `package.manifest_missing` | no `manifest.yaml`; generate the package with `plugin-init` |
| `package.entry_missing` | `entry` does not exist. For wasm this is normal before `sh build.sh` |
| `manifest.unreadable`, `manifest.invalid` | the YAML does not parse, or a field fails `Validate()` (the message names the field) |
| `manifest.no_events` | a hook subscribes to nothing, so it will never run |
| `tier.interpreter_missing` | the interpreter is not on this host's `PATH` (warning; the server needs it on *its* host) |
| `tier.interpreter_broken` | the interpreter resolves but does not execute (e.g. the WindowsApps `python.exe` alias, exit 9009); install a real one or name `python3` |
| `trust.unsigned` | no digest yet: fine while `plugins.require_signed` is off, stamp it with `cmd/sign-plugin` before enforcing |
| `trust.unverified` | signed but no `-key` was given, so the signature could not be checked |
| `trust.failed` | digest/signature verification failed (message lists both digests) or `require_signed` refused the package |
| `trust.bad_key` | `-key` is not a 32-byte hex Ed25519 public key |
| `wasm.invalid_module`, `wasm.too_large` | the module does not compile, or exceeds the 16 MiB cap |
| `wasm.missing_run`, `wasm.bad_abi` | `run` is missing or has the wrong signature (must be `(i32,i32) -> (i64)`) |
| `wasm.missing_memory`, `wasm.memory_too_large` | `memory` is missing, or its minimum exceeds the 256-page cap |
| `wasm.unexpected_import`, `wasm.bad_log_abi` | the module imports something other than `forgec2.log(ptr,len)`, e.g. WASI |
| `run.skipped`, `run.completed`, `run.failed` | the execution phase: not requested, exited 0, or failed (timeout, quota, non-zero exit) |
| `run.bad_input` | `-input` is not a JSON object |
| `result.ok`, `result.unsuccessful` | stdout parsed; `unsuccessful` means the plugin itself reported `success: false` |
| `result.bad_json` | stdout is not the JSON object for this plugin type (the message quotes it) |
| `result.empty_output` | a command/report plugin printed nothing |
| `result.empty_title`, `result.empty_report` | a report has no title / no content |

## Trust and installation

A package is a **unit**: the digest covers every file in the directory plus any
`includes` paths, minus the manifest and `.sig` sidecars. Editing a helper
after signing invalidates the signature, which is the point.

```sh
go run ./cmd/sign-plugin -gen                                  # one-time key pair
PLUGIN_SIGNING_KEY=<64-hex-seed> go run ./cmd/sign-plugin <dir> # stamp in place
go run ./cmd/sign-plugin -verify -key <64-hex-pubkey> <dir>     # verify
```

The server verifies packages against `plugins.trusted_keys` on load and install;
`plugins.require_signed` makes an unsigned package fatal. Check a package
against the server's policy before shipping it:

```sh
go run ./cmd/plugin-check ./my-plugin -key <64-hex-pubkey> -require-signed
```

Install by copying the directory into the plugins tree (it loads on start and
refresh) or through the UI's install flow, which applies the same checks.

### Verifying a package you received

`plugin-check` takes a **directory** and treats signatures as optional, because the bundled plugins ship unsigned. When
the package arrived from someone else and you already know which key you expect, use the receiving entry point instead:

```sh
go run ./cmd/verify-plugin package.zip -key <64-hex-pubkey>
go run ./cmd/verify-plugin ./extracted -key <64-hex-pubkey> -quiet
```

Three differences from `plugin-check`, and they are the whole reason it exists:

- It accepts an **archive** (or a directory) and unpacks it — refusing any entry that would resolve outside the
  destination, because the archive is attacker-supplied input.
- `-key` is **required**, so "verified" cannot quietly mean "the package agrees with itself".
- `RequireSigned` is **on**, so an unsigned package fails instead of being installed with a warning.

Exit codes match the other verifiers (`docs/VERIFIERS.md`): `0` verified, `1` invalid, unsigned or signed by a different
key, `2` the archive could not be read at all. `-quiet` leaves the answer to the exit code; `-json` prints the report.

A single top-level directory in the archive is unwrapped automatically, so the manifest is found where the packager put
it. A flat archive is extracted as-is.


## Prompt block for coding agents

Paste this into an agent working on a ForgeC2 plugin:

```text
Write a ForgeC2 plugin package. Layout: manifest.yaml + entry file in one directory.
Start from: go run ./cmd/plugin-init -name <name> -type command|hook|report -interpreter python3|bash|powershell|go|wasm -dir <dir>
Contract: stdin is ONE JSON object {"params": {...}, "config": {...}} (+"agent_id" for command, +"event" for hook);
stdout must be ONE JSON object — a Result {"success": true, "output": "...", "data": {...}} for command/hook,
or a Report {"title": "...", "format": "markdown", "content": "<base64 of the document>"} for report.
Logs go to stderr. No extra stdout output, ever. Runs are bounded by the manifest timeout plus CPU/write budgets.
After every edit run: go run ./cmd/plugin-check <dir> -run -json
and fix every finding with "level": "error" until the report has none. Do not hand-edit digest/signature.
Full contract: docs/PLUGIN_AUTHORING.md
```
