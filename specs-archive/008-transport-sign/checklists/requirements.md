# Specification Quality Checklist: internal/transport/sign — canonical-JSON request signing + replay protection

**Purpose**: Validate specification completeness and quality before proceeding to planning
**Created**: 2026-04-28
**Feature**: [spec.md](../spec.md)

## Content Quality

- [x] No implementation details (languages, frameworks, APIs)
- [x] Focused on user value and business needs
- [x] Written for non-technical stakeholders
- [x] All mandatory sections completed

## Requirement Completeness

- [x] No [NEEDS CLARIFICATION] markers remain
- [x] Requirements are testable and unambiguous
- [x] Success criteria are measurable
- [x] Success criteria are technology-agnostic (no implementation details)
- [x] All acceptance scenarios are defined
- [x] Edge cases are identified
- [x] Scope is clearly bounded
- [x] Dependencies and assumptions identified

## Feature Readiness

- [x] All functional requirements have clear acceptance criteria
- [x] User scenarios cover primary flows
- [x] Feature meets measurable outcomes defined in Success Criteria
- [x] No implementation details leak into specification

## Notes

- Items marked incomplete require spec updates before `/speckit-clarify` or `/speckit-plan`.
- The chunk doc (`docs/sdd/SDD-08.md`) names specific Go libraries (`go-bitcoin`,
  `sync.Map`, `json.RawMessage`) and exported function names. Those are HOW and
  belong to the plan phase. The spec deliberately uses concept-level language
  ("ECDSA signature", "canonical JSON encoder", "explicit Run entry point",
  "nonce-cache entry") so plan-phase choices remain reversible.
- The constitution's Principle III (Layer 4) names the crypto choice (secp256k1
  ECDSA, SHA-256). The spec treats those as fixed by upstream documents rather
  than re-deriving them, and lists them in **Assumptions** so the trace from
  "WHAT" → "HOW" stays explicit.
- The "exactly one firstSeen=true under concurrent Add" property (FR-008,
  SC-006) and the "no implicit goroutines" property (FR-009/FR-010, SC-007)
  are the two non-obvious invariants that fuzz/race testing alone would not
  surface; they are called out as their own user stories so the test authors
  cannot miss them.
