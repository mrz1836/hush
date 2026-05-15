# Contract — `scripts/check-package-map-vs-code.sh`

The drift-detection script delivered by SDD-33 (FR-013 / SC-002). This
file is the **CLI contract** for the script: invocation, exit codes,
stdout/stderr shape. The script implementation follows
[research.md R-001](../research.md) for the algorithm.

---

## Invocation

```bash
scripts/check-package-map-vs-code.sh
```

Run from the repository root. The script SHALL refuse to run from any
other working directory and SHALL exit `2` with a stderr message
naming the expected working directory.

### Optional arguments

The script ships with **zero flags** in v0.1.0. Future additions
(`--package <pkg>` to scope, `--format json` for machine output) are
out of scope per FR-017 (no new behaviour) and may land in a
follow-on chunk.

### Required environment

- `go` (≥ 1.26.1) on `PATH` — the script invokes `go doc`.
- POSIX-compatible `bash`, `awk`, `sed`, `grep`, `diff`, `sort`,
  `comm` on `PATH`. All standard on macOS + Linux.
- `git` on `PATH` (only used for the working-directory check via `git
  rev-parse --show-toplevel`).

The script SHALL NOT require Go modules to be downloaded — `go doc`
on the local module tree works without network.

### Required filesystem state

- `docs/PACKAGE-MAP.md` exists.
- `internal/...` packages exist and compile cleanly (the script
  checks neither — it inherits `go doc`'s behaviour, which prints a
  diagnostic on compile failure and exits non-zero).

---

## Exit codes

| Code | Meaning |
|------|---------|
| `0` | No drift detected. PACKAGE-MAP.md and `go doc ./internal/...` agree on the set of exported symbols per package. |
| `1` | Drift detected. At least one symbol in code is missing from PACKAGE-MAP.md, OR at least one entry in PACKAGE-MAP.md no longer resolves to a real symbol. Stdout names every offender. |
| `2` | Usage error: not run from the repository root, missing tooling, or `docs/PACKAGE-MAP.md` not found. Stderr names the cause. |
| `3` | Internal error: `go doc` returned non-zero, awk/sed parse failure, or unhandled exception. Stderr captures the underlying tool's error. |

Other exit codes are reserved.

---

## Stdout / stderr shape

### On success (exit 0)

Stdout: a single line confirming the check passed and the count of
packages and symbols verified. Example:

```text
check-package-map-vs-code: 19 packages, 247 exported symbols, 0 drift.
```

Stderr: empty.

### On drift (exit 1)

Stdout: zero or more report blocks, one per drifting package, in the
form:

```text
internal/<pkg>:
  + doc-only:   <symbol>      (PACKAGE-MAP entry has no matching code symbol)
  + doc-only:   <symbol>
  - code-only:  <symbol>      (code exports symbol absent from PACKAGE-MAP)
  - code-only:  <symbol>
```

After all per-package blocks, a summary line:

```text
check-package-map-vs-code: <n> packages drifting, <total> symbols total. Reconcile via PACKAGE-MAP.md edit or symbol removal.
```

Stderr: empty (drift reports go to stdout — they are the script's
intended signal output, not error output).

### On usage / internal error (exit 2 / 3)

Stdout: empty.

Stderr: one or more diagnostic lines, prefixed with
`check-package-map-vs-code: error: ` and naming the cause.

---

## Idempotence and side-effects

- The script is **read-only**: it does not write any file, mutate
  Git state, modify `docs/PACKAGE-MAP.md`, or invoke any package's
  `init` function beyond what `go doc` triggers (which is none for
  doc-extraction).
- The script's exit code depends only on the current working tree —
  it has no caching, no networked lookup, no state file.
- Running the script twice in a row produces identical output (and
  the same exit code) as long as the working tree is unchanged.

---

## Performance contract

Target: **end-to-end runtime ≤ 30 seconds on a developer laptop**
(observable via `time scripts/check-package-map-vs-code.sh` from the
repo root). Measurement assumes the Go module's build cache is warm
(typical post-`go test`); cold-cache runs may take longer and that
is acceptable.

---

## Self-test (operator-runnable)

The script header comment block documents a self-test recipe:

```bash
# Self-test (operator-runnable; verifies the script catches drift):
#
# 1. Copy the repo to a tempdir:
#      cp -R . /tmp/hush-self-test && cd /tmp/hush-self-test
# 2. Inject a stub exported function in a package:
#      printf '\nfunc StubForDriftCheck() {}\n' >> internal/audit/doc.go
# 3. Run the script:
#      scripts/check-package-map-vs-code.sh
# 4. Expected output (exit code 1):
#      internal/audit:
#        - code-only: StubForDriftCheck
# 5. Cleanup:
#      cd - && rm -rf /tmp/hush-self-test
```

This recipe is the script's `// see #N`-style documentation contract:
operators run it on demand to verify the gate before merging
PACKAGE-MAP-touching PRs.

---

## Future-extension constraints (informative)

For any future enhancement (out of scope for SDD-33):

- `--format json` MUST add a JSON object on stdout with shape
  `{"drift": [{"package":"...", "kind":"doc-only|code-only", "symbol":"..."}], "ok": true|false}`.
- `--package <import-path>` MUST scope the check to one package and
  exit 0 if that single package agrees, regardless of other packages'
  state.
- A `-q` / `--quiet` flag MUST suppress stdout output but preserve
  the exit code.

Adding any of these is FR-017 (no new behaviour) territory and
requires its own chunk.
