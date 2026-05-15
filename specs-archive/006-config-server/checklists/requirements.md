# Specification Quality Checklist: internal/config — server TOML schema + validation

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

- Items marked incomplete require spec updates before `/speckit-clarify` or `/speckit-plan`
- Validation iteration 1: all items pass on first pass.
- The spec describes a developer-platform feature; the "operator" is the
  human author of `~/.hush/config.toml` and the downstream callers
  (server entry point, `hush init`) consuming the loaded configuration.
- Plan-phase decisions (TOML decoder choice, exact path-resolution
  mechanism, error type names) are deliberately deferred per the chunk
  contract; the spec documents only the WHAT.
- Constitution alignment notes (load-bearing for review):
  - Principle III (Argon2id ≥ 256 MiB floor) → FR-004, US4, SC-002.
  - Principle VI (Tailscale-only bind) → FR-003, US3, SC-002.
  - Principle X / Security Requirements (no secrets in config, no env
    for secret fields) → FR-006, FR-007, US6, SC-005.
  - Principle VIII (typed errors, fuzz target #5, ≥95% coverage on
    `internal/config`) → FR-009, FR-010, SC-001, SC-002, SC-003.
