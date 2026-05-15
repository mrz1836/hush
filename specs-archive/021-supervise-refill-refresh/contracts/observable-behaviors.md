# Observable Behaviors — SDD-21 (`internal/supervise` refill + refresh + grace)

This document is the **black-box** contract for the chunk's three
exported types. Every entry maps to at least one test in the
[quickstart.md](../quickstart.md) test plan and to at least one
spec FR. Reviewers diff this against the implemented behavior — any
behavior not listed here is implementation detail and may change
without breaking the contract.

---

## Refiller — observable behaviors

### B-RR-1 — Silent refill on clean child exit (FR-021-1, Story 1)
- **Pre:** `Store.Snapshot().Token` non-nil; HTTP 200 + valid ECIES envelope per scope.
- **Action:** `Refill(ctx, []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY"})`.
- **Observable:**
  - One GET per scope to `<server>/s/<name>` with `Authorization: Bearer <jwt>` header.
  - Two `Grace.Set(name, sb)` calls in any order.
  - Returns `nil`.
  - No `string(...)` of any decrypted byte slice (assertable via marker-byte test).
  - No Discord-bound side effect (operational logger MAY emit one INFO line; no Approver-equivalent dependency invoked).

### B-RR-2 — 401 with `unknown_jti` returns `ErrJTIUnknown` (FR-021-3, Story 1 Scenario 2)
- **Pre:** Server returns HTTP 401 with body `{"error":"unknown_jti"}` for any one scope.
- **Action:** `Refill(ctx, []string{"A", "B", "C"})`.
- **Observable:**
  - Refill stops at the failing scope (no further GETs).
  - All previously-successfully-decrypted `*SecureBytes` from this call are destroyed before return.
  - Returns wrapped error satisfying `errors.Is(err, ErrJTIUnknown)`.
  - No `Grace.Set` calls.

### B-RR-3 — Network error returns wrapped error, NOT `ErrJTIUnknown` (FR-021-4, Story 1 Scenario 3)
- **Pre:** `client.Do` returns a `*net.OpError` for any one scope.
- **Action:** `Refill(ctx, []string{"A", "B"})`.
- **Observable:**
  - Refill stops at the failing scope.
  - Decrypted bytes from prior scopes destroyed before return.
  - Returns non-nil error.
  - `errors.Is(err, ErrJTIUnknown) == false`.
  - `errors.As(err, &netErr)` where `netErr *net.OpError` succeeds.

### B-RR-4 — Atomic destruction on partial failure (FR-021-5)
- **Pre:** Three scopes; scope 1 succeeds (200), scope 2 succeeds (200), scope 3 fails (any reason).
- **Action:** `Refill(ctx, scopes)`.
- **Observable:**
  - Scopes 1 + 2 each produce a `*SecureBytes` that is destroyed (assertable via `errors.Is(sb1.Use(...), ErrDestroyed)` after Refill returns).
  - No `Grace.Set` call for any of the three scopes.

### B-RR-5 — Bearer token never leaks to logs (Constitution X)
- **Pre:** `Store.Token` holds bytes `0xCAFEBABE...`; logger captures into a `bytes.Buffer`.
- **Action:** Any `Refill(ctx, ...)` call.
- **Observable:** Log buffer does NOT contain `0xCAFEBABE` byte sequence; SecureBytes' `LogValue()` returns `"[redacted]"` so any log site rendering the JWT prints `[redacted]`.

### B-RR-6 — Decrypted bytes never become a Go string (Constitution X, SC-021-8)
- **Pre:** Server returns ECIES envelope whose decrypted plaintext is the marker `b"HUSH-MARKER-21-PLAINTEXT"`.
- **Action:** `Refill(ctx, []string{"S1"})`.
- **Observable:**
  - `Grace.Set("S1", sb)` is called; `sb.Use(func(b []byte) {})` reveals the marker bytes.
  - Logger output bytes contain neither the marker nor any substring of it.
  - Inspecting `Refill`'s implementation under `gosec` / `unconvert` lint shows no `string(decryptedBytes)` site.

### B-RR-7 — Context cancellation is honored
- **Pre:** ctx cancelled before Refill is called; or cancelled mid-call.
- **Action:** `Refill(ctx, ...)`.
- **Observable:** Returns wrapped `ctx.Err()`; in-flight HTTP request aborted via `Request.Context()`; any decrypted bytes destroyed.

---

## Refresher — observable behaviors

### B-RF-1 — Fires exactly once inside the configured window (FR-021-7, Story 2 Scenario 1)
- **Pre:** `window = "09:00-10:00"`; clock set to start at 08:55; advance to 09:05; `ttl > 24h`.
- **Action:** `go Run(ctx)`; advance clock; observe.
- **Observable:** Refill callback invoked exactly once. Subsequent ticks at 09:30, 09:55, 10:00 do not re-invoke.

### B-RF-2 — T-30 fallback fires when window has passed (FR-021-8, Story 2 Scenario 3)
- **Pre:** `window = "09:00-10:00"`; clock starts at 11:00 (window passed today); `bornAt + ttl = 11:25` → 25min remaining < 30min.
- **Action:** `go Run(ctx)`.
- **Observable:** Refill callback invoked exactly once on tick. `t30Fired = true` after fire — second `Run` of a fresh `Refresher` would re-fire if conditions repeat (per-session flag).

### B-RF-3 — No double-fire within the same window (FR-021-10)
- **Pre:** Clock at 09:30 (in-window); `Refresher.Run` already fired earlier at 09:05.
- **Action:** Tick re-evaluates.
- **Observable:** Refill NOT invoked again. `lastFiredDay` equals today.

### B-RF-4 — Process restart inside window fires on init (FR-021-10 second sentence, Story 2 Scenario 2)
- **Pre:** Fresh `*Refresher`; `lastFiredDay` zero-valued; clock at 09:30 (in-window); `ttl > 24h`.
- **Action:** `Run(ctx)` starts.
- **Observable:** Refill callback invoked once on first tick (well before 10:00). `lastFiredDay = today`.

### B-RF-5 — Run exits cleanly on ctx cancel (FR-021-9, SC-021-9)
- **Pre:** `Run` blocking inside `select`.
- **Action:** Cancel parent ctx.
- **Observable:** Run returns `ctx.Err()` within one tick interval; goroutine count returns to baseline; `-race` flag clean.

### B-RF-6 — Backwards clock step does NOT cause double-fire (FR-021-11)
- **Pre:** Clock at 09:30, fire occurred. Step clock back to 09:15.
- **Action:** Wait for next tick.
- **Observable:** Refill NOT invoked. `lastFiredDay` already set to today; tick path falls through.

### B-RF-7 — Rate-limited refill error counts as issued (FR-021-11a, Clarification 4)
- **Pre:** `refill` callback returns a non-nil error (e.g. `discord.ErrRateLimited`).
- **Action:** Tick fires.
- **Observable:**
  - Refill callback invoked exactly once.
  - `lastFiredDay` advanced.
  - Logger emits one WARN line naming the error class.
  - Subsequent in-window ticks do NOT re-invoke.
  - Run does NOT propagate the error to its caller.

### B-RF-8 — Single-use Run (defensive)
- **Pre:** `Refresher.Run(ctx1)` already returned.
- **Action:** `Refresher.Run(ctx2)`.
- **Observable:** Returns sentinel error immediately; no goroutine spawned, no callback invoked.

### B-RF-9 — Window-crosses-midnight is treated as one interval (Edge Case)
- **Pre:** `window = "23:00-01:00"`; clock at 23:30.
- **Action:** Tick.
- **Observable:** Fire occurs (in-window); `lastFiredDay` set to today's date.

---

## Grace — observable behaviors

### B-GR-1 — Cache hit before TTL elapse (FR-021-12, Story 3 Scenario 1)
- **Pre:** `NewGrace(60*time.Minute, true)`; clock at T0; `Set("API_KEY", sb)`; clock at T0 + 30min.
- **Action:** `Get("API_KEY")`.
- **Observable:** Returns `(sb, true)`; sb.Use returns the expected bytes; map size 1.

### B-GR-2 — Cache miss after TTL elapse, lazy destroy (FR-021-13, Story 3 Scenario 2)
- **Pre:** `Set("API_KEY", sb)` at T0; clock at T0 + 61min.
- **Action:** `Get("API_KEY")`.
- **Observable:** Returns `(nil, false)`; the previously-stored sb is destroyed (next `sb.Use` returns `ErrDestroyed`); map size 0.

### B-GR-3 — Disabled cache stores nothing (FR-021-14, Story 3 Scenario 3)
- **Pre:** `NewGrace(60*time.Minute, false)`.
- **Action:** `Set("X", sb)` then `Get("X")`.
- **Observable:** Get returns `(nil, false)`; sb is NOT destroyed by Set (caller retains ownership); map size 0.

### B-GR-4 — Zero-window equivalent to disabled (Edge Case "Grace TTL configured as 0")
- **Pre:** `NewGrace(0, true)`.
- **Action:** `Set("X", sb)`; `Get("X")`.
- **Observable:** Get returns `(nil, false)`; sb is NOT destroyed by Set; map size 0.

### B-GR-5 — TTL hard-capped at 4 hours (FR-021-12, Story 3 Scenario 4, SC-021-5)
- **Pre:** `NewGrace(8*time.Hour, true)`; clock at T0; `Set("X", sb)`.
- **Action:** Inspect entry's expires field via test seam OR advance clock to T0 + 4h + 1ns and Get.
- **Observable:** Get returns `(nil, false)`; sb destroyed. Effective TTL was exactly 4 hours.

### B-GR-6 — Set-overwrite destroys prior entry (FR-021-13)
- **Pre:** `Set("X", sb1)`; `sb1` is alive.
- **Action:** `Set("X", sb2)`.
- **Observable:** `sb1.Use(...)` returns `ErrDestroyed`; subsequent `Get("X")` returns `(sb2, true)`.

### B-GR-7 — Evict destroys + removes (FR-021-16, Clarification 5)
- **Pre:** `Set("X", sb)`.
- **Action:** `Evict("X")`.
- **Observable:** sb destroyed; `Get("X")` returns `(nil, false)`; map size 0.

### B-GR-8 — Evict on absent name is silent no-op (FR-021-16)
- **Pre:** Empty cache.
- **Action:** `Evict("nonexistent")`.
- **Observable:** No panic, no error path; cache unchanged.

### B-GR-9 — Cached values never become a Go string (FR-021-15, Constitution X)
- **Pre:** `Set("X", sb)` where `sb` wraps marker bytes `b"HUSH-MARKER-21-CACHED"`.
- **Action:** Render `Grace` via `slog.Info("dump", "grace", g)` or any logger path that touches the cache.
- **Observable:** Output buffer does NOT contain marker bytes. (Grace itself does not implement `LogValue` because it never logs; `*SecureBytes` redacts via `LogValue`.)

### B-GR-10 — Concurrent Get/Set/Evict are race-clean (SC-021-9)
- **Pre:** N=100 goroutines each calling some interleaving of Set, Get, Evict on the same key.
- **Action:** Run under `-race`.
- **Observable:** No race detected; final state is consistent (entry either present-and-alive, or absent-and-destroyed); no double-Destroy panic.

---

## Sentinel-error semantics

### B-SE-1 — `errors.Is(refillErr, ErrJTIUnknown)` matches the JTI path (FR-021-3)
- Refill must wrap `ErrJTIUnknown` via `fmt.Errorf("...: %w", ErrJTIUnknown)`; tests assert `errors.Is`.

### B-SE-2 — `ErrBootTimeout` is exported but never produced here (R-010, FR-021-20)
- The sentinel is declared in this chunk; no `Refill` / `Run` / `Get` / `Set` / `Evict` path returns it.
- Smoke test asserts `errors.Is(supervise.ErrBootTimeout, supervise.ErrBootTimeout) == true`.

---

## Cross-cutting invariants

| ID | Invariant | Spec / Constitution |
|----|-----------|--------------------|
| X-1 | No package-level mutable state | Constitution IX (`gochecknoglobals`) |
| X-2 | Every goroutine has a clear owner + ctx-cancellation path + termination condition | Constitution IX |
| X-3 | Every error wraps via `%w` and uses sentinel for class identity | Constitution IX |
| X-4 | Every secret bytes path uses `*SecureBytes`; no `string(decryptedBytes)` anywhere | Constitution X / SC-021-8 |
| X-5 | All exported symbols carry godoc with examples for non-obvious shapes | Constitution X (operator-visible contract) |
| X-6 | `-race` flag clean across the test suite | Constitution VIII |
| X-7 | Coverage ≥95% on the three new files | SC-021-10 |
