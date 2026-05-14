#!/usr/bin/env bash
# install.sh — idempotent installer for the hush vault server.
#
# Operator usage:
#   sudo ./deploy/install.sh                  # canonical
#   HUSH_USER=foo sudo ./deploy/install.sh    # override run-as user
#
# Recognised environment variables (all optional):
#   PREFIX             Install prefix (default /usr/local).
#   HUSH_USER          System account that runs the daemon
#                      (default _hush on macOS, hush on Linux).
#   HUSH_STATE_DIR     Vault state directory
#                      (default /usr/local/var/hush on macOS,
#                       /var/lib/hush on Linux).
#   HUSH_INSTALL_ROOT  Staging prefix for tests (default empty).
#                      Operators should leave this unset.
#   HUSH_SOURCE_BIN    Path to source hush binary (default ./hush).
#   HUSH_FORCE_OS      Test-only escape hatch (darwin|linux|other) that
#                      bypasses uname(1) detection.
#
# Exit codes:
#   0   Success (first run or no-op re-run).
#   1   Generic install failure.
#   2   Bad input (unsupported OS, malformed env, missing binary).
#   3   Insufficient privilege.
#   4   Required external tool missing (tmutil on macOS, useradd on Linux).
#
# install.sh creates ZERO Keychain entries. The next-steps banner prints
# the exact `security add-generic-password -T <binary> ...` invocation
# the operator runs separately. install.sh handles no secret material.

set -euo pipefail

die() {
  printf 'install.sh: %s: %s\n' "$1" "$2" >&2
  exit "${3:-1}"
}

PREFIX="${PREFIX:-/usr/local}"
HUSH_INSTALL_ROOT="${HUSH_INSTALL_ROOT:-}"
HUSH_SOURCE_BIN="${HUSH_SOURCE_BIN:-./hush}"

# --- OS detection ----------------------------------------------------------
if [ -n "${HUSH_FORCE_OS:-}" ]; then
  OS="${HUSH_FORCE_OS}"
else
  case "$(uname -s 2>/dev/null || echo unknown)" in
    Darwin) OS=darwin ;;
    Linux)  OS=linux ;;
    *)      OS=other ;;
  esac
fi

case "${OS}" in
  darwin|linux) ;;
  *) die "os-detect" "unsupported OS '${OS}' (only darwin and linux are supported in v0.1.0)" 2 ;;
esac

# --- Per-OS defaults -------------------------------------------------------
case "${OS}" in
  darwin)
    HUSH_USER="${HUSH_USER:-_hush}"
    HUSH_STATE_DIR="${HUSH_STATE_DIR:-/usr/local/var/hush}"
    SERVICE_NAME=hush.plist
    SERVICE_DST="${HUSH_INSTALL_ROOT}/Library/LaunchDaemons/hush.plist"
    BANNER_SERVICE_PATH="/Library/LaunchDaemons/hush.plist"
    ;;
  linux)
    HUSH_USER="${HUSH_USER:-hush}"
    HUSH_STATE_DIR="${HUSH_STATE_DIR:-/var/lib/hush}"
    SERVICE_NAME=hush.service
    SERVICE_DST="${HUSH_INSTALL_ROOT}/etc/systemd/system/hush.service"
    BANNER_SERVICE_PATH="/etc/systemd/system/hush.service"
    ;;
esac

# --- Validate inputs -------------------------------------------------------
if ! printf '%s' "${HUSH_USER}" | grep -Eq '^[a-zA-Z_][a-zA-Z0-9_-]*$'; then
  die "validate" "HUSH_USER '${HUSH_USER}' is malformed; must match ^[a-zA-Z_][a-zA-Z0-9_-]*$" 2
fi
case "${PREFIX}" in
  /*) ;;
  *) die "validate" "PREFIX '${PREFIX}' must be an absolute path (start with /)" 2 ;;
esac
case "${HUSH_STATE_DIR}" in
  /*) ;;
  *) die "validate" "HUSH_STATE_DIR '${HUSH_STATE_DIR}' must be absolute" 2 ;;
esac
if [ -n "${HUSH_INSTALL_ROOT}" ]; then
  case "${HUSH_INSTALL_ROOT}" in
    /*) ;;
    *) die "validate" "HUSH_INSTALL_ROOT '${HUSH_INSTALL_ROOT}' must be absolute" 2 ;;
  esac
  [ -d "${HUSH_INSTALL_ROOT}" ] || die "validate" "HUSH_INSTALL_ROOT '${HUSH_INSTALL_ROOT}' does not exist" 2
fi
if [ ! -f "${HUSH_SOURCE_BIN}" ]; then
  die "source-binary" "HUSH_SOURCE_BIN '${HUSH_SOURCE_BIN}' not found; build hush first or set HUSH_SOURCE_BIN" 2
fi

# --- Resolved paths --------------------------------------------------------
BIN_PATH="${HUSH_INSTALL_ROOT}${PREFIX}/bin/hush"
STATE_DIR="${HUSH_INSTALL_ROOT}${HUSH_STATE_DIR}"
RESOLVED_BIN_FOR_ACL="${PREFIX}/bin/hush"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
SERVICE_SRC="${SCRIPT_DIR}/${SERVICE_NAME}"

[ -f "${SERVICE_SRC}" ] || die "service-source" "service file '${SERVICE_SRC}' missing from deploy/ tree" 1

# --- Step 1: idempotently ensure ${HUSH_USER} exists -----------------------
# Skipped under HUSH_INSTALL_ROOT (test staging) — the test cannot create
# real system users and the deploy contract does not require it.
ensure_user() {
  if [ -n "${HUSH_INSTALL_ROOT}" ]; then
    return 0
  fi
  case "${OS}" in
    darwin)
      command -v dscl >/dev/null 2>&1 || die "user-create" "dscl not found" 4
      if dscl . -read "/Users/${HUSH_USER}" >/dev/null 2>&1; then
        return 0
      fi
      local next_uid
      next_uid=$(dscl . -list /Users UniqueID 2>/dev/null | awk '{print $2}' | sort -n | tail -1)
      next_uid=$((next_uid + 1))
      dscl . -create "/Users/${HUSH_USER}"                                    || die "user-create" "dscl create ${HUSH_USER} failed" 1
      dscl . -create "/Users/${HUSH_USER}" UserShell /usr/bin/false           || die "user-create" "set shell failed for ${HUSH_USER}" 1
      dscl . -create "/Users/${HUSH_USER}" RealName "hush server"             || die "user-create" "set RealName failed for ${HUSH_USER}" 1
      dscl . -create "/Users/${HUSH_USER}" UniqueID "${next_uid}"             || die "user-create" "set UniqueID failed for ${HUSH_USER}" 1
      dscl . -create "/Users/${HUSH_USER}" PrimaryGroupID 20                  || die "user-create" "set PrimaryGroupID failed for ${HUSH_USER}" 1
      dscl . -create "/Users/${HUSH_USER}" NFSHomeDirectory /var/empty        || die "user-create" "set NFSHomeDirectory failed for ${HUSH_USER}" 1
      ;;
    linux)
      if command -v getent >/dev/null 2>&1 && getent passwd "${HUSH_USER}" >/dev/null 2>&1; then
        return 0
      fi
      command -v useradd >/dev/null 2>&1 || die "user-create" "useradd not found" 4
      useradd --system --shell /usr/sbin/nologin --home-dir /nonexistent --no-create-home "${HUSH_USER}" \
        || die "user-create" "useradd ${HUSH_USER} failed" 1
      ;;
  esac
}
ensure_user

# --- Step 2: vault state directory (0700, owned by ${HUSH_USER}) -----------
if ! mkdir -p "${STATE_DIR}" 2>/dev/null; then
  die "state-dir" "could not create state directory '${STATE_DIR}' (insufficient privilege?)" 3
fi
chmod 0700 "${STATE_DIR}" || die "state-dir" "chmod 0700 failed on '${STATE_DIR}'" 1
if [ -z "${HUSH_INSTALL_ROOT}" ]; then
  chown "${HUSH_USER}" "${STATE_DIR}" || die "state-dir" "chown ${HUSH_USER} failed on '${STATE_DIR}'" 1
fi

# --- Step 3: macOS Time Machine exclusion (Constitution XI non-negotiable) -
# Apple's tmutil is itself idempotent, but the install.sh contract requires
# we invoke addexclusion AT MOST ONCE per state-dir across re-runs. A
# zero-byte marker inside the state dir records first-run completion.
if [ "${OS}" = darwin ]; then
  command -v tmutil >/dev/null 2>&1 || die "tmutil" "tmutil not found; Constitution XI non-negotiable (vault state must never be backed up)" 4
  TMUTIL_MARKER="${STATE_DIR}/.tmutil-excluded"
  if [ ! -e "${TMUTIL_MARKER}" ]; then
    tmutil addexclusion "${STATE_DIR}" >/dev/null 2>&1 \
      || die "tmutil" "tmutil addexclusion failed for '${STATE_DIR}'" 1
    : > "${TMUTIL_MARKER}" || die "tmutil" "could not write exclusion marker '${TMUTIL_MARKER}'" 1
    chmod 0600 "${TMUTIL_MARKER}" 2>/dev/null || true
  fi
fi

# --- Step 4: install binary -----------------------------------------------
mkdir -p "$(dirname "${BIN_PATH}")" 2>/dev/null \
  || die "install-binary" "could not create bin dir '$(dirname "${BIN_PATH}")'" 3
install -m 0755 "${HUSH_SOURCE_BIN}" "${BIN_PATH}" \
  || die "install-binary" "could not copy '${HUSH_SOURCE_BIN}' to '${BIN_PATH}'" 3

# --- Step 5: install service file with @HUSH_USER@ substitution -----------
mkdir -p "$(dirname "${SERVICE_DST}")" 2>/dev/null \
  || die "install-service" "could not create service dir '$(dirname "${SERVICE_DST}")'" 3

case "${OS}" in
  darwin)
    if [ "${HUSH_USER}" = "_hush" ]; then
      install -m 0644 "${SERVICE_SRC}" "${SERVICE_DST}" \
        || die "install-service" "could not copy plist to '${SERVICE_DST}'" 3
    else
      sed "s|<string>_hush</string>|<string>${HUSH_USER}</string>|g" "${SERVICE_SRC}" > "${SERVICE_DST}" \
        || die "install-service" "sed substitution failed on plist" 1
      chmod 0644 "${SERVICE_DST}"
    fi
    ;;
  linux)
    sed "s|@HUSH_USER@|${HUSH_USER}|g" "${SERVICE_SRC}" > "${SERVICE_DST}" \
      || die "install-service" "sed substitution failed on unit" 1
    chmod 0644 "${SERVICE_DST}"
    ;;
esac

# --- Step 6: systemd daemon-reload (Linux real-install only) --------------
if [ "${OS}" = linux ] && [ -z "${HUSH_INSTALL_ROOT}" ]; then
  if command -v systemctl >/dev/null 2>&1; then
    systemctl daemon-reload >/dev/null 2>&1 \
      || die "systemd" "systemctl daemon-reload failed" 1
  fi
fi

# --- Step 7: byte-identical-across-reruns next-steps banner ---------------
case "${OS}" in
  darwin)
    printf '%s\n' \
"hush installed:" \
"  binary:      ${RESOLVED_BIN_FOR_ACL}" \
"  service:     ${BANNER_SERVICE_PATH}" \
"  state dir:   ${HUSH_STATE_DIR}  (0700, owned by ${HUSH_USER}, excluded from Time Machine)" \
"  run-as user: ${HUSH_USER}" \
"" \
"Next steps (run these yourself — install.sh creates no Keychain entries):" \
"" \
"  1. Store the vault passphrase in the macOS Keychain with the binary-path ACL:" \
"       security add-generic-password \\" \
"         -a \"${HUSH_USER}\" -s \"hush-vault-passphrase\" \\" \
"         -T \"${RESOLVED_BIN_FOR_ACL}\" \\" \
"         -U -w \"<YOUR-PASSPHRASE>\"" \
"" \
"  2. Run 'hush init' interactively to create the vault." \
"" \
"  3. Load the daemon:" \
"       sudo launchctl bootstrap system ${BANNER_SERVICE_PATH}" \
"" \
"See docs/CLEAN-MACHINE.md for per-machine client registration."
    ;;
  linux)
    printf '%s\n' \
"hush installed:" \
"  binary:      ${RESOLVED_BIN_FOR_ACL}" \
"  service:     ${BANNER_SERVICE_PATH}" \
"  state dir:   ${HUSH_STATE_DIR}  (0700, owned by ${HUSH_USER})" \
"  run-as user: ${HUSH_USER}" \
"" \
"Next steps:" \
"" \
"  1. Provision the vault passphrase via the operator's chosen secret" \
"     mechanism (systemd LoadCredential, vault-aware launcher, etc.)." \
"     See docs/CLEAN-MACHINE.md." \
"" \
"  2. Run 'hush init' interactively to create the vault." \
"" \
"  3. Enable and start the service:" \
"       sudo systemctl daemon-reload" \
"       sudo systemctl enable --now hush.service" \
"" \
"See docs/CLEAN-MACHINE.md for per-machine client registration."
    ;;
esac
