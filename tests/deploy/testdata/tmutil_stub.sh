#!/usr/bin/env bash
# tmutil_stub.sh — recording shim used by the SDD-29 integration tests.
# Renamed to `tmutil` and placed first on PATH so install.sh resolves it
# in place of the real Apple binary. Appends its argv to ${TMUTIL_LOG}
# (one space-separated line per invocation) and exits 0 unconditionally.
set -euo pipefail
printf '%s\n' "$*" >> "${TMUTIL_LOG:-/tmp/tmutil.log}"
exit 0
