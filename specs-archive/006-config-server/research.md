# Phase 0 Research: `internal/config` (Server)

**Feature**: 006-config-server — server-side TOML schema, defaults, validation, and path-safety
**Date**: 2026-04-28

This document resolves every technical decision the plan depends on. Each entry follows the **Decision / Rationale / Alternatives considered** format. There are no remaining `NEEDS CLARIFICATION` markers in the spec; the five clarification answers from Session 2026-04-28 (`spec.md` §Clarifications) are encoded into the relevant decisions below.

---

## R-001 — TOML decoder choice and strict-mode wiring

**Decision**: Use `github.com/pelletier/go-toml/v2`. Construct the decoder via `toml.NewDecoder(r)`, call `.DisallowUnknownFields(true)` before `.Decode(&decoded)`. The decoder reads the file via `os.Open(path)` → `*os.File` → `defer f.Close()` → pass the file as the `io.Reader`.

**Rationale**: The chunk contract (SDD-06.md) names `pelletier/go-toml/v2` and `DisallowUnknownFields(true)` explicitly. The strict-decode mode is the load-bearing feature for spec FR-002 (typo defence): an unknown TOML key returns a `*toml.StrictMissingError` (or modern equivalent — the v2 API surface is tracked) which the loader translates into the package's `ErrUnknownField` sentinel via `errors.Is`-friendly wrapping (`fmt.Errorf("hush/config: unknown field %q: %w", fieldPath, ErrUnknownField)`).

The reader-based API lets the loader keep a `*os.File` open only for the decode duration, never reading the entire file into a buffer. This caps peak memory at the decoder's internal buffer (~64 KiB for typical configs) regardless of input size, satisfying the fuzz-target's "no unbounded memory growth" invariant.

**Alternatives considered**:
- *`BurntSushi/toml`*: rejected. The chunk contract pins pelletier/v2; switching would require a constitutional amendment. Behaviourally `BurntSushi/toml`'s strict-decode is also available (`Decoder.DisallowUnknownFields`) but its API surface is older and less idiomatic.
- *Hand-rolled TOML subset parser*: rejected — see Complexity Tracking row 1 in plan.md.
- *Read whole file into memory then decode*: rejected. Peak memory under fuzz would be unbounded; the streaming approach is strictly safer.

---

## R-002 — `listen_addr` validation

**Decision**: Parse the supplied string via `netip.ParseAddrPort`. On parse error, return `ErrListenMalformed`. On success, extract `addrport.Addr()` and check in this order: (1) `IsLoopback()` → `ErrListenLoopback`; (2) `IsUnspecified()` → `ErrListenUnspecified`; (3) `TailscaleCGNAT.Contains(addr)` → ACCEPT; (4) anything else → `ErrListenPublic`. Each of `ErrListenLoopback`, `ErrListenUnspecified`, `ErrListenPublic` wraps `ErrTailscaleBindRequired` so a downstream `errors.Is(err, ErrTailscaleBindRequired)` matches all three.

`TailscaleCGNAT` is a package-level `var TailscaleCGNAT = netip.MustParsePrefix("100.64.0.0/10")`. The `MustParse` call is safe because the literal is a compile-time constant; a panic at package-load is a programmer error not an operator error.

**Rationale**: `netip.AddrPort` is the stdlib's canonical "parsed address with port" type; it handles IPv4 (`100.96.10.4:7743`) and IPv6 bracketed notation (`[100::1]:7743`) uniformly. Using `IsLoopback` / `IsUnspecified` catches `127.0.0.1`, `::1`, `0.0.0.0`, `[::]` with a single API call each. The CGNAT prefix `100.64.0.0/10` is the documented Tailscale range per `docs/CONFIG-SCHEMA.md` `[network] allowed_cidrs` default — no other range is acceptable in v0.1.0.

The order-of-checks matters: a loopback IPv6 like `::1` is NOT in the CGNAT prefix and NOT unspecified, so without explicit `IsLoopback` first it would fall through to `ErrListenPublic`, which is the wrong category. The chunk contract's "reject IsLoopback, IsUnspecified, public IPs; allow ONLY 100.64.0.0/10" order is preserved.

The wrap relationship (`ErrListenLoopback wraps ErrTailscaleBindRequired`) is what spec FR-009 requires: each rejection is "distinct, named" (different sentinels per category) AND callers can match the umbrella with a single `errors.Is`. This is the same pattern Go's stdlib uses for `os.ErrNotExist` wrapping `*os.PathError`.

**Alternatives considered**:
- *Use `net.ParseIP` + `net.SplitHostPort`*: rejected. `netip.AddrPort` does both in one call and returns a value-type (no allocation, no nil-checks). `net.IP` requires the legacy 4-byte/16-byte branching dance that `netip.Addr` solves.
- *Treat `0.0.0.0` as a separate "wildcard bind" sentinel*: rejected. The constitution's Principle VI is unambiguous: any non-Tailscale bind is forbidden. There is no "advanced operator can bind to 0.0.0.0" mode.
- *Resolve hostnames via `net.LookupHost`*: rejected. Hostname resolution at config-load time depends on DNS, which is non-deterministic and can fail under launchd boot ordering. The spec assumes the operator writes a literal Tailscale IP. Hostname support is a v0.2 concern, if ever.

---

## R-003 — `audit_log` path-safety: stdlib-correct containment check

**Decision**: After both `audit_log` and `state_dir` have been `~`-expanded and run through `filepath.Abs`, compute `rel, err := filepath.Rel(absStateDir, absAuditLog)`. The audit log is "inside" `state_dir` iff: (a) `err == nil`, AND (b) `rel != ".."`, AND (c) `!strings.HasPrefix(rel, ".."+string(filepath.Separator))`, AND (d) `!filepath.IsAbs(rel)` (defensive — should never be true after `filepath.Rel`, but guards against future stdlib behaviour change). Any failure → `ErrAuditLogEscape`.

The chunk contract names `filepath.HasPrefix(audit_log, state_dir)` as the implementation. **`filepath.HasPrefix` is documented as deprecated by the Go standard library** ("HasPrefix exists for historical compatibility and should not be used. The result is unreliable as it does not respect path boundaries and does not ignore case where required."). The plan honours the chunk-contract's *intent* (path-containment check) while using the stdlib's recommended substitute. The behavioural contract is identical for the inputs this loader sees (operator-supplied paths after `filepath.Abs` canonicalisation); the difference is correctness on edge cases like `state_dir = "/usr"` and `audit_log = "/usrlocal/audit"`, where `HasPrefix` would falsely accept (`"/usrlocal"` starts with `"/usr"` as a string prefix) and `filepath.Rel` correctly rejects (the relative path is `"../usrlocal/audit"`).

**Rationale**: `filepath.Rel` is the Go-idiomatic, separator-aware containment primitive. The four-part check above is the canonical "is path B underneath path A" recipe documented in multiple security-review articles in the Go ecosystem (notably the Snyk Go path-traversal advisories). It correctly handles:
- Trailing-slash differences (`/foo` vs `/foo/`): `Abs` + `Rel` normalises both.
- Symlinks within `state_dir`: out of scope. The loader does not call `filepath.EvalSymlinks` because (a) the spec describes path-containment in lexical terms, not link-resolved terms, and (b) symlink resolution requires the directories to exist — the audit log file may not exist yet at first load (the server creates it). A future hardening pass MAY add symlink resolution; that is not SDD-06's contract.
- Different drive letters / Windows path conventions: out of scope (Windows is project-wide out of scope).

Honouring the chunk-contract's named function while substituting the stdlib-correct primitive is a deliberate, documented divergence — the plan's Constitution Check section X notes this as a "stdlib-correct refinement" rather than a deviation; the spec's FR-005 describes the user-visible behaviour ("rejected with a distinct, named error") in lexical terms, so the substitute satisfies the spec exactly.

**Alternatives considered**:
- *Use `filepath.HasPrefix` literally per the chunk contract*: rejected. Documented-deprecated; vulnerable to the prefix-on-substring false-positive shown above.
- *Use `os.Root` (Go 1.24+) for filesystem-rooted access*: rejected. `os.Root` requires the loader to actually open and operate within a rooted filesystem view, which is a much larger surface change for a load-time path check. SDD-06 only validates the path string; SDD-13 (audit-handler) may use `os.Root` for the actual file open.
- *Resolve symlinks via `filepath.EvalSymlinks` first*: deferred. See above — out of scope for load-time validation.

---

## R-004 — `state_dir` non-creation discipline

**Decision**: The loader never creates, modifies, or chmods `state_dir`. After `~` expansion + `filepath.Abs`, the loader calls `os.Stat(absStateDir)`. If the error is `errors.Is(err, fs.ErrNotExist)`, return `ErrStateDirNotFound`. If the result is non-nil but the FileInfo's `IsDir()` is `false`, return `ErrStateDirUnsafe` (path resolves to a regular file, a symlink to a non-directory, etc.). On any other `os.Stat` error (permission denied, EIO), wrap and return as `fmt.Errorf("hush/config: stat %s: %w", absStateDir, err)` — those errors are operator-environment failures, not config errors, and don't have a sentinel.

The loader does NOT check the directory mode (e.g., `0o700`). File-mode enforcement is SDD-10's job (startup hardening per spec FR-15 / AC-8). A `state_dir` with mode `0o755` will load successfully here but fail SDD-10's startup check. This separation matches `docs/CONFIG-SCHEMA.md` validation-rules-summary table: "state dir/file modes are too loose" is listed as a *startup* failure, not a *load* failure.

**Rationale**: Spec clarification 1 (Session 2026-04-28) chose Option A: "Reject with a typed error; never create. Creation is `hush init`'s job." The loader's job is gated, read-only validation. `hush init` (SDD-15) is the wizard that creates `~/.hush/` with mode `0o700` if the operator confirms. Splitting these concerns:
- Keeps the loader idempotent (calling it twice with the same input produces the same result).
- Keeps the loader safe to call from any consumer that doesn't expect side-effects (e.g., a future `hush config validate` subcommand).
- Avoids the "config load creates a directory" surprise that violates the principle of least astonishment.

**Alternatives considered**:
- *Auto-create with `os.MkdirAll(absStateDir, 0o700)`*: rejected by spec clarification 1. Operators have a documented init wizard; the loader is not a wizard.
- *Treat missing `state_dir` as a warning, not an error*: rejected. Without a real `state_dir`, the audit-log containment check (R-003) cannot run reliably. A missing state dir is a configuration error.

---

## R-005 — `~` path expansion semantics

**Decision**: The loader implements one form of expansion: a leading `~` followed by either end-of-string or `string(filepath.Separator)` is replaced with the result of `os.UserHomeDir()`. Concretely:
- `"~"` → `os.UserHomeDir()`
- `"~/audit.jsonl"` → `filepath.Join(homeDir, "audit.jsonl")`
- `"~user/foo"` → NOT expanded, treated as a literal path with the leading `~user` segment preserved (the result will fail `os.Stat` or path-containment, producing `ErrStateDirNotFound` or `ErrAuditLogEscape` as appropriate).
- `"$HOME/audit.jsonl"` → NOT expanded; treated as a literal path.
- `"~/foo/${SOMETHING}/bar"` → only the `~` is expanded; `${SOMETHING}` is preserved literally.

After this expansion, `filepath.Abs` canonicalises the result.

`os.UserHomeDir()` reads `$HOME` on Unix (and `%USERPROFILE%`/`%HOMEPATH%`+`%HOMEDRIVE%` on Windows, which is project-wide out of scope). Reading `$HOME` is **NOT** a violation of FR-007 ("must not consult environment variables for any secret-bearing field"): `$HOME` is not secret-bearing — it is a non-secret, ubiquitous Unix convention published in `printenv`, `id`, and every shell's prompt. The self-test (`TestLoadServer_DoesNotReadSecretsFromEnv`) sets *secret-named* env vars (`HUSH_DISCORD_TOKEN`, `HUSH_VAULT_PASSPHRASE`, etc.) and asserts they are absent from the loaded `Server`; it does NOT assert `$HOME` is unread, because it must be.

**Rationale**: Spec clarification 2 (Session 2026-04-28) chose Option A: "Expand a leading `~` to `$HOME`, then resolve to absolute paths via `filepath.Abs` before path-safety checks. No other shell-style expansion." The conservative single-form expansion mirrors how `cd ~/foo` works in every Unix shell — operator muscle memory carries over without surprise.

**Alternatives considered**:
- *Expand `~user` (other-user home directories)*: rejected by spec clarification 2. Looking up another user's home requires `os/user.Lookup`, which does NSS lookups, which can hang under launchd's NSS-not-yet-ready boot phase. The single-`~` form requires only `$HOME`, which is set before any user-mode launchd unit runs.
- *Expand `$VAR` env-style references*: rejected by spec clarification 2 + FR-007 spirit. Env-driven path overrides are a known foot-gun for "load-time-vs-runtime divergence" bugs.
- *Use `os.ExpandEnv`*: rejected. `os.ExpandEnv` does both `${VAR}` and `$VAR` expansion, neither of which the spec wants.

---

## R-006 — Defaults application: two-struct decode pipeline

**Decision**: The decoder pipeline is two-struct:

```go
// Wire-shape — pointer / sentinel types where "absent vs zero" matters.
type serverDecoded struct {
    Server   serverSectionDecoded   `toml:"server"`
    Discord  discordSectionDecoded  `toml:"discord"`
    Crypto   cryptoSectionDecoded   `toml:"crypto"`
    Network  networkSectionDecoded  `toml:"network"`
    Security securitySectionDecoded `toml:"security"`
}

// Public shape — concrete types only; what callers see.
type Server struct { /* ... */ }
```

The `*Decoded` types use:
- `*bool` for fields where `false` is a valid explicit value distinct from "absent" (e.g., `[network] require_tailscale`, `[security] require_*`).
- `string` (zero value `""`) for paths and IDs — the empty string is the "absent" sentinel because no documented path/ID has empty as a valid value.
- `string` for duration fields — the empty string is "absent"; non-empty is parsed via `time.ParseDuration` in the materialize step (any parse error → `ErrInvalidDuration`).
- `*uint32` / `*uint8` for numeric crypto params — zero is a valid "explicitly low and to be rejected" value distinct from "absent and use the default". (E.g., `argon_memory_mb = 0` should produce `ErrArgonMemoryTooLow`, not "default to 256".)
- `[]string` (nil for absent, non-nil including empty for explicit) for `allowed_cidrs`.

After decode, a `materialize(serverDecoded) (*Server, error)` function:
1. For each pointer field that is nil, substitutes the corresponding `Default*` constant.
2. For each empty-string duration, parses the corresponding `Default*` duration constant directly.
3. For non-nil pointer fields, dereferences and copies the value.
4. For empty-string non-duration fields, applies the default if one exists; otherwise records the field as "missing required" and accumulates an `ErrMissingRequiredField`.
5. Returns the populated `*Server` plus any accumulated error (errors.Join for multiple missing-field cases, so the operator sees all missing fields in a single load attempt).

Validation runs LAST, on the materialized `*Server`, via `Server.Validate`.

**Rationale**: The two-struct approach cleanly separates wire shape (driven by go-toml/v2's decoder behaviour) from public API (driven by what consumers want — concrete types, no nil-checks). The pointer-distinction trick is the standard Go idiom for "did the operator explicitly set this field" — without it, the loader cannot tell `require_tailscale = false` (must reject per FR-005c) from `# require_tailscale not set` (must default to true).

The `errors.Join` for multiple missing-required-field errors gives the operator a single round-trip to author a complete config, rather than the "fix one field, re-run, see the next missing field" frustration. The chunk contract does not mandate `errors.Join` but the spec's SC-004 ("an operator who introduces a single typo or schema violation receives a single, named error") implies the operator-experience priority — for multiple violations, joining is the operator-friendly option.

**Alternatives considered**:
- *Single-struct decode with a `decoded` bitfield*: rejected. The bitfield duplicates information the pointer-presence already encodes; introduces a parallel "did decode populate this" tracker that can drift from reality.
- *Custom `UnmarshalTOML` per field type*: rejected. go-toml/v2 supports this but the implementation cost is higher than the two-struct approach, and it scatters defaulting logic across many small types instead of centralising it in `materialize`.
- *Apply defaults DURING decode via go-toml/v2's `Default(...)` decoder option*: rejected. The library does not expose a per-field-default API of this shape; emulating it would require post-decode walks of the syntax tree. Two-struct is simpler.
- *Stop on first missing-required-field error*: rejected by SC-004 spirit. Operator ergonomics favour `errors.Join`.

---

## R-007 — Sentinel error catalogue and wrap relationships

**Decision**: The package exports the following sentinels (all `var Err... = errors.New("hush/config: ...")`); inline `//nolint:gochecknoglobals` comments cite the constitutional sentinel-class precedent.

Top-level (no wrap):
- `ErrTOMLDecode` — wraps any go-toml/v2 decode error other than unknown-field; the wrapped error is the underlying go-toml/v2 type so `errors.As` can extract structured info.
- `ErrUnknownField` — strict-decode rejected an unknown TOML key; wraps the go-toml/v2 strict-mode error; the message names the offending field.
- `ErrMissingRequiredField` — required field absent or empty; the message names the offending field.
- `ErrInvalidDuration` — `time.ParseDuration` failed on a duration-shaped field; the message names the field and the offending value.

Network family:
- `ErrTailscaleBindRequired` — umbrella for "address is not in the Tailscale CGNAT range".
- `ErrListenLoopback` — wraps `ErrTailscaleBindRequired`; address is loopback.
- `ErrListenUnspecified` — wraps `ErrTailscaleBindRequired`; address is `0.0.0.0` / `[::]`.
- `ErrListenPublic` — wraps `ErrTailscaleBindRequired`; address is routable / outside CGNAT.
- `ErrListenMalformed` — `netip.ParseAddrPort` failed.
- `ErrTailscaleRequired` — `[network] require_tailscale = false` is rejected per FR-005c.

Path family:
- `ErrPathPrefixInvalid` — path_prefix length out of [6, 32] OR contains a non-URL-safe character.
- `ErrAuditLogEscape` — audit_log path resolves outside state_dir per R-003.
- `ErrStateDirNotFound` — state_dir does not exist.
- `ErrStateDirUnsafe` — state_dir exists but is not a directory.

Crypto family:
- `ErrArgonMemoryTooLow` — `argon_memory_mb < MinArgonMemoryMB (256)`.
- `ErrArgonTimeTooLow` — `argon_time < MinArgonTime (4)`.
- `ErrArgonThreadsTooLow` — `argon_threads < MinArgonThreads (4)`.
- `ErrSupervisorTTLOutOfRange` — `max_supervisor_ttl ≤ jwt_default_ttl` OR `max_supervisor_ttl > DefaultSupervisorTTLMax (24h)`.

Each sentinel has at least one named test in `validate_test.go` that asserts `errors.Is(err, ErrXxx)` for the documented bad-input case.

**Rationale**: The spec's FR-009 demands "typed sentinel errors for every defined rejection category". The chunk contract names eight sentinels and an ellipsis ("..."); the catalogue above is the spec-derived completion of that ellipsis. Wrap relationships (loopback wraps Tailscale-required) let operator dashboards / CI gates match the broad category with one `errors.Is` while detailed test assertions match the specific category.

The `ErrTOMLDecode` umbrella for go-toml/v2 errors is essential because the library's error types are version-specific (the v2 surface has changed across minor versions); wrapping them behind a stable sentinel insulates downstream code from upstream churn.

**Alternatives considered**:
- *One umbrella `ErrInvalidConfig` for everything*: rejected. Spec FR-009 demands "distinct, named" per category — one sentinel cannot satisfy that.
- *Per-field sentinels (`ErrListenAddrLoopback`, `ErrListenAddrUnspecified`, ...) with `health_bind` getting its own duplicates*: rejected. The wrap-relationship approach lets `health_bind` REUSE the same sentinel identity; downstream code can ask "is this an unspecified-address error" without caring which field it came from. The error message is the field-aware part.
- *Use error types (`type LoopbackError struct{...}` with `As`-style extraction)*: deferred. Sentinels are simpler for the catalogue size at hand; if a future chunk needs richer error data, it can promote specific sentinels to types.

---

## R-008 — Fuzz target shape

**Decision**: Add `server_fuzz_test.go` with `FuzzServerTOML(f *testing.F)`:

```go
func FuzzServerTOML(f *testing.F) {
    // Seed corpus — eight files in testdata/fuzz/FuzzServerTOML/.
    seedSeeds(f) // adds seedFromFile() entries for each corpus file.
    f.Fuzz(func(t *testing.T, in []byte) {
        // Use a temp dir as state_dir so path-containment validation has a real anchor.
        dir := t.TempDir()
        path := filepath.Join(dir, "config.toml")
        if err := os.WriteFile(path, in, 0o600); err != nil {
            t.Fatal(err)
        }
        s, err := config.LoadServer(t.Context(), path)
        if err == nil {
            // Successful load — no further assertion. The contract is "no panic".
            // Defensive: spot-check the result has no nil pointers in the struct shape.
            _ = s
            return
        }
        // Every error must be one of our typed sentinels (or wrap one).
        if !isKnownSentinel(err) {
            t.Errorf("LoadServer returned non-sentinel error type %T: %v", err, err)
        }
    })
}
```

Where `isKnownSentinel(err)` iterates the package's sentinel catalogue and returns `true` if `errors.Is(err, candidate)` for any sentinel.

The seed corpus (in `testdata/fuzz/FuzzServerTOML/`) ships eight files:
1. `minimal-valid` — minimum fields set, valid Tailscale IP, valid argon params.
2. `full-default` — every documented field present at its documented default value.
3. `malformed-bytes` — random non-TOML bytes (`\x00\x01\x02...`).
4. `empty` — zero-byte file.
5. `partial-table` — a `[server]` table header with no key/value pairs.
6. `conflicting-types` — `listen_addr = 1234` (int where string expected).
7. `very-long-string` — `listen_addr = "<256 KiB of A>"`.
8. `unicode-edge` — UTF-8 boundary fuzz, including 4-byte sequences.

CI gate (per the implement-phase release-step list): `go test -fuzz=FuzzServerTOML -fuzztime=60s ./internal/config/` runs clean — no panic, no new corpus entries representing crashes (any new entry goes into `testdata/fuzz/FuzzServerTOML/` and would trigger a CI follow-up).

**Rationale**: The chunk contract names FuzzServerTOML and the 60 s gate. Constitution VIII lists "Supervisor config TOML parsing" as fuzz target #5 — same category, same package family. The "no panic, every error typed" invariant is the load-bearing security property: a panic in `LoadServer` would crash `hush serve` at startup, an attacker who compromised the operator's `~/.hush/config.toml` could deny service via crash. The seed corpus accelerates fuzz-coverage convergence — the eight seeds cover all the major decoder paths so 60 s of fuzzing actually exercises the rule engine, not just "find the first parse error".

**Alternatives considered**:
- *Skip the seed corpus, rely on random-byte coverage alone*: rejected. 60 s of random bytes rarely hits the rule-engine paths (most random bytes fail at "is this UTF-8" or "is this TOML syntax"). Seeds are cheap to write and dramatically improve coverage.
- *Run for 5 minutes instead of 60 s*: deferred. The chunk contract says 60 s; CI cost is a real constraint. A future hardening pass can lengthen if a defect class emerges.
- *Fuzz `Validate` separately from `LoadServer`*: rejected. `LoadServer` is the entry-point operators see; fuzzing it covers the full pipeline (decode + materialize + validate). Fuzzing `Validate` separately would require synthesising decoded structs, which is a less faithful representation of the production attack surface.

---

## R-009 — `require_tailscale = false` rejection (FR-005c)

**Decision**: The validator runs unconditionally before any other network-section check. If `s.Network.RequireTailscale == false`, return `ErrTailscaleRequired`. The `serverDecoded` shape stores this as `*bool`; the materializer copies the dereferenced value to `Server.Network.RequireTailscale`, applying the default `true` when nil. Validate then checks the materialized value.

**Rationale**: Spec clarification 3 (Session 2026-04-28) chose Option A: "Yes — reject with a distinct typed error. The flag must be `true` (or absent — defaults to true) in v0.1.0." The validation order matters: this check runs BEFORE `listen_addr` validation, because if Tailscale is disabled the listen-addr rules are nonsensical. Putting it first gives the operator the clearest signal.

The flag is documented in `docs/CONFIG-SCHEMA.md` `[network]` section and the CONFIG-SCHEMA validation-rules-summary lists `must remain true in v0.1.0`. SDD-06 enforces that documented constraint.

**Alternatives considered**:
- *Treat `require_tailscale = false` as a soft warning and rely on `listen_addr` validation alone*: rejected. The flag exists in the schema as a load-time gate — silently ignoring it would defeat its documentary purpose. A soft warning is also softer than the spec mandates.
- *Make the flag implicitly write-locked (`require_tailscale` not present in the struct, ignored if set)*: rejected. The schema documents the field; removing it from the struct creates a divergence between docs and code.

---

## R-010 — `path_prefix` charset and length enforcement (FR-005d)

**Decision**: Length must be in `[MinPathPrefixLen (6), MaxPathPrefixLen (32)]`. Charset is enforced via a compiled regex `var pathPrefixRegex = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)`. The validator checks length first (faster; no regex compile/match needed for the rejection-by-length case), then charset. Either failure → `ErrPathPrefixInvalid`.

`pathPrefixRegex` is lazily initialised via `sync.Once` and `var pathPrefixOnce sync.Once`; the regex variable is exported as private (`pathPrefixRegex`, lowercase) — the locked API does not name this variable. The `MustCompile` call is safe (literal compile-time pattern) and inside the `Once.Do` callback for Constitution-IX-friendly initialisation (no `init()` function).

**Rationale**: Spec clarification 5 (Session 2026-04-28) chose Option A: enforce length 6-32 and URL-safe charset at load time. The charset `[A-Za-z0-9_-]` matches the URL-safe-base64 alphabet minus `+` and `/` (which would conflict with HTTP path semantics). The regex is the simplest, most readable encoding of this charset.

The lazy `sync.Once` init avoids `init()` (Constitution IX) while still amortising the regex compile cost across all calls — same pattern as `internal/logging`'s `RedactPatterns` (SDD-05 R-005).

**Alternatives considered**:
- *Hand-coded byte-by-byte loop instead of regex*: rejected. The regex is the readable expression of the URL-safe charset; the cost is one compile + one regex match per `LoadServer` call, sub-microsecond.
- *Allow longer than 32 characters for "future expansion"*: rejected. The CONFIG-SCHEMA documents 6-32; longer prefixes leak entropy budget for no benefit (32 chars of base64 is 192 bits, far above any realistic guessing threshold).

---

## R-011 — `max_supervisor_ttl` bounds (TTL-out-of-range)

**Decision**: Validate enforces TWO conditions on `max_supervisor_ttl`:
1. `s.Crypto.MaxSupervisorTTL > s.Crypto.JWTDefaultTTL` (comparative — per `docs/CONFIG-SCHEMA.md` "must be greater than `jwt_default_ttl`").
2. `s.Crypto.MaxSupervisorTTL <= DefaultSupervisorTTLMax (24h)` (absolute v0.1.0 cap — per `docs/CONFIG-SCHEMA.md` "must not exceed 24h in v0.1.0").

Either violation → `ErrSupervisorTTLOutOfRange`. The error message names which condition was violated.

`DefaultSupervisorTTLMax` is `24 * time.Hour` (the v0.1.0 cap). There is no `Min` constant because the floor is comparative (against `jwt_default_ttl`, which itself can be operator-configured).

**Rationale**: The two-condition test exactly mirrors `docs/CONFIG-SCHEMA.md` `[crypto] max_supervisor_ttl` rules section. Encoding the cap as an exported constant lets tests assert against the same value the validator uses, with no drift risk.

**Alternatives considered**:
- *Add a `MinSupervisorTTL` constant (e.g., 1h) as an absolute floor*: rejected. The CONFIG-SCHEMA documents only the comparative floor; adding an absolute one would over-specify and could surprise an operator who wants a 30-minute supervisor for a flaky daemon.
- *Two separate sentinel errors (`ErrSupervisorTTLBelowJWTTTL`, `ErrSupervisorTTLAboveCap`)*: deferred. The single sentinel + descriptive message is simpler and matches how the spec lists the category (singular: "TTL-out-of-range"). A future split is non-breaking.

---

## R-012 — `health_bind` validation parity with `listen_addr` (FR-003a)

**Decision**: When `s.Network.HealthBind` is non-empty (explicitly set), it is validated by the same `validateTailscaleAddrPort` helper that validates `listen_addr`. Same rejection categories, same sentinel identities (with field-name in the wrapping message). When `s.Network.HealthBind` is empty in the decoded form, the materializer copies `s.Server.ListenAddr` into it; since `listen_addr` has already been validated, no second validation pass is needed for the inherited value.

**Rationale**: Spec clarification 4 (Session 2026-04-28) chose Option A: "Yes — apply identical Tailscale CGNAT rules to `health_bind` when explicitly set." Sharing the validator function avoids the "two rules drift apart" failure mode. Inheriting from `listen_addr` when absent matches `docs/CONFIG-SCHEMA.md`'s `[network] health_bind` documentation: "default: same as `listen_addr`".

**Alternatives considered**:
- *Validate `health_bind` even when inherited from `listen_addr`*: rejected as redundant. Defensive belt-and-suspenders pays no dividend when the source has already been validated.
- *Allow `health_bind` outside CGNAT for "split-listener future flexibility"*: rejected. Spec clarification 4 was explicit. Future flexibility is a v0.2 concern, if ever.

---

## R-013 — `pelletier/go-toml/v2` dependency justification

**Decision**: Add `github.com/pelletier/go-toml/v2` as a direct dependency in `go.mod` for the SDD-06 implement commit. This is the package's only new direct dependency.

**Rationale**: Constitution XI requires every new direct dependency to satisfy: (a) maintainer activity, (b) supply-chain provenance, (c) transitive dependency footprint, (d) why no stdlib option suffices.

- *(a)* **Maintainer activity**: Maintained by Thomas Pelletier (https://github.com/pelletier). Active commit cadence (multiple commits per month historically); v2 line is feature-stable since 2023; security advisories are addressed within days when reported. 4k+ GitHub stars; downloaded 100M+ times via `proxy.golang.org`.
- *(b)* **Supply-chain provenance**: Hosted on GitHub at `github.com/pelletier/go-toml`. Module path `github.com/pelletier/go-toml/v2`. Distributed via `proxy.golang.org` with a Sigstore transparency-log entry. Module is signed via the Go transparency log; `go.sum` checksums are reproducible.
- *(c)* **Transitive dependency footprint**: ZERO non-stdlib transitive dependencies for the runtime decoder path. The module's `go.mod` lists `github.com/google/go-cmp` and `github.com/stretchr/testify` only as test-only deps (`indirect` to the consumer). The runtime surface depends on `unicode/utf8`, `strings`, `time`, `reflect`, `bufio`, `errors`, `io`, `fmt` — all stdlib.
- *(d)* **Stdlib gap**: The Go standard library does not include a TOML decoder. There is an open `encoding/toml` proposal but it has not landed in Go 1.26 and is unlikely to land before v0.1.0 ships. The CONFIG-SCHEMA (Phase 0) commits the project to TOML; the decoder is a hard dependency.

The trusted-sources hierarchy in `.github/tech-conventions/dependency-management.md` lists the wider Go ecosystem as tier 4 — used "only when stdlib, sigil baseline, and bsv-blockchain options are insufficient". For TOML decoding, all three earlier tiers are insufficient (none includes a TOML decoder); pelletier/go-toml/v2 is the canonical Go answer.

The implement-commit PR description will repeat this justification verbatim per Constitution XI's "every NEW direct dependency requires a written justification" clause; the Complexity Tracking row in plan.md is the constitutional gate signature.

**Alternatives considered**:
- *`BurntSushi/toml`*: rejected. The chunk contract names pelletier/v2 explicitly; switching would require a constitutional amendment. Behaviourally similar but `BurntSushi/toml`'s API is older (the strict-decode mode is `Decoder.Strict(true)`, not `DisallowUnknownFields`); the chunk contract's wire-name-level lock points to pelletier.
- *Hand-rolled TOML subset*: rejected — see plan.md Complexity Tracking row 1. Parser bugs in a security-critical config loader are a worse trade than adopting a maintained, fuzzed-upstream decoder.
- *Switch the file format to JSON or YAML*: rejected. JSON has no comment syntax (operator-hostile for a config they author by hand); YAML has equally large or larger surface (`gopkg.in/yaml.v3`) with a worse track record for surprises (the famous Norway problem, indentation pitfalls). TOML is the right format; pelletier is the right decoder.

---

## R-014 — `errors.Join` for multi-violation reporting

**Decision**: When the `materialize` step or the `Validate` rule engine encounters multiple violations in a single config, return `errors.Join(err1, err2, ...)`. The joined error satisfies `errors.Is(err, ErrXxx)` for every joined sentinel — Go 1.20+ wires this up automatically.

The chunk contract names sentinel-error returns; `errors.Join` is sentinel-compatible (it's the standard library's official multi-error type). Tests can assert `errors.Is(err, ErrMissingRequiredField)` AND `errors.Is(err, ErrAuditLogEscape)` on the same error value when both apply.

**Rationale**: Spec SC-004 ("operator who introduces a single typo or schema violation receives a single, named error identifying the offending field within the same load attempt — no trial-and-error"). For *multiple* violations, trial-and-error is exactly what stop-on-first-error produces. `errors.Join` is the operator-friendly answer.

The decode phase is "fail fast" — go-toml/v2 returns the first decode error; the loader does not attempt to recover and continue decoding. The materialize and validate phases are "fail full" — every detectable error is collected and joined.

**Alternatives considered**:
- *Stop on first error in all phases*: rejected. Fewer round-trips for the operator is worth the small implementation cost of accumulating errors in a slice.
- *Custom `MultiError` type instead of `errors.Join`*: rejected. `errors.Join` is the stdlib-blessed answer since Go 1.20; using a custom type creates surface drift for no behavioural gain.

---

## R-015 — Test corpus discipline

**Decision**: Test fixtures live in `internal/config/testdata/`. Subdirectories:
- `testdata/valid/` — minimal-valid, full-default, full-maximal — happy-path samples.
- `testdata/invalid/` — one file per sentinel: `unknown-field.toml`, `loopback.toml`, `unspecified.toml`, `public.toml`, `malformed.toml`, `tailscale-required.toml`, `path-prefix-too-short.toml`, `path-prefix-bad-charset.toml`, `audit-log-escape.toml`, `state-dir-missing.toml`, `argon-memory-low.toml`, `argon-time-low.toml`, `argon-threads-low.toml`, `supervisor-ttl-below-jwt.toml`, `supervisor-ttl-above-cap.toml`, `bad-duration.toml`.
- `testdata/fuzz/FuzzServerTOML/` — eight seed files per R-008.

Each file is a real TOML document; tests load them via `config.LoadServer(ctx, "testdata/.../foo.toml")` and assert the expected sentinel via `errors.Is`. Tests for `state-dir-missing` and `audit-log-escape` use `t.TempDir()` to construct ephemeral state-dir paths and pass them via test-only string substitution (the TOML file uses a placeholder like `__STATE_DIR__` that the test rewrites before calling LoadServer).

**Rationale**: Real TOML files in `testdata/` are auditable in code review (you can read the file and see the input the test exercises) and enable manual repro by an operator who wants to understand a failure (`hush config validate testdata/invalid/audit-log-escape.toml` once such a subcommand exists). The placeholder substitution for path-bearing fixtures avoids hardcoding absolute paths that would not exist on a CI runner.

**Alternatives considered**:
- *Inline TOML strings in the test source*: rejected. Multi-line raw-string TOML is hard to read and harder to diff in code review.
- *Generate fixtures dynamically per test run*: rejected. Determinism + auditability favour committed fixtures.
