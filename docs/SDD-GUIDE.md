# SDD Guide

## Intent

This repo is being shaped so spec-driven development can execute cleanly.

Phase 0 is not fluff. It is constraint-setting.

## Required alignment

Every implementation phase must keep these aligned:

- approved todo
- approved plan
- tasks.yaml progress
- constitution
- architecture docs
- code layout on disk

## Phase 0 bootstrap standard

Phase 0 is done when:

- repo purpose is obvious
- security posture is obvious
- lifecycle posture is obvious
- future package boundaries are obvious
- there is minimal room for interpretation drift

## Working rules

- Docs define intent before complex implementation
- Security claims require tests before they count
- New behavior should map back to an acceptance criterion
- If implementation contradicts the constitution, implementation is wrong

## Build sequence for T-173

1. bootstrap repo structure
2. lock mission and constitution
3. lock architecture and daemon model
4. build phase-by-phase from the approved plan
5. verify each security property with tests, not vibes
