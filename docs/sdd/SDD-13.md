# SDD-13 — Server `/s`, `/revoke`, `/hz` handlers + audit log

**Phase:** 3
**Package:** `internal/server` (handlers) + `internal/audit` (new)
**Files:** `internal/server/{secret_handler.go, revoke_handler.go, health_handler.go, *_test.go}`; `internal/audit/{chain.go, writer.go, discord_mirror.go, *_test.go}`
**Branch:** `013-server-handlers-and-audit` (created by the `before_specify` git hook)
**Blocked by:** SDD-09, SDD-12
**Blocks:** SDD-25, SDD-28
**Primary AC:** AC-1, AC-2, AC-4, AC-7
**Coverage target:** server handlers 95%; audit chain 100%

**Behaviour contracts (MUST):**
- `/s/<name>` handler validates token via `token.Validate` (with scope=name and IP=remote); decrements `max_uses` for interactive
- `/s` response body is the raw ECIES envelope (`Content-Type: application/octet-stream`)
- `/revoke` handler accepts a signed JSON body `{jti, timestamp, nonce, signature}`; verify with same client key registry as `/claim`
- `/hz` returns `{status:"ok", uptime, secrets_count, active_tokens, discord_connected}`; reachable WITHOUT JWT (G3 trust: Tailscale only)
- Audit `Writer`: single goroutine, buffered channel, every event hash-chained + signed; canonicalise data via SDD-08's `CanonicalJSON` before hashing
- Audit `DiscordMirror` is best-effort: log WARN on mirror failure, never block append

**Anti-contracts (MUST NOT):**
- Return decrypted secret in any error path
- Place secret VALUE in audit data alongside its name (only the name)
- Drop audit events under backpressure (block instead)

**Tests required:**
- Unit: `TestSecret_HappyPath_ECIESPayload`, `TestSecret_ExpiredJWT_401`, `TestSecret_OutOfScope_403`, `TestSecret_WrongIP_401`, `TestSecret_ExhaustedInteractive_401`, `TestSecret_SupervisorIgnoresMaxUses`, `TestRevoke_HappyPath`, `TestRevoke_BadSignature_403`, `TestHealth_NoAuth_OK`, `TestAuditChain_HashLinkContiguous`, `TestAuditChain_SignatureValid`, `TestAuditChain_BreakDetectedOnTamper`
- Sentinel-leak: `TestSecret_ErrorBodyNoSentinel`, `TestAudit_RecordNoSecretValue`
- Race: audit writer concurrent writes — exactly N records with monotonic seq

**Constitutional principles in scope:** III (Layer 6 audit chain), IV (token enforcement), VIII, X (no values in errors / audit data)

**Exported API to lock in PACKAGE-MAP.md (this chunk):**
- (server: HTTP routes only — note in PACKAGE-MAP entry "GET /s/<name>, POST /revoke, GET /hz — see docs/API.md (locked at SDD-13)")
- audit (new package):
  - `type Event struct { Seq uint64; Time time.Time; Action string; Data map[string]any; PrevHash, Hash, Signature string }`
  - `type Writer interface { Append(ctx context.Context, action string, data map[string]any) error; Run(ctx context.Context) error }`
  - `func NewWriter(ctx context.Context, path string, signKey *ecdsa.PrivateKey, mirror *DiscordMirror, logger *slog.Logger) (Writer, error)`
  - `type DiscordMirror struct { ... }`
  - `var ErrAuditChainBroken`

---

## How to run this chunk

Run **5 separate Claude Code sessions**, one per prompt below. All
commits for this chunk are deferred to a single combined commit at the
end of Prompt 5 (Implement). Do not commit between phases.

This chunk introduces TWO packages (handlers in internal/server,
audit in internal/audit/new). Treat both as one cohesive deliverable
— the handlers depend on the audit writer for every outcome.

---

## Prompt 1 — Specify  (fresh session)

```
You are running the SPECIFY phase of SDD-13 (server /s, /revoke, /hz
handlers + audit log) of the hush project.

Read first (in order):
- /Users/mrz/projects/hush/.specify/memory/constitution.md  (Principles III Layer 6, IV, VIII, X)
- /Users/mrz/projects/hush/docs/API.md  (GET /s, POST /revoke, GET /hz — full)
- /Users/mrz/projects/hush/docs/SPEC.md  (FR-9, FR-14, FR-17, AC-1, AC-2, AC-4, AC-7)
- /Users/mrz/projects/hush/docs/SECURITY.md  (Layer 6 audit chain — hash-chained signed events)
- /Users/mrz/projects/hush/docs/CONFIG-SCHEMA.md  ([server] discord_audit_channel_id, audit_log)
- /Users/mrz/projects/hush/docs/LIFECYCLE-SCENARIOS.md  (Scenario 7 secret retrieval; Scenario 13 revoke)
- /Users/mrz/projects/hush/docs/AC-MATRIX.md  (current AC-1/2/4/7 row state)
- /Users/mrz/projects/hush/docs/sdd/SDD-13.md  (the full chunk contract)

About this chunk (one-paragraph intent, for the spec's overview):
This chunk delivers the three remaining server endpoints (/s for
secret retrieval, /revoke for explicit JTI revocation, /hz for
health) AND the tamper-evident audit log every outcome flows
through. The audit log is a hash-chained, signed sequence of events
written to disk and optionally mirrored to a Discord channel — its
chain integrity is the cornerstone of post-incident forensics.

The spec MUST encode these acceptance-level (WHAT) requirements.
Override any /speckit-specify "informed guess" that would soften
them:

- /s/<name>: requires a valid JWT bound to that secret name, the
  caller's IP, and (for INTERACTIVE) a remaining use-count.
  Response body is the opaque ECIES envelope; never the
  plaintext.
- /revoke: requires a signed body proving the caller owns the
  client key the JWT was issued to; revocation is permanent
  for the JWT's lifetime.
- /hz: reachable without a JWT (Tailscale boundary is the auth);
  reports operational state only, never any secret name OR token
  contents.
- The audit log is a hash-chained, signed sequence: each event
  contains the previous event's hash and is itself signed by the
  audit-signing key. Tampering with any past event detectably
  breaks the chain.
- Audit data records the secret NAME for retrieval events but
  NEVER the secret VALUE.
- The audit writer never drops events on backpressure; it blocks
  the producing handler instead.
- Discord mirroring of audit events is best-effort — failures
  log a WARN but never block the audit-to-disk path.

The spec MUST NOT encode HOW (no library names, no specific
hashing or signing algorithms beyond what the constitution
requires). Those are plan-phase.

Acceptance criteria: AC-1, AC-2 (vault round-trip via /s), AC-4
(token enforcement + revoke), AC-7 (ECIES on the wire).

Action — run exactly one command:
  /speckit-specify "GET /s/<name> (token-gated, scope=name + client IP enforcement, INTERACTIVE consumes uses, response body is ECIES envelope), POST /revoke (signed body, permanent revocation), GET /hz (no auth, operational state only); plus internal/audit: hash-chained + signed event log, never drops events on backpressure, optional best-effort Discord mirror"

The before_specify hook will create branch 013-server-handlers-and-audit.

If /speckit-specify produces [NEEDS CLARIFICATION] markers, check
each against the chunk contract / constitution. Otherwise leave
the marker — /speckit-clarify will handle it next session.

```

---

## Prompt 2 — Clarify  (fresh session)

```
You are running the CLARIFY phase of SDD-13 of the hush project.

Read /Users/mrz/projects/hush/docs/sdd/SDD-13.md.

Run: /speckit-clarify

```

---

## Prompt 3 — Plan  (fresh session)

```
You are running the PLAN phase of SDD-13 (server handlers + audit
log) of the hush project.

Read first (in order):
- /Users/mrz/projects/hush/.specify/memory/constitution.md  (full file — /speckit-plan runs a Constitution Check; III/IV/VIII/X load-bearing)
- /Users/mrz/projects/hush/docs/API.md  (GET /s, POST /revoke, GET /hz — full)
- /Users/mrz/projects/hush/docs/SPEC.md  (FR-9, FR-14, FR-17)
- /Users/mrz/projects/hush/docs/SECURITY.md  (Layer 6 audit chain — entry shape mandatory)
- /Users/mrz/projects/hush/docs/PACKAGE-MAP.md  (internal/server + new internal/audit)
- /Users/mrz/projects/hush/docs/sdd/SDD-13.md  (the full chunk contract)

The plan MUST honour every item below. /speckit-plan runs a
Constitution Check — if it fires, fix the plan, do NOT bypass.

Scope:
- Packages: internal/server (handlers) + internal/audit (NEW)
- Files:
    internal/server/secret_handler.go
    internal/server/revoke_handler.go
    internal/server/health_handler.go
    internal/server/secret_handler_test.go
    internal/server/revoke_handler_test.go
    internal/server/health_handler_test.go
    internal/audit/chain.go (Event, hash-chain, sign helper)
    internal/audit/writer.go (Writer interface + impl with single goroutine)
    internal/audit/discord_mirror.go
    internal/audit/chain_test.go
    internal/audit/writer_test.go
    internal/audit/discord_mirror_test.go
- Exported API (audit):
    type Event struct { Seq uint64; Time time.Time; Action string; Data map[string]any; PrevHash, Hash, Signature string }
    type Writer interface { Append(ctx context.Context, action string, data map[string]any) error; Run(ctx context.Context) error }
    func NewWriter(ctx context.Context, path string, signKey *ecdsa.PrivateKey, mirror *DiscordMirror, logger *slog.Logger) (Writer, error)
    type DiscordMirror struct { ... }
    func NewDiscordMirror(channelID string, session *discordgo.Session) *DiscordMirror
    var ErrAuditChainBroken

Implementation contract (HOW — locked):
- /s/<name>:
    1. Extract Bearer JWT from Authorization header.
    2. token.Validate(ctx, encoded, verifyKey, store, remoteIP, name).
    3. On success: vault.Store.Get(name) → ECIES.Encrypt against
       claims.EphemeralPubKey → write octet-stream body.
    4. Always Audit.Append the outcome (decision, scope, request_id).
- /revoke:
    1. Parse body {jti, timestamp, nonce, signature}.
    2. CanonicalJSON+Verify against the same client key registry
       used by /claim (lookup by jti → original signer).
    3. token.Store.Revoke(jti).
    4. Audit.Append.
- /hz: no auth; response is JSON with the documented fields;
  uptime sourced from the Server start time.
- Audit Writer:
    - Single goroutine started by Run(ctx). Buffered chan of events.
    - Append blocks if buffer full (backpressure invariant).
    - For each event: assign Seq from atomic counter; compute
      Hash = SHA-256(prevHash || canonical(Event-without-Hash-or-Signature));
      Signature = ECDSA(signKey, Hash).
    - Persist to <audit_log> (append-only, mode 0600).
    - If DiscordMirror present: send asynchronously, log WARN
      on failure, NEVER block the disk write.
- TestAuditChain_BreakDetectedOnTamper: load the file, mutate one
  event's Data, verify the chain re-validation surfaces
  ErrAuditChainBroken at the first tampered event.

Coverage target: 95% for handlers; 100% for the audit chain
(Constitution VIII security-critical).
Constitutional principles in scope: III (Layer 6), IV, VIII, X.

Run: /speckit-plan

```

---

## Prompt 4 — Tasks  (fresh session)

```
You are running the TASKS phase of SDD-13 of the hush project.

Read /Users/mrz/projects/hush/docs/sdd/SDD-13.md.

Run:
  /speckit-tasks "TDD-mandatory per Constitution VIII: include a test-writing task for every behaviour contract BEFORE the implementation task. Coverage target: 95% for handlers, 100% for internal/audit. Tests required (handlers): TestSecret_HappyPath_ECIESPayload, TestSecret_ExpiredJWT_401, TestSecret_OutOfScope_403, TestSecret_WrongIP_401, TestSecret_ExhaustedInteractive_401, TestSecret_SupervisorIgnoresMaxUses, TestRevoke_HappyPath, TestRevoke_BadSignature_403, TestRevoke_UnknownJTI_404, TestHealth_NoAuth_OK, TestHealth_DiscordConnectedFlag. Tests required (audit): TestAuditChain_HashLinkContiguous, TestAuditChain_SignatureValid, TestAuditChain_BreakDetectedOnTamper, TestAuditWriter_BlocksOnBackpressure, TestAuditWriter_ConcurrentAppendMonotonicSeq (race-clean, exactly N records), TestDiscordMirror_FailureLogsWarnNoBlock. Sentinel-leak: TestSecret_ErrorBodyNoSentinel and TestAudit_RecordNoSecretValue (both use SECRET_SHOULD_NEVER_APPEAR_13). Final phase MUST include magex format:fix, magex lint, magex test:race."

```

---

## Prompt 5 — Implement  (fresh session)

```
You are running the IMPLEMENT phase of SDD-13 of the hush project.

Read /Users/mrz/projects/hush/docs/sdd/SDD-13.md.

Run: /speckit-implement

After /speckit-implement completes, do these steps from repo root:

1. Gates (all must pass clean):
     magex format:fix && magex lint && magex test:race
2. Verify coverage:
     go test -cover ./internal/server/         (≥ 95% on handlers)
     go test -cover ./internal/audit/          (= 100%)
3. Confirm both sentinel-leak tests passed:
   TestSecret_ErrorBodyNoSentinel and TestAudit_RecordNoSecretValue
   — SECRET_SHOULD_NEVER_APPEAR_13 absent everywhere.
4. Confirm TestAuditChain_BreakDetectedOnTamper detects tampering
   at the first mutated event.
5. Append "Exported API — locked at SDD-13" section to
   docs/PACKAGE-MAP.md:
     - Under internal/server: note "GET /s/<name>, POST /revoke,
       GET /hz handlers — see docs/API.md".
     - NEW entry for internal/audit listing the locked API
       (Event, Writer, NewWriter, DiscordMirror, NewDiscordMirror,
       ErrAuditChainBroken).
6. Update docs/AC-MATRIX.md AC-1, AC-2, AC-4, AC-7 rows with the
   new test file paths.
7. Mark SDD-13 status `done` in docs/SDD-PLAYBOOK.md.

Make one combined commit:
  git add internal/server/ internal/audit/ docs/PACKAGE-MAP.md \
          docs/AC-MATRIX.md docs/SDD-PLAYBOOK.md \
          specs/<feature-dir>/tasks.md
  git commit -m "feat(server,audit): /s + /revoke + /hz + hash-chained signed audit log (SDD-13)"

Final message: confirm gates passed, race-clean, handler coverage
≥ 95%, audit coverage = 100%, ECIES end-to-end verified,
audit-chain tamper test breaks correctly, AC-1/2/4/7 rows updated,
SDD-PLAYBOOK updated, and the combined commit created.
```
