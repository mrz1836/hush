# Contract: TAILSCALE-ACLS.md Verification Matrix

**Branch**: `030-examples-and-tailscale` | **Date**: 2026-05-14

This contract is the verify-and-polish audit for `docs/TAILSCALE-ACLS.md`
per FR-009 / SC-005 / SC-007. The /speckit-tasks task T3 walks this
table top to bottom; the post-/speckit-implement gate is: every row's
"Status" cell is `OK` or has an applied patch.

The authoritative cross-reference points are:
- `.specify/memory/constitution.md` Principle VI
- `docs/SECURITY.md` §2.3 Network
- `docs/CONFIG-SCHEMA.md` `[network]` section
- `docs/SPEC.md` FR-8

---

## Audit table

Each row checks one claim in TAILSCALE-ACLS.md against the
authoritative sources. Status values: `OK` (no edit needed),
`PATCH` (specific patch applied in this chunk), `WONTFIX` (out of
scope; deferred to a later chunk with rationale).

| # | TAILSCALE-ACLS.md claim | Authoritative source(s) | Status |
|---|------------------------|------------------------|--------|
| 1 | "hush requires Tailscale-only network reachability for the vault server" (preamble) | Constitution VI: "vault server MUST NOT be reachable outside the Tailscale mesh"; SECURITY.md §2.3 "Tailscale-only"; SPEC.md FR-8 | OK — exact match |
| 2 | "port 7743 (the default vault server port)" (preamble) | CONFIG-SCHEMA.md `[server].listen_addr` example uses port 7743; SECURITY.md §2.3 "port 7743"; constitution VI "port 7743" | OK — port number consistent across all four sources |
| 3 | "Constitution Principle VI is non-negotiable" (§Why ACLs matter) | Constitution VI itself | OK — references the principle by number, preserves the non-negotiable framing |
| 4 | "A correct ACL grants port 7743 access only from explicitly tagged agent machines to explicitly tagged vault hosts" (§Why ACLs matter) | Constitution VI: "Tailscale ACLs MUST restrict port 7743 to `tag:trusted → tag:sandbox` grants"; SECURITY.md §2.3 same | OK at the pattern level (one source tag, one dest tag, port 7743) |
| 5 | "tag:hush-agent — applied to machines that run hush request, hush supervise, or hush client" (§The pattern) | Constitution VI: `tag:trusted` is the canonical source tag name | **PATCH — R-002** |
| 6 | "tag:hush-vault — applied to the single vault host that runs hush serve" (§The pattern) | Constitution VI: `tag:sandbox` is the canonical destination tag name | **PATCH — R-002** |
| 7 | "The grant: tag:hush-agent → tag:hush-vault:7743 (and nothing else for port 7743)" (§The pattern) | Constitution VI: `tag:trusted → tag:sandbox:7743` | **PATCH — R-002** |
| 8 | "Operators are free to substitute names that fit their existing conventions (e.g. tag:trusted-dev and tag:secrets-host). The pattern holds regardless..." (§The pattern, closing) | (this is the operator-substitution clause that justifies R-002's both-pairs-valid resolution) | OK — keep as-is; the patch in R-002 builds on this clause |
| 9 | Example ACL JSON block (§Example ACL JSON) uses `tag:hush-agent` / `tag:hush-vault` throughout | Constitution VI tag pair: `tag:trusted` / `tag:sandbox` | **PATCH — R-002** (update example to show both pairs or use constitutional pair) |
| 10 | "Before / after diff (illustrative)" diff hunks use `tag:hush-agent` / `tag:hush-vault` | Constitution VI tag pair | **PATCH — R-002** |
| 11 | "Applying the tags" section names `hush-vault` and `hush-agent` | Constitution VI tag pair | **PATCH — R-002** |
| 12 | "Verification" section — three verification curl commands | Constitution VI + SECURITY.md §2.3 + SPEC.md FR-8 — all three say the same thing functionally | OK — verification logic is correct |
| 13 | Step 3 of verification: "The vault server's `listen_addr` is bound to a Tailscale interface IP per Constitution Principle VI; this is enforced at the bind layer in addition to the ACL." | CONFIG-SCHEMA.md `[server].listen_addr` rules ("host must resolve to a Tailscale interface address"; "must not be 0.0.0.0, 127.0.0.1, empty host, or a public IP") | OK — exact functional match |
| 14 | "Tightening further (optional)" — per-agent restriction, time-of-day grants, posture | (not normative — operator-tightening menu) | OK — orthogonal to the tag-pair question |
| 15 | "What this document does NOT cover" — Tailscale install, application-layer DiD, multi-tailnet | Cross-references to SECURITY.md, SPEC.md — all consistent | OK |
| 16 | Cross-references block (footer) | All references resolve; SECURITY.md / CONFIG-SCHEMA.md / SPEC.md / CLEAN-MACHINE.md / constitution.md all exist | OK |
| 17 | Operator-specific-identifier grep (FR-011) | seed list per FR-007 | OK — TAILSCALE-ACLS.md uses only generic `tag:hush-*` and example-cidr placeholders; no operator-specific names |

---

## Patch specification (R-002)

The /speckit-tasks T3 task applies the following patches to
`docs/TAILSCALE-ACLS.md`. Each patch is a minimal, focused edit
targeting one of the rows above marked **PATCH**.

### Patch 1 — Rewrite §"The pattern" opening

**Before:**

```
Two tags:

- **`tag:hush-agent`** — applied to machines that run `hush request`,
  `hush supervise`, or `hush client`. These are the legitimate clients.
- **`tag:hush-vault`** — applied to the single vault host that runs
  `hush serve`.

The grant: `tag:hush-agent → tag:hush-vault:7743` (and nothing else for
port 7743).
```

**After:**

```
The constitution names the canonical pair as `tag:trusted → tag:sandbox:7743`.
Many operators prefer more descriptive tags such as
`tag:hush-agent → tag:hush-vault`. The **pattern** is the
load-bearing part — one source tag, one destination tag, port 7743
only — not the specific names.

Two tags (substitute names that fit your existing tailnet
conventions):

- **source tag** (canonical: `tag:trusted`; descriptive alternative
  shown in examples below: `tag:hush-agent`) — applied to machines
  that run `hush request`, `hush supervise`, or `hush client`. These
  are the legitimate clients.
- **destination tag** (canonical: `tag:sandbox`; descriptive
  alternative: `tag:hush-vault`) — applied to the single vault host
  that runs `hush serve`.

The grant pattern: `<source-tag> → <destination-tag>:7743` (and
nothing else for port 7743). Either tag-pair satisfies Constitution
Principle VI as long as the grant is scoped to port 7743 and the
source-tagged set is exactly the set of authorised agents.
```

### Patch 2 — Update §"Example ACL JSON" header tagOwners and acls

**Before:** uses `tag:hush-agent` / `tag:hush-vault` throughout.

**After:** use the constitutional pair as the primary example with a
trailing comment showing the descriptive alternative.

```hujson
{
  "tagOwners": {
    "tag:trusted":  ["autogroup:admin"],   // canonical per Constitution VI
    "tag:sandbox":  ["autogroup:admin"],
    // Descriptive alternative — pick one pair, not both:
    // "tag:hush-agent": ["autogroup:admin"],
    // "tag:hush-vault": ["autogroup:admin"],
  },

  "acls": [
    // Existing rules...

    // hush — vault access for tagged agents only
    {
      "action": "accept",
      "src":    ["tag:trusted"],
      "dst":    ["tag:sandbox:7743"]
    }
  ],

  // Optional: deny non-tagged devices from reaching the vault host
  // entirely (recommended on shared tailnets).
  "ssh": [
    // Existing SSH rules...
  ]
}
```

### Patch 3 — Update §"Before / after diff" hunks

Replace `tag:hush-agent` / `tag:hush-vault` with `tag:trusted` /
`tag:sandbox` in both diff hunks (the default-allow and the
default-deny examples).

### Patch 4 — Update §"Applying the tags"

**Before:**

```
- The vault host: tag `hush-vault`.
- Each agent machine that runs `hush request` or `hush supervise`: tag
  `hush-agent`.
```

**After:**

```
- The vault host: tag `sandbox` (canonical) or `hush-vault`
  (descriptive alternative).
- Each agent machine that runs `hush request` or `hush supervise`:
  tag `trusted` (canonical) or `hush-agent` (descriptive alternative).
```

### Patch 5 — Tightening §"Per-agent restriction"

**Before:**

```
- **Per-agent restriction:** Replace `tag:hush-agent` with one tag per
  agent machine (`tag:hush-agent-<machine-name>`). Combined with the
  per-machine client key (`m/44'/7743'/3'/{machine_index}` BIP32 path),
  this gives two independent authorisation factors at the network layer
  alone.
```

**After:**

```
- **Per-agent restriction:** Replace the canonical source tag with
  one tag per agent machine (e.g., `tag:trusted-<machine-name>` or
  `tag:hush-agent-<machine-name>`). Combined with the per-machine
  client key (`m/44'/7743'/3'/{machine_index}` BIP32 path), this
  gives two independent authorisation factors at the network layer
  alone.
```

---

## Final verification gate (post-patch)

After patches 1–5 are applied, every row in the audit table reads
`OK`. The /speckit-implement step 4 manual cross-doc check confirms:

- SECURITY.md §2.3 ↔ TAILSCALE-ACLS.md tag pair: aligned (canonical
  pair appears in TAILSCALE-ACLS.md primary examples).
- CONFIG-SCHEMA.md `[network]` ↔ TAILSCALE-ACLS.md port + CIDR claims:
  aligned (port 7743 + `100.64.0.0/10` consistent across docs).
- Operator-specific identifier grep (FR-011): zero matches in
  TAILSCALE-ACLS.md after patches.

SC-005 ("zero contradictions between TAILSCALE-ACLS.md and
CONFIG-SCHEMA.md / SECURITY.md Layer 0") is satisfied.
SC-007 ("zero operator-specific identifier matches in
TAILSCALE-ACLS.md") is verified by /speckit-tasks T5
(`TestExamples_NoOperatorSpecificNames` extended to also grep
docs/ TAILSCALE-ACLS.md per spec FR-007 / FR-011 — or by a
manual grep documented in tasks.md; the chunk-doc says "Manual
review" so this is acceptable as a documented manual step).
