# Quickstart — SDD-29 Deploy Artifacts

This is the operator-facing "how do I use this chunk" walkthrough. It
mirrors `docs/CLEAN-MACHINE.md` but anchored on the four files this
chunk delivers.

---

## 1. Build the binary

```sh
magex build           # produces ./hush in repo root
```

`install.sh` reads `HUSH_SOURCE_BIN` (default `./hush`) — running the
installer from the repo root works without any flag.

---

## 2. First install (macOS)

```sh
sudo ./deploy/install.sh
```

What happens, in order:
1. install.sh detects macOS via `uname`.
2. Creates the `_hush` system account if missing (`dscl . -create`).
3. `install -d -m 0700 -o _hush /usr/local/var/hush`.
4. `tmutil addexclusion /usr/local/var/hush`.
5. `install -m 0755 ./hush /usr/local/bin/hush`.
6. `install -m 0644 deploy/hush.plist /Library/LaunchDaemons/hush.plist`.
7. Prints the next-steps banner (see [data-model.md §4](data-model.md)).

You then run the banner's `security add-generic-password -T
/usr/local/bin/hush ...` command yourself to populate the Keychain.
install.sh **never** touches the Keychain (FR-003).

Finally:
```sh
sudo launchctl bootstrap system /Library/LaunchDaemons/hush.plist
```

---

## 3. First install (Linux)

```sh
sudo ./deploy/install.sh
```

1. install.sh detects Linux via `uname`.
2. Creates the `hush` system account if missing
   (`useradd --system --shell /usr/sbin/nologin --no-create-home hush`).
3. `install -d -m 0700 -o hush /var/lib/hush`.
4. `install -m 0755 ./hush /usr/local/bin/hush`.
5. Substitutes `@HUSH_USER@` → `hush` while copying
   `deploy/hush.service` to `/etc/systemd/system/hush.service`.
6. `systemctl daemon-reload`.
7. Prints the next-steps banner.

You then arrange passphrase delivery via your chosen mechanism
(systemd `LoadCredential`, vault-aware launcher script, etc. — see
`docs/CLEAN-MACHINE.md`) and:
```sh
sudo systemctl enable --now hush.service
```

---

## 4. Re-running install.sh (upgrades, debugging)

`install.sh` is idempotent. A second run:
- exits 0;
- emits byte-identical stdout;
- does NOT re-create the `${HUSH_USER}` account;
- does NOT invoke `tmutil addexclusion` a second time on macOS;
- replaces the binary in-place if the source binary changed (mode and
  owner preserved at `0755`, `root`);
- leaves the service file untouched if its content is unchanged.

`magex test:race -tags=integration -run TestDeploy_InstallIdempotent
./tests/deploy/...` proves all of the above runs in CI on every PR.

---

## 5. Deploying a long-running daemon under the supervisor

`install.sh` does NOT touch `deploy/supervise-launch.sh.template`.
You — the operator — copy it once per daemon and fill in the
placeholders.

```sh
cp deploy/supervise-launch.sh.template ~/.hush/launchers/openclaw-hush-launch.sh
chmod +x ~/.hush/launchers/openclaw-hush-launch.sh

# Edit the file: replace <NAME>, <KEYCHAIN_ITEM>, <CONFIG_PATH>
# with the daemon's logical name, its Keychain item name, and the
# absolute path to its supervisor TOML.
```

Then register the customised script with launchd (macOS) or systemd
(Linux) via a per-daemon plist or unit file (out of SDD-29 scope —
operator responsibility per `docs/DAEMONS.md` §8).

**If you forget to substitute a placeholder**, the pre-flight guard
inside the template detects it and exits 78 (`EX_CONFIG`). The init
system records the exit code; check `launchctl print` or
`journalctl -u <name>.service` to see the error.

---

## 6. Verifying the install

Static checks anyone can run after install:

```sh
ls -l /usr/local/bin/hush                       # 0755, root-owned
ls -ld /usr/local/var/hush                      # 0700, _hush-owned (macOS)
ls -l /Library/LaunchDaemons/hush.plist         # 0644, root:wheel
sudo launchctl list | grep com.hush.server      # loaded
tmutil isexcluded /usr/local/var/hush           # "Excluded from backup"
```

On Linux:
```sh
ls -l /usr/local/bin/hush                       # 0755, root-owned
ls -ld /var/lib/hush                            # 0700, hush-owned
ls -l /etc/systemd/system/hush.service          # 0644, root-owned
systemctl status hush.service                   # active (running)
```

---

## 7. CI gate (what runs on every PR)

```sh
magex format:fix && magex lint                  # Go formatting + lint
bash -n deploy/install.sh                       # parser pass
bash -n deploy/supervise-launch.sh.template     # parser pass
shellcheck deploy/install.sh deploy/supervise-launch.sh.template  # if available
magex test:race -tags=integration -run TestDeploy_ ./tests/deploy/...
```

A green CI run guarantees:
- All four `deploy/*` files parse.
- install.sh is idempotent in `t.TempDir()`.
- The banner contains the correct `-T "<binary-path>"` invocation.
- The launcher template uses `hush supervise` and contains no active
  `hush request --exec`.
- No operator-specific names are committed under `deploy/`.

---

## 8. Anti-patterns (do NOT do these)

- **Do NOT** run install.sh as a non-root user without `HUSH_INSTALL_ROOT`
  set — it will fail at step 5 (write to `/usr/local/bin`) and exit 3.
- **Do NOT** use `hush request --exec` in any per-daemon launcher.
  Re-prompts every restart. Defeats Constitution IV. The launcher
  template's pre-flight guard does NOT catch this — the operator must
  not introduce it.
- **Do NOT** back up `${HUSH_STATE_DIR}` (`/usr/local/var/hush` or
  `/var/lib/hush`). It contains the vault. Constitution XI non-negotiable.
- **Do NOT** edit the committed `deploy/hush.plist` or
  `deploy/hush.service` in your fork to add an operator-specific value.
  Operator-specific values belong in a per-daemon launcher (SDD-30
  territory), not in this chunk's files.
