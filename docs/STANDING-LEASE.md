# STANDING-LEASE — machine-bound standing supervisor lease

> Design + security review for an **opt-in, machine-bound standing supervisor
> lease**: a single human approval establishes a supervisor session that
> reissues itself against the per-machine client key with **no further
> Discord approval**, until it is revoked. It is a narrow, revocable carve-out
> to the "one approval per bounded TTL" model — proposed as a constitutional
> amendment (Principles II & V), not a blanket auto-approve.
>
> Status: **RATIFIED 2026-07-14.** The Principles II & V amendment in
> `.specify/memory/constitution.md` is ratified (Constitution v3.0.0), so this
> design is in force. See §9. Pairs with `docs/DAEMONS.md`,
> `docs/CONFIG-SCHEMA.md`, and `docs/SECURITY.md` §4 / §6.

---

## 1. The problem: a truly always-on daemon still pages the phone

`hush supervise` already decouples Discord-approval lifetime from
child-process lifetime: one approval covers crashes, updates, and restarts
within a bounded session TTL (Constitution V; `docs/DAEMONS.md` §1–3).
Two mechanisms keep an ordinary daemon quiet:

- **Silent refill within TTL.** While the supervisor's JWT is valid, a child
  crash triggers a silent re-fetch — no Discord call.
- **Session resumption.** When a supervisor process itself restarts,
  `tryResumeSupervisorSession` (`internal/server/claim_handler.go`) finds the
  live `(SessionSupervisor, ClientIP, Scope)` session via
  `Store.FindActiveSession`, mints a fresh JWT for the caller's new ephemeral
  key **inheriting only the remaining TTL**, and revokes the old JTI — again
  no approver call.

Both stop at the TTL boundary. The supervisor TTL is hard-capped at **24h**
(`DefaultSupervisorTTLMax` in `internal/config/defaults.go`; `MaxRequestedTTL`
in `internal/supervise/config/defaults.go`; enforced by `capTTL`, which clamps
to `cs.MaxSupervisorTTL`). Once that window lapses:

- `tryResumeSupervisorSession` computes `remaining = ExpiresAt - now`; when
  `remaining <= 0` it returns `false` and the claim falls through to the human
  approver.
- The grace cache (`cache_secrets_for_restart`) is capped at **4h**
  (`MaxGraceWindow`), so it cannot bridge a full unattended day.

For a daemon that **must** fire on a fixed schedule around the clock — an
evening bell, an overnight dead-man tripwire, a monthly heartbeat — that
boundary means a **recurring human approval**: every ~24h the operator must
tap Approve on their phone, or the next cold restart after the lapsed window
parks at `awaiting-approval` and the scheduled action silently does not fire.
That recurring tap is the exact failure this design removes.

### Why raising the TTL alone is not enough

Bumping the 24h ceiling to, say, two weeks reduces the tap frequency but does
not remove it — the operator still taps on every renewal, and a cold restart
after the (now longer) window still parks until approved. A daemon whose whole
purpose is unattended reliability cannot depend on a periodic phone tap at all.
The bounded-TTL bump is retained here only as a **fallback** (§8).

---

## 2. Goals & non-goals

**Goals:**

- One opted-in supervised daemon, on one enrolled machine, delivers a single
  scoped secret with **zero recurring human approval** after a one-time
  establishing approval.
- The mechanism is **revocable** in one operator action and **auditable** —
  every unattended reissue emits a distinct audit event.
- Every other supervisor on every machine keeps the unchanged 24h
  human-approval floor. The change is inert unless explicitly opted in.

**Non-goals:**

- **Not** a blanket auto-approve. The **first / establishing** grant for a
  standing lease still goes through the human approver — the Constitution II
  choke point in the `/claim` pipeline is untouched.
- **Not** a trusted-host mode. The lease is bound to one machine's registered
  client key; presenting the same config on another machine falls back to
  human approval.
- **Not** unscoped. A standing lease covers exactly the `scope` array declared
  in its supervisor TOML — one lease, one fixed secret set.

---

## 3. The mechanism

### 3.1 Opt-in flag

A new supervisor-TOML field:

```toml
session_type         = "supervisor"
standing_lease       = true          # opt-in; default false
client_machine_index = 1             # REQUIRED when standing_lease = true
scope                = ["EXAMPLE_DAEMON_TOKEN"]
```

Cross-field validation (config load, `internal/supervise/config`):

- `standing_lease = true` **requires** `client_machine_index` to be set and
  `session_type = "supervisor"`. Absent machine index → a load-time error
  (new sentinel, e.g. `ErrStandingLeaseNeedsMachineIndex`).
- Default is `false`; a config that omits the field is byte-for-byte a
  today's-behavior supervisor.
- Strict TOML decoding still rejects unknown fields — the flag is added to the
  decoder, not smuggled past it.

### 3.2 Establishing grant (human — unchanged)

The first `/claim` for a standing supervisor session is an ordinary claim: it
walks the full pipeline (`shape → verify → nonce → timestamp → ip → capTTL →
approver → issue`) and lands on the **human approver**. The operator taps
Approve once. This establishes the machine-bound grant. No approval, no lease.

### 3.3 Standing reissue (unattended)

The relaxation lives in `tryResumeSupervisorSession`. Today it reissues only
the **remaining** TTL and bails when the window has lapsed. For a session whose
config is flagged `standing_lease`, the resume path is widened:

- When a standing supervisor claim arrives with a **matching machine-bound
  prior grant** for the same `(ClientIP, Scope)` tuple, reissue a **fresh
  full-window session** (up to `MaxStandingLeaseTTL`, §3.5) instead of
  inheriting only the remaining TTL — **without** calling
  `approverImpl.RequestApproval`.
- The reissue rides the one human approval that established the lease. It never
  reaches the approver, so there is no DM, no rate-limit wait, no keychain
  prompt.
- The establishing choke point is untouched: if no prior machine-bound grant
  exists (first boot, or after revocation), the claim flows to the human
  approver as normal.

The token record already carries `ClientIP` and `Scope` for the
`(SessionType, ClientIP, Scope)` secondary lookup (`internal/token`). A
**standing / machine marker** is added to that record (`internal/token/issue.go`)
so resumption can recognize an established standing grant rather than an
ordinary within-TTL resume.

### 3.4 Machine binding

The binding anchor is the per-machine client key. Each enrolled machine has a
registered client keypair at BIP32 path `m/44'/7743'/3'/{client_machine_index}`
(`docs/SECURITY.md` Layer 1 / Layer 4). Every `/claim` is already ECDSA-signed
by that key and IP-checked against the Tailscale peer address — two factors,
unchanged.

The standing lease adds **no new trust in the machine beyond what a signed
claim already proves**: the reissue only fires when the claim is signed by the
enrolled client key registered for `client_machine_index` and originates from
the allow-listed Tailscale IP. A claim that presents the standing config but is
signed by a different machine's key (or from a non-allow-listed IP) does not
match the prior grant and falls back to the human approver. Machine mismatch
degrades safely to the existing floor; it never silently reissues.

### 3.5 Distinguished ceiling — ordinary supervisors keep 24h

A new bound `MaxStandingLeaseTTL` is added to
`internal/supervise/config/defaults.go`. The 24h ceilings that apply to
ordinary supervisors (`DefaultSupervisorTTLMax`, `MaxRequestedTTL`, the
`capTTL` clamp) are **unchanged for non-standing sessions**. Only a session
whose config is flagged `standing_lease` may exceed 24h and be reissued on the
standing path; the validations apply `MaxStandingLeaseTTL` solely to that path.
This keeps the relaxation from leaking into any supervisor that did not opt in.

### 3.6 Audit

Every unattended reissue emits a **distinct** audit event (e.g. an
`outcome: "standing-reissue"` claim-audit detail), separate from both the
`approved` outcome and the existing within-TTL resume ops-log line. The
hash-chained, ECDSA-signed audit log (`docs/SECURITY.md` Layer 6) remains the
source of truth for "who reissued what, when" — a standing lease is fully
visible in the audit stream, never silent.

---

## 4. Trust boundary

| Element | Trust under a standing lease |
|---------|------------------------------|
| The one enrolled machine's client key (`m/44'/7743'/3'/{index}`) | Trusted to reissue **its own** already-established, single-scope session — nothing else |
| The Tailscale peer IP | Trusted as the second factor, exactly as today |
| The establishing approval | Human, one-time; the root of the lease's authority |
| Any other machine / scope / secret | **Untrusted** — every other claim keeps the full human-approval floor |
| The vault host | Semi-trusted as today; a standing lease grants no new server-side authority |

The lease widens exactly one thing: the **reissue** of a supervisor session
that a human already approved, for one machine and one scope, until revoked.
It does not lower any of the seven crypto layers, does not touch the
Tailscale-only perimeter, and does not create a first-grant bypass.

---

## 5. Security review / threat model

Mirrors the residual-risk treatment in `docs/SECURITY.md` §6.

| Threat | Mitigation under a standing lease |
|--------|-----------------------------------|
| Malware on the enrolled machine mints an unattended reissue | It would need the machine's client signing key (mlocked, keychain-ACL'd, never on disk) **and** the allow-listed Tailscale IP. This is the same two-factor bar as any `/claim` today — the lease adds no new key material and no new endpoint. |
| Attacker copies the standing config to another host | Machine binding: the reissue only matches a grant established by the client key for `client_machine_index`. A different machine's key does not match → falls back to the human approver. |
| Attacker widens the blast radius via the lease | Scope is fixed to the TOML `scope` array (single secret in the intended deployment). A standing lease cannot reach any other secret. |
| Standing reissue happens silently / undetected | Every reissue emits a distinct hash-chained audit event; the audit chain is tamper-evident. Watchdog `401` patterns and `job_run` records surface a broken lease. |
| Lease outlives its usefulness / needs to be pulled | Revocable in one operator action (§6.3): `hush revoke` the active session, drop `standing_lease` from the TOML, reload — the daemon returns to the 24h human floor. |
| First request auto-approved | **Impossible.** The establishing grant is human-only; the Constitution II choke point is unchanged. Only reissue of an already-approved session is unattended. |
| The bounded 24h floor is weakened for other daemons | Opt-in + distinguished ceiling: `MaxStandingLeaseTTL` applies only to `standing_lease` sessions; every other supervisor keeps 24h. |

### Residual risk the operator accepts

A standing lease means: **for one enrolled machine and one scoped secret, the
one-time human approval extends indefinitely — the machine can silently
reissue its own session until the lease is revoked.** If that single machine
is fully compromised at the level of its keychain-held client signing key,
the attacker can reissue the one scoped secret without a fresh approval, for
as long as the lease stands, within the existing bounded time-window that any
active session already grants. The operator accepts this because the secret in
question is considered safe on that specific machine and because the failure
mode it removes — an unattended daemon silently ceasing to fire because nobody
tapped a phone — is judged the greater operational risk. The residual is
**bounded to one machine and one scope**, is **auditable**, and is
**revocable at will**. This is documented in `docs/SECURITY.md` §6 as an
accepted, opt-in trade-off.

---

## 6. Lifecycle: provision, rotate, revoke, monitor

### 6.1 Provision

1. Enroll the machine's client key (`hush init client --machine-index N`) and
   register its public key on the vault server, if not already done.
2. Vault the scoped secret (`hush secret add EXAMPLE_DAEMON_TOKEN`, interactive
   TTY).
3. Set `standing_lease = true` and `client_machine_index = N` in the
   supervisor TOML; declare the single-secret `scope`.
4. Start the supervisor. The first claim fires **one** establishing approval;
   the operator taps Approve once. The lease is now standing.

### 6.2 Rotate

Rotate the underlying secret with the normal path — `hush secret rotate
EXAMPLE_DAEMON_TOKEN` on the vault host (SIGHUP atomic swap), then `hush client
refresh --supervisor <daemon>` on the agent host. Rotation does **not** require
re-establishing the lease; the standing grant continues to reissue the
now-rotated value.

### 6.3 Revoke

Any of the following pulls the lease:

- `hush revoke <jti>` kills the active session; the next reissue attempt finds
  no live grant.
- Remove `standing_lease` from the TOML (or set it `false`) and reload the
  supervisor — subsequent claims flow to the human approver again.
- Rotating/removing the scoped secret, or de-registering the machine's client
  key, removes the lease's reach entirely.

Full revocation = drop the flag **and** revoke the active session, then reload,
so no in-flight session can reissue.

### 6.4 Monitor

- **Audit:** the distinct `standing-reissue` events in the hash-chained audit
  log.
- **Watchdog:** `401`/stale patterns surface a lease that has stopped working.
- **Delivery record:** the daemon's own run log / job records confirm the
  scheduled actions actually fired unattended.

---

## 7. Comparison with existing mechanisms

| | Ordinary supervisor session | Grace cache (`cache_secrets_for_restart`) | **Standing lease** |
|---|---|---|---|
| First approval | Human | Human | Human (unchanged) |
| Reissue after TTL lapse | Human re-approval | N/A (caches plaintext, ≤4h) | **Unattended, machine-bound** |
| Max unattended window | 24h | +4h grace | Until revoked (bounded per reissue by `MaxStandingLeaseTTL`) |
| Extra plaintext surface | Child only | Child + supervisor (cap 4h) | Child only — **no new plaintext cache** |
| Opt-in | n/a (default) | Per supervisor | Per supervisor + per machine |
| Blast radius | 24h window | Grace window | One machine, one scope, revocable |

The standing lease is orthogonal to the grace cache: it does **not** hold a
plaintext secret cache. It changes *when the supervisor may reissue its own
session unattended*, not *what plaintext the supervisor retains*. The two knobs
can be combined or used independently.

---

## 8. Fallback: bounded long TTL + mid-day refresh window

If the standing-lease carve-out is not ratified, the documented fallback keeps
the phone-tap cadence low without a new primitive:

- Raise the standing path's ceiling and set `requested_ttl ≈ 14d` (≈ `336h`).
- Move `refresh_window` to a **mid-day** slot, off any scheduled-fire boundary,
  so the renewal DM never contends with the daemon's actual fire time.

This is **one tap every ~2 weeks**, not zero — a cold restart after the window
still parks until approved. It requires only the TTL-ceiling change (raise the
bound for the standing path), **not** the reissue primitive of §3. It is the
strictly-more-conservative option and is retained so the owner can redirect to
it at ratification.

---

## 9. Constitutional impact

The standing lease is honestly a machine-scoped **standing grant** that
reissues a supervisor session without a fresh approval. That relaxes:

- **Principle II** ("no auto-approve, no trusted-host exception, no
  service-account bypass") — narrowly, for *reissue of an already-approved
  session*, bound to one machine and one scope, revocable. First grants stay
  human.
- **Principle V** ("one approval covers crashes within the session TTL") —
  extends "within the session TTL" to "within the standing lease, until
  revoked" for opted-in sessions only.

Because this redefines two principles, it is a **MAJOR amendment** (Governance
§3: incompatible redefinition). The amendment text lives in
`.specify/memory/constitution.md`, **ratified 2026-07-14** (owner sign-off) and
folded into Principles II & V. The constitution version bumped `2.0.0 → 3.0.0`
and the sync-impact report records the carve-out.

---

## 10. Cross-references

| Topic | See |
|-------|-----|
| Threat model + residual-risk table | `docs/SECURITY.md` §4, §6 |
| Supervisor model + grace-cache tradeoff | `docs/DAEMONS.md` §1–6 |
| Per-supervisor TOML schema | `docs/CONFIG-SCHEMA.md` |
| `/claim` grant path | `docs/API.md` |
| Constitution (Principles II & V, proposed amendment) | `.specify/memory/constitution.md` |
