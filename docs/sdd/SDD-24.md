# SDD-24 — (reserved orchestration glue; default skipped)

**Phase:** 5
**Status:** SKIPPED by default
**Coverage target:** N/A
**Branch:** N/A (no branch is created unless this chunk is activated)

---

## Why this slot exists

SDD-24 is a deliberately reserved slot. The original SDD plan
allocated it for orchestration glue that might be needed if SDD-25's
fifteen-scenario lifecycle integration harness surfaces seams between
the supervisor packages. The default assumption is that SDD-21's
refill/refresh/grace + SDD-22's pidfile/socket + SDD-23's CLI
orchestrator already cover everything; SDD-24 only activates if
SDD-25 reveals a gap.

## How to handle this slot

There are two valid outcomes for SDD-24 — pick the one that matches
what SDD-25 produced:

### Outcome A — confirm skipped (most likely)

After SDD-25 ships green, if no orchestration gap surfaced:

1. Update [docs/SDD-PLAYBOOK.md](../SDD-PLAYBOOK.md) row for SDD-24:
   leave status `skipped`.
2. No code, no spec, no plan, no tasks. No commit needed beyond what
   SDD-25 already committed.

### Outcome B — activate the slot (only if SDD-25 surfaces a gap)

If SDD-25's harness reveals a real seam (something the SDD-19..23
orchestrator can't express cleanly), then SDD-24 becomes a real
chunk. In that case:

1. **Define the gap**, in plain English, in a fresh
   [docs/sdd/SDD-24.md](SDD-24.md) (overwrite this file). Cite the
   specific SDD-25 scenario(s) that exposed it.
2. **Decide on a chunk identity**: package, branch name, primary
   AC, blockers/blocks, coverage target.
3. **Restructure this file using the standard 5-prompt template**
   (see [docs/sdd/SDD-01.md](SDD-01.md) as the canonical example).
   Specify and Plan prompts MUST be verbose; Clarify, Tasks, and
   Implement MUST be lean.
4. Run the 5 prompts in 5 fresh Claude Code sessions per the
   [docs/SDD-PLAYBOOK.md](../SDD-PLAYBOOK.md) workflow.
5. The Implement prompt's combined commit should reference SDD-24
   and cite the SDD-25 scenario that motivated it.

## Decision rule

The default decision is **skip**. Activating SDD-24 requires
explicit, documented evidence from SDD-25 — do NOT pre-emptively
fill this slot just because the number exists.

## Cross-references

- Original SDD-25 owner of the lifecycle scenarios:
  [docs/sdd/SDD-25.md](SDD-25.md)
- Workflow expectations:
  [docs/SDD-PLAYBOOK.md](../SDD-PLAYBOOK.md)
- Status table:
  [docs/SDD-PLAYBOOK.md](../SDD-PLAYBOOK.md) (SDD-24 row defaults to
  `skipped`)
