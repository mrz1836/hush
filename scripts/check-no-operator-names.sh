#!/usr/bin/env bash
# check-no-operator-names.sh — operator-name leak gate (whole tree).
#
# Per SDD-33 FR-014 / SC-005 (research.md R-004). The structural test
# is the Go test:
#
#   internal/supervise/config/example_test.go::
#       TestExamples_NoOperatorSpecificNames_WholeTree
#
# This shell wrapper exists so operators can run the gate from the
# repo root without thinking about the Go test path. It delegates to
# `go test` and inherits the test's exit code.
#
# Usage:
#   scripts/check-no-operator-names.sh
#
# Exit codes:
#   0 — no operator-specific tokens leaked anywhere in the tree.
#   1 — leak detected; the underlying Go test names the offending
#       file(s) and token(s).
#   2 — usage / environment error (wrong cwd, missing tooling).

set -euo pipefail

PROG="check-no-operator-names"

err() { printf '%s: error: %s\n' "$PROG" "$*" >&2; }

toplevel=$(git rev-parse --show-toplevel 2>/dev/null || true)
if [[ -z "$toplevel" ]]; then
  err "not inside a git repository"
  exit 2
fi
if [[ "$PWD" != "$toplevel" ]]; then
  err "must be run from the repo root: $toplevel (currently in $PWD)"
  exit 2
fi

if ! command -v go >/dev/null 2>&1; then
  err "go not on PATH"
  exit 2
fi

go test -count=1 \
  -run '^TestExamples_NoOperatorSpecificNames_WholeTree$' \
  ./internal/supervise/config/...
