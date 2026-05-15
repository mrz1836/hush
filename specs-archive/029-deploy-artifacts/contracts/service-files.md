# Contract — launchd plist + systemd unit file shape

This document fixes the static structure of `deploy/hush.plist` and
`deploy/hush.service`. Both files are committed artefacts. `install.sh`
copies them into the platform service location and (for the systemd
unit) performs a single substitution.

---

## `deploy/hush.plist` — committed content

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN"
                       "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>             <string>com.hush.server</string>
  <key>ProgramArguments</key>  <array>
    <string>/usr/local/bin/hush</string>
    <string>serve</string>
    <string>--config</string>
    <string>/usr/local/etc/hush/config.toml</string>
  </array>
  <key>UserName</key>          <string>_hush</string>
  <key>RunAtLoad</key>         <true/>
  <key>KeepAlive</key>         <true/>
  <key>StandardOutPath</key>   <string>/usr/local/var/log/hush.out.log</string>
  <key>StandardErrorPath</key> <string>/usr/local/var/log/hush.err.log</string>
</dict>
</plist>
```

**Asserted by `TestDeploy_PlistParsesAsXML`:**
- File parses with `encoding/xml` (FR-012).
- `<key>UserName</key>` exists and its sibling `<string>` is not `root`
  and not `0` (FR-010 + SC-005).
- `<key>ProgramArguments</key>`'s array contains
  `/usr/local/bin/hush` as the first element (FR-011).
- File contains zero operator-specific tokens — a grep against a
  denylist (`mrz`, `openclaw`, `hermes`, `tail*-tag-*`, etc.) returns
  zero matches (FR-013 + SC-007).

**install.sh substitution.** If `${HUSH_USER}` differs from the literal
`_hush`, install.sh sed-replaces `<string>_hush</string>` with
`<string>${HUSH_USER}</string>` during the copy. The committed file
content is unchanged.

---

## `deploy/hush.service` — committed content

```ini
[Unit]
Description=hush — Discord-gated secrets broker
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=@HUSH_USER@
ExecStart=/usr/local/bin/hush serve --config /etc/hush/config.toml
Restart=on-failure
RestartSec=5s
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true

[Install]
WantedBy=multi-user.target
```

**Asserted by `TestDeploy_ServiceParsesAsINI`:**
- File parses as an INI-style structure with `[Unit]`, `[Service]`,
  `[Install]` sections (FR-016).
- `[Service]` contains `User=` with a value that is neither `root` nor
  `0` and that is the literal substitution token `@HUSH_USER@` in the
  committed file (FR-014 + SC-005).
- `ExecStart=` begins with `/usr/local/bin/hush` (FR-015).
- File contains zero operator-specific tokens (FR-017 + SC-007).

**install.sh substitution.** Exactly one sed at copy time replaces
`@HUSH_USER@` → `${HUSH_USER}`. The destination is
`/etc/systemd/system/hush.service`.

---

## Cross-OS invariants (FR-011 + FR-015)

Both files reference `/usr/local/bin/hush` as the absolute binary
path. install.sh places the binary at `${PREFIX}/bin/hush`; the default
`PREFIX=/usr/local` agrees with the service-file constant. If an
operator overrides `PREFIX`, the service files require manual editing
to match — this is documented in the banner's "advanced installation"
footnote.

For v0.1.0, the chunk-doc-locked path `/usr/local/bin/hush` is fixed in
both service files. Operator-overridden prefix without matching
service-file edits is an operator footgun, explicitly out of SDD-29
scope.
