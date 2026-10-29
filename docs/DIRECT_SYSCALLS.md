# Direct syscalls: what exists, and what is deliberately not wired

Two pure parsers and their composition exist in `internal/payload/agent`, and **no agent code path calls them**. This page exists so
that the next person (or the next decision) starts from what is true rather than from what the names suggest.

## What exists

| File | Does | Touches Windows? |
|---|---|---|
| `pe_exports.go` | name → **RVA** from a PE32/PE32+ export table, refusing forwarders and truncation | **no** — byte slices only |
| `syscall_ssn.go` | **RVA → syscall number**, refusing hooked-but-unparsable stubs, zero, and out-of-range immediates | **no** |
| `syscall_resolve.go` | composes both for the five `Nt*` primitives a syscall layer would need | **no** |

All three build and are tested **on every platform**, which is the point: the failure mode of this technique is not "it did not parse"
but **"it parsed the wrong thing"**, and a wrong syscall number calls a different function on the target with no visible symptom.

### The unit trap, recorded because it already bit once

`FindExportRVA` returns an **RVA**; `ExtractSSN` reads bytes at a **file offset**. Passing one where the other is expected does not
fail loudly — it scans the wrong region and reports *"no syscall stub prologue"*, which reads like a missing export. `ResolveNtSyscalls`
translates once, through `FileOffsetForRVA`, and that translation is what the composed test pins.

## What is deliberately absent

- **No caller.** `grep -n "ResolveRequiredSyscalls" internal/payload/agent --include='*.go'` returns the definition and its test only.
- **No `Nt*` indirection.** The existing evasion path (`edr_strategies_windows.go`, API hashing, AMSI/ETW patching) is untouched, so
  today's binaries behave exactly as before.
- **No assembly.** Indirect calls through a ntdll gadget (`jmp rbx`) need a hand-written trampoline; that is the step that changes the
  binary, and it is not taken yet.

## The wiring decision, with its options

Wiring in requires one choice, and the choice is about **blast radius**, not about code:

**Option A — hang it on `sleepMaskPolicyEnabled`.** One switch means "be stealthier"; an operator who already accepts sleep-masking
accepts syscall indirection. *Pro:* one concept, one thing to explain. *Con:* the two mechanisms fail differently (a wrong sleep mask
corrupts memory; a wrong SSN calls the wrong kernel routine), so an operator cannot enable one without the other.

**Option B — a separate `syscallPolicy`.** Each mechanism gets its own switch and its own failure story. *Pro:* the blast radii stay
separate, and a bad table can be turned off without losing sleep obfuscation. *Con:* one more switch to document and one more state
to test in combination.

**Recommendation: B.** The two mechanisms share a purpose (reduce detection) but not a failure mode, and this codebase's own history
says the expensive bugs come from conflating two things that a reader assumed were one switch.

Whichever is chosen, the wiring must: resolve once at startup (never per call, and never from a mapped image an EDR may have patched),
refuse to run when the table is incomplete rather than falling back to the `Nt*` exports, and record the source build in `/ready` so a
mismatch is visible before a task fails mysteriously.
