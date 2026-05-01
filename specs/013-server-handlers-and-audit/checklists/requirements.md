# Specification Quality Checklist: Server `/s`, `/revoke`, `/hz` Handlers + Audit Log

**Purpose**: Validate specification completeness and quality before proceeding to planning
**Created**: 2026-04-30
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
- The spec deliberately uses generic terms ("ECIES envelope", "hash-chained signed sequence", "canonical-JSON form", "audit-signing key") rather than naming specific Go libraries or hashing algorithm constants — those are plan-phase decisions per the constitution's HOW/WHAT split.
- HTTP status codes are specified where they are part of the externally-observable contract (`200`, `403`, `408`, `503`, `429` for the claim handler in the previous chunk) but elsewhere left as "documented authentication-failure status" / "documented bad-request status" so that the plan phase can finalize the exact mapping when the existing handler conventions are reviewed. This keeps the spec testable without prematurely locking the wire contract.
- The audit chain's exact hash function and signing scheme are not named here — Constitution III/V already constrain them (SHA-256 + ES256K-class secp256k1 ECDSA) and the spec defers to those constraints rather than restating them.
- Three [NEEDS CLARIFICATION] candidates were considered and resolved with informed defaults (in spec):
  1. Whether `/s` returns `403` or `404` for an in-scope-but-missing secret name. Resolved as a "documented not-found status" with the test obligation that scope-existence is not disclosed (FR-006/FR-007).
  2. Whether the audit writer's buffer size is configurable. Deferred to the plan phase — the spec only requires the backpressure invariant (FR-031), not the buffer size.
  3. Whether revocation requires a body or a path-only signature. Resolved as a body-shaped, signed request (FR-010 / FR-011) so the verification path mirrors `/claim`.
