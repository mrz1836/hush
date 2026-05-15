#!/usr/bin/env bash
# check-package-map-vs-code.sh — drift detector for the docs/PACKAGE-MAP.md
# Symbol manifest vs the actual exported symbols of internal/* packages.
#
# Per SDD-33 FR-013 / SC-002 / R-001 (specs/033-final-overhaul/research.md).
# Stdlib only (Constitution XI): bash + awk + sed + diff + sort + go + git.
#
# Usage:
#   scripts/check-package-map-vs-code.sh
#
# Run from repo root. Refuses to run elsewhere (exit 2).
#
# Exit codes:
#   0 — manifest matches code; no drift.
#   1 — drift detected; stdout names every offending symbol.
#   2 — usage / environment error (wrong cwd, missing tooling, missing
#       PACKAGE-MAP.md or its manifest block).
#   3 — internal error (go doc failed, parse failed).
#
# Self-test (operator-runnable; verifies the script catches drift):
#
#   1. Copy the repo to a tempdir:
#        cp -R . /tmp/hush-self-test && cd /tmp/hush-self-test
#   2. Inject a stub exported function:
#        printf '\nfunc StubForDriftCheck() {}\n' >> internal/audit/doc.go
#   3. Run the script:
#        scripts/check-package-map-vs-code.sh
#   4. Expected (exit 1):
#        internal/audit:
#          - code-only: StubForDriftCheck
#   5. Cleanup:
#        cd - && rm -rf /tmp/hush-self-test

set -euo pipefail

PROG="check-package-map-vs-code"

err() { printf '%s: error: %s\n' "$PROG" "$*" >&2; }

# 1. cwd must be repo root.
toplevel=$(git rev-parse --show-toplevel 2>/dev/null || true)
if [[ -z "$toplevel" ]]; then
  err "not inside a git repository"
  exit 2
fi
if [[ "$PWD" != "$toplevel" ]]; then
  err "must be run from the repo root: $toplevel (currently in $PWD)"
  exit 2
fi

# 2. Required tooling.
for tool in go awk sed diff sort comm; do
  if ! command -v "$tool" >/dev/null 2>&1; then
    err "required tool not on PATH: $tool"
    exit 2
  fi
done

# 3. PACKAGE-MAP.md + manifest block.
pkgmap="docs/PACKAGE-MAP.md"
if [[ ! -f "$pkgmap" ]]; then
  err "missing $pkgmap"
  exit 2
fi
if ! grep -q '^<!-- symbol-manifest: BEGIN' "$pkgmap"; then
  err "missing 'symbol-manifest: BEGIN' marker in $pkgmap"
  exit 2
fi
if ! grep -q '^<!-- symbol-manifest: END -->' "$pkgmap"; then
  err "missing 'symbol-manifest: END -->' marker in $pkgmap"
  exit 2
fi

# 4. Compute expected set: extract the fenced block between manifest markers,
#    strip the surrounding ``` lines and any blank lines.
tmpdir=$(mktemp -d -t hush-drift.XXXXXX)
trap 'rm -rf "$tmpdir"' EXIT

expected="$tmpdir/expected.txt"
actual="$tmpdir/actual.txt"

awk '
  /^<!-- symbol-manifest: BEGIN/  { inblk = 1; next }
  /^<!-- symbol-manifest: END --> / { inblk = 0; next }
  /^<!-- symbol-manifest: END -->/  { inblk = 0; next }
  inblk == 1 {
    if ($0 ~ /^```/) next
    if ($0 ~ /^[[:space:]]*$/) next
    print $0
  }
' "$pkgmap" | sort -u > "$expected"

if [[ ! -s "$expected" ]]; then
  err "expected manifest block in $pkgmap is empty"
  exit 2
fi

# 5. Compute actual set: enumerate ./internal/... and extract symbols
#    via embedded awk script.
extractor="$tmpdir/extract.awk"
cat > "$extractor" <<'AWK'
BEGIN { inblock = 0 }
/^const \(/ { inblock = 1; next }
/^var \(/   { inblock = 1; next }
/^\)$/      { inblock = 0; next }
{
  if (inblock == 1) {
    if ($0 ~ /^[[:space:]]*$/) next
    if ($0 ~ /^[[:space:]]*\/\//) next
    if (match($0, /^[[:space:]]+[A-Z][A-Za-z0-9_]+/)) {
      sym = substr($0, RSTART, RLENGTH)
      sub(/^[[:space:]]+/, "", sym)
      print pkg, sym
    }
    next
  }
  if (/^const [A-Z]/) {
    sym = $2; gsub(/[=\(\[].*$/, "", sym); gsub(/[[:space:]]+$/, "", sym)
    if (sym ~ /^[A-Z]/) print pkg, sym
    next
  }
  if (/^var [A-Z]/) {
    sym = $2; gsub(/[=\(\[].*$/, "", sym); gsub(/[[:space:]]+$/, "", sym)
    if (sym ~ /^[A-Z]/) print pkg, sym
    next
  }
  if (/^type [A-Z]/) {
    sym = $2; gsub(/[\[=].*$/, "", sym); gsub(/[[:space:]]+$/, "", sym)
    if (sym ~ /^[A-Z]/) print pkg, sym
    next
  }
  if (/^func [A-Z]/) {
    sym = $2; gsub(/[\(\[].*$/, "", sym)
    if (sym ~ /^[A-Z]/) print pkg, sym
    next
  }
}
AWK

pkglist="$tmpdir/pkgs.txt"
if ! go list ./internal/... > "$pkglist" 2>"$tmpdir/golist.err"; then
  err "go list ./internal/... failed:"
  sed 's/^/  /' "$tmpdir/golist.err" >&2
  exit 3
fi

modroot=$(go list -m 2>/dev/null || true)
if [[ -z "$modroot" ]]; then
  err "could not determine go module root"
  exit 3
fi

: > "$actual"
while IFS= read -r fullpkg; do
  short="${fullpkg#${modroot}/}"
  if ! go doc -short -all "$fullpkg" 2>"$tmpdir/godoc.err" \
      | awk -v pkg="$short" -f "$extractor" >> "$actual"; then
    err "go doc -short -all $fullpkg failed:"
    sed 's/^/  /' "$tmpdir/godoc.err" >&2
    exit 3
  fi
done < "$pkglist"

sort -u "$actual" -o "$actual"

# 6. Diff and report.
pkg_count=$(wc -l < "$pkglist" | tr -d '[:space:]')
sym_count=$(wc -l < "$actual" | tr -d '[:space:]')

if diff -u "$expected" "$actual" > "$tmpdir/diff" 2>/dev/null; then
  printf '%s: %s packages, %s exported symbols, 0 drift.\n' \
    "$PROG" "$pkg_count" "$sym_count"
  exit 0
fi

# Drift: report per-package via the union of mismatched lines.
# `+ doc-only` = manifest has it, code does not.
# `- code-only` = code has it, manifest does not.
doc_only=$(comm -23 "$expected" "$actual")
code_only=$(comm -13 "$expected" "$actual")

# Group offenders by package for readable output.
all_pkgs=$(printf '%s\n%s\n' "$doc_only" "$code_only" \
  | awk 'NF { print $1 }' | sort -u)

drift_pkg_count=$(printf '%s\n' "$all_pkgs" | awk 'NF' | wc -l | tr -d '[:space:]')
drift_sym_count=$(printf '%s\n%s\n' "$doc_only" "$code_only" \
  | awk 'NF' | wc -l | tr -d '[:space:]')

printf '%s\n' "$all_pkgs" | awk 'NF' | while IFS= read -r p; do
  printf '%s:\n' "$p"
  printf '%s\n' "$doc_only" | awk -v pkg="$p" '$1 == pkg { printf "  + doc-only:   %s\n", $2 }'
  printf '%s\n' "$code_only" | awk -v pkg="$p" '$1 == pkg { printf "  - code-only:  %s\n", $2 }'
done

printf '%s: %s packages drifting, %s symbols total. Reconcile via PACKAGE-MAP.md edit or symbol removal.\n' \
  "$PROG" "$drift_pkg_count" "$drift_sym_count"
exit 1
