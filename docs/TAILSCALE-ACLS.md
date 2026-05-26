# TAILSCALE-ACLS — recommended ACL pattern for hush

> hush requires Tailscale-only network reachability for the vault server.
> This document shows the **recommended ACL pattern** for restricting
> port 7743 (the default vault server port) to authorised agent machines.
> Operators substitute their own tag names; the pattern is the load-bearing
> part, not the specific names.

---

## Why ACLs matter

hush's network rule is non-negotiable: the vault server MUST NOT be
reachable outside the Tailscale mesh. Tailscale's default ACL (allow all
between mesh members) is too permissive — a compromised non-agent device
on the same tailnet could probe port 7743.

A correct ACL grants port 7743 access **only** from explicitly tagged agent
machines to explicitly tagged vault hosts. Everything else is denied at the
Tailscale layer, before the request ever reaches the vault server's
defence-in-depth (signed-request verification, IP allowlist, JWT validation).

---

## The pattern

The canonical tag pair is `tag:trusted → tag:sandbox:7743`. Many operators
prefer more descriptive names such as `tag:hush-agent → tag:hush-vault`. The
**pattern** is the load-bearing part — one source tag, one destination tag,
port 7743 only — not the specific names.

Two tags (substitute names that fit your existing tailnet conventions):

- **source tag** (canonical: `tag:trusted`; descriptive alternative
  shown in examples below: `tag:hush-agent`) — applied to machines
  that run `hush request`, `hush supervise`, or `hush client`. These
  are the legitimate clients.
- **destination tag** (canonical: `tag:sandbox`; descriptive
  alternative: `tag:hush-vault`) — applied to the single vault host
  that runs `hush serve`.

The grant pattern: `<source-tag> → <destination-tag>:7743` (and nothing
else for port 7743). Either tag-pair satisfies the Tailscale-only network
rule as long as the grant is scoped to port 7743 and the source-tagged set
is exactly the set of authorised agents.

---

## Example ACL JSON

The following is a representative Tailscale ACL excerpt showing the
hush-relevant grants. Drop it into your existing `tailnet/policy.hujson`
(or equivalent) alongside your other tag definitions and ACL entries.

```hujson
{
  "tagOwners": {
    "tag:trusted":  ["autogroup:admin"],   // canonical hush source tag
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

### Before / after diff (illustrative)

If your tailnet currently has a permissive default-allow for everything,
the change looks like:

```diff
   "acls": [
     {
       "action": "accept",
       "src":    ["*"],
       "dst":    ["*:*"]
     },
+    {
+      "action": "accept",
+      "src":    ["tag:trusted"],
+      "dst":    ["tag:sandbox:7743"]
+    }
   ]
```

If your tailnet uses a default-deny model (recommended), the hush rule is
the only thing granting port 7743 across the mesh:

```diff
   "acls": [
     // (existing tag-scoped grants...)
+    {
+      "action": "accept",
+      "src":    ["tag:trusted"],
+      "dst":    ["tag:sandbox:7743"]
+    }
   ]
```

---

## Applying the tags

In the Tailscale admin console (`https://login.tailscale.com/admin/machines`),
edit each machine and set the appropriate tag:

- The vault host: tag `sandbox` (canonical) or `hush-vault`
  (descriptive alternative).
- Each agent machine that runs `hush request` or `hush supervise`:
  tag `trusted` (canonical) or `hush-agent` (descriptive alternative).

Untagged machines continue to use whatever your default ACL specifies
(typically: full access between members, or default-deny if you've moved
to that model). Either way, port 7743 is gated by the new rule.

---

## Verification

After applying the ACL:

1. From an agent machine: `curl -v http://<vault-tailscale-ip>:7743/h/<prefix>/hz`
   should return HTTP 200 and the health JSON.
2. From a non-agent machine on the same tailnet:
   `curl -v http://<vault-tailscale-ip>:7743/h/<prefix>/hz`
   should fail with **connection refused** or **timeout** (depending on
   Tailscale ACL enforcement mode). If it returns 200, the ACL is wrong.
3. From the public internet: it must **not** be reachable. The vault
   server's `listen_addr` is bound to a Tailscale interface IP, enforced
   at the bind layer in addition to the ACL.

---

## Tightening further (optional)

For higher-security environments:

- **Per-agent restriction:** Replace the canonical source tag with
  one tag per agent machine (e.g., `tag:trusted-<machine-name>` or
  `tag:hush-agent-<machine-name>`). Combined with the per-machine
  client key (`m/44'/7743'/3'/{machine_index}` BIP32 path), this
  gives two independent authorisation factors at the network layer
  alone.
- **Time-of-day grants:** Tailscale supports time-based ACLs. If your
  agents only need vault access during business hours, narrow the grant
  accordingly — in tandem with the supervisor's `refresh_window`
  configuration.
- **Auto-tagging by device posture:** If your tailnet integrates with a
  device posture provider, restrict `tag:hush-agent` to devices that
  meet a posture check (disk encryption on, OS up to date, etc.).

These are tightenings; the **default pattern in this document is the floor**.

---

## What this document does NOT cover

- Tailscale installation or tailnet bootstrap. Refer to Tailscale's own
  documentation.
- Defence-in-depth at the application layer: ECDSA-signed requests, IP
  allowlist, JWT validation. Those live inside hush itself and are
  documented in [`docs/SECURITY.md`](SECURITY.md) Layer 4.
- Multi-tailnet topologies. The pattern works inside a single tailnet;
  cross-tailnet sharing is out of scope.

---

## Cross-references

- [`docs/SECURITY.md`](SECURITY.md) — full threat model (Layers 1–7); this
  doc covers the network perimeter only.
- [`docs/CONFIG-SCHEMA.md`](CONFIG-SCHEMA.md) — server config:
  `[network] require_tailscale = true` and `allowed_cidrs`.
- [`docs/CLEAN-MACHINE.md`](CLEAN-MACHINE.md) — companion checklist for
  removing on-host secrets from agent machines.
