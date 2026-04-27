# SDD-15 — `hush init` (server + client modes; macOS Keychain ACL integration)

**Phase:** 4
**Package:** `internal/cli` + `internal/keychain`
**Files:** `internal/cli/init.go`, `internal/keychain/{keychain.go, keychain_darwin.go, keychain_linux.go, *_test.go}`
**Branch:** `015-init-and-keychain` (created by the `before_specify` git hook)
**Blocked by:** SDD-01, SDD-03, SDD-14
**Blocks:** SDD-16, SDD-29
**Primary AC:** AC-1, AC-6
**Coverage target:** 85%

**Behaviour contracts (MUST):**
- Passphrase ≥ 12 chars (Constitution Security Requirements)
- Salt is `crypto/rand` 16 bytes
- Server mode writes `config.toml` mode `0600` with all defaults from `docs/CONFIG-SCHEMA.md`
- Bot token + vault passphrase stored via `security add-generic-password -s hush-discord -a hush -T /usr/local/bin/hush -w <token>` on macOS (or test-injectable equivalent — for tests, an in-process fake Keychain)
- Client mode requires `--machine-index` flag; conflicts with server mode
- Print public key fingerprint to stdout in copy-pasteable format

**Anti-contracts (MUST NOT):**
- Read passphrase from env var or arg
- Skip the `-T` ACL flag on macOS
- Generate a passphrase for the user

**Tests required:**
- Unit: `TestInitServer_RefusesShortPassphrase`, `TestInitServer_CreatesVaultWith0600`, `TestInitServer_CreatesConfigWithAllDefaults`, `TestInitClient_RequiresMachineIndex`, `TestInitClient_StoresInKeychainWithACL` (skip-if-not-darwin), `TestInitClient_PrintsFingerprint`
- Integration: full init dance in `t.TempDir`
- macOS-specific tests skip on linux via build tags

**Constitutional principles in scope:** I (operator-agnostic), III (Argon2id parameters), VII (cobra-only), Security Requirements (passphrase length, Keychain ACLs)

**Exported API to lock in PACKAGE-MAP.md (this chunk):**
- internal/cli: subcommand `init` (registered via package side-effect in cli.Execute)
- internal/keychain (NEW package):
  - `type Keychain interface { Store(ctx context.Context, service, account string, value *securebytes.SecureBytes, acl string) error; Retrieve(ctx context.Context, service, account string) (*securebytes.SecureBytes, error); Delete(ctx context.Context, service, account string) error }`
  - `func New(logger *slog.Logger) (Keychain, error)`  — picks darwin or linux impl
  - `var ErrKeychainItemNotFound, ErrKeychainPermissionDenied`

---

## How to run this chunk

Run **5 separate Claude Code sessions**, one per prompt below. The
`extensions.yml` hooks auto-commit each artifact (accept in Prompts 1,
3, 4; conditionally in Prompt 2; **decline** in Prompt 5).

---

## Prompt 1 — Specify  (fresh session)

```
You are running the SPECIFY phase of SDD-15 (hush init: server +
client modes + Keychain ACL integration) of the hush project.

Read first (in order):
- /Users/mrz/projects/hush/.specify/memory/constitution.md  (Principles I, III, VII; Security Requirements — Keychain ACLs are non-negotiable)
- /Users/mrz/projects/hush/docs/SPEC.md  (FR-3, FR-22, AC-1, AC-6)
- /Users/mrz/projects/hush/docs/SECURITY.md  (Keychain ACLs section)
- /Users/mrz/projects/hush/docs/CONFIG-SCHEMA.md  (server defaults — every field needs a default in the generated config)
- /Users/mrz/projects/hush/docs/AC-MATRIX.md  (current AC-1/6 row state)
- /Users/mrz/projects/hush/docs/sdd/SDD-15.md  (the full chunk contract)

About this chunk (one-paragraph intent, for the spec's overview):
hush init bootstraps a fresh installation in two distinct modes:
server (creates the encrypted vault, the server config.toml, and
stores the Discord bot token + vault passphrase in the OS keychain)
and client (derives the per-machine client key and stores it in the
keychain, then prints the public-key fingerprint for the operator
to paste into the server's allow list). Both modes use OS keychain
items with a hush-binary-only ACL so other processes cannot read
them.

The spec MUST encode these acceptance-level (WHAT) requirements.
Override any /speckit-specify "informed guess" that would soften
them:

- The vault passphrase MUST be at least 12 characters; a shorter
  passphrase is rejected with a distinct error.
- The salt for Argon2id derivation is 16 bytes from crypto/rand.
- Server mode writes config.toml with file mode 0600, populated
  with every default from docs/CONFIG-SCHEMA.md.
- Both modes store secrets in the OS keychain with an ACL that
  restricts read access to the hush binary path. On macOS this
  is the security CLI's -T flag.
- Client mode requires an explicit --machine-index flag and
  cannot be combined with server mode (mutually exclusive).
- After client mode, the public key fingerprint is printed to
  stdout in a copy-pasteable single line for operator workflow.
- The init command MUST NEVER read the passphrase from an env
  var or command-line arg.
- The init command MUST NEVER generate a passphrase for the
  operator (operator entropy is intentional).

The spec MUST NOT encode HOW (no library names, no specific
syscalls). Those are plan-phase.

Acceptance criteria: AC-1 (server CLI surface), AC-6 (Keychain ACL
enforcement).

Action — run exactly one command:
  /speckit-specify "hush init: server mode (creates vault + config + keychain entries with hush-binary-only ACL) and client mode (--machine-index, derives client key, stores in keychain, prints fingerprint); rejects passphrase < 12 chars; never reads passphrase from env or arg; never generates a passphrase for the operator"

The before_specify hook will create branch 015-init-and-keychain.

If /speckit-specify produces [NEEDS CLARIFICATION] markers, check
each against the chunk contract / constitution. Otherwise leave
the marker — /speckit-clarify will handle it next session.

When the after_specify hook offers to auto-commit spec.md, accept.
```

---

## Prompt 2 — Clarify  (fresh session)

```
You are running the CLARIFY phase of SDD-15 of the hush project.

Read /Users/mrz/projects/hush/docs/sdd/SDD-15.md.

Run: /speckit-clarify

Accept the after_clarify auto-commit only if spec.md actually changed.
```

---

## Prompt 3 — Plan  (fresh session)

```
You are running the PLAN phase of SDD-15 (hush init + Keychain) of
the hush project.

Read first (in order):
- /Users/mrz/projects/hush/.specify/memory/constitution.md  (full file — /speckit-plan runs a Constitution Check; I/III/VII/Security Requirements load-bearing)
- /Users/mrz/projects/hush/docs/SPEC.md  (FR-3, FR-22, AC-1, AC-6)
- /Users/mrz/projects/hush/docs/SECURITY.md  (Keychain ACLs — security CLI's -T flag is the locked mechanism on macOS)
- /Users/mrz/projects/hush/docs/CONFIG-SCHEMA.md  (every default this command must write)
- /Users/mrz/projects/hush/docs/PACKAGE-MAP.md  (internal/cli + new internal/keychain)
- /Users/mrz/projects/hush/docs/sdd/SDD-15.md  (the full chunk contract)

The plan MUST honour every item below. /speckit-plan runs a
Constitution Check — if it fires, fix the plan, do NOT bypass.

Scope:
- internal/cli/init.go (server + client subcommands)
- internal/keychain (NEW package): keychain.go (interface),
  keychain_darwin.go (security CLI shellout), keychain_linux.go
  (zalando/go-keyring wrapper), keychain_test.go,
  keychain_darwin_test.go (//go:build darwin),
  keychain_linux_test.go (//go:build linux)

Implementation contract (HOW — locked):
- Keychain interface:
    type Keychain interface {
        Store(ctx context.Context, service, account string, value *securebytes.SecureBytes, acl string) error
        Retrieve(ctx context.Context, service, account string) (*securebytes.SecureBytes, error)
        Delete(ctx context.Context, service, account string) error
    }
    func New(logger *slog.Logger) (Keychain, error)
- darwin impl: shells out to /usr/bin/security via os/exec:
    security add-generic-password -s <service> -a <account>
             -T <acl> -w <secret-bytes-from-Use(fn)>
  The -T flag receives the absolute path to the hush binary
  (caller passes via cli init). Read SecureBytes via Use(fn);
  write to security stdin to avoid argv exposure.
- linux impl: github.com/zalando/go-keyring. Constitution XI:
  this is the chosen Linux backend; document in plan's research.md.
- init.go subcommand structure:
    hush init server   — interactive passphrase prompt (TTY-only,
                         re-prompt if < 12 chars), generates 16B
                         salt, derives keys via SDD-01, calls
                         vault.Save (SDD-03), stores bot token
                         (prompted) + vault passphrase in keychain,
                         writes config.toml with all CONFIG-SCHEMA
                         defaults at mode 0600.
    hush init client --machine-index <N> — derives the per-machine
                         client key via SDD-01, stores in keychain
                         under (hush-client, machine-<N>), prints
                         public-key fingerprint.
- Both subcommands compute the binary's absolute path via
  os.Executable() and pass it as the ACL.
- Tests use an in-process fake Keychain for cross-platform
  coverage; macOS-specific tests verify the actual security
  command was constructed correctly.

Coverage target: 85%.
Constitutional principles in scope: I, III, VII, IX, Security
Requirements (passphrase length + Keychain ACL).

Run: /speckit-plan

Accept the after_plan auto-commit.
```

---

## Prompt 4 — Tasks  (fresh session)

```
You are running the TASKS phase of SDD-15 of the hush project.

Read /Users/mrz/projects/hush/docs/sdd/SDD-15.md.

Run:
  /speckit-tasks "TDD-mandatory per Constitution VIII: include a test-writing task for every behaviour contract BEFORE the implementation task. Coverage target: 85%. Tests required: TestInitServer_RefusesShortPassphrase, TestInitServer_CreatesVaultWith0600, TestInitServer_CreatesConfigWithAllDefaults (assert every field from docs/CONFIG-SCHEMA.md), TestInitServer_StoresBotTokenInKeychain, TestInitServer_NeverReadsPassphraseFromEnv, TestInitClient_RequiresMachineIndex, TestInitClient_StoresInKeychainWithACL (//go:build darwin — verify -T flag in constructed security command), TestInitClient_PrintsFingerprintOneLine, TestInitClient_ConflictsWithServerMode, TestKeychain_StoreRetrieveRoundTrip (in-process fake), TestKeychain_DeleteRemoves, TestKeychainDarwin_ConstructedSecurityCommand (//go:build darwin), TestKeychainLinux_ZalandoBackend (//go:build linux). Integration test: full init dance in t.TempDir. Final phase MUST include magex format:fix, magex lint, magex test:race, and magex test:race -tags=integration."

Accept the after_tasks auto-commit.
```

---

## Prompt 5 — Implement  (fresh session)

```
You are running the IMPLEMENT phase of SDD-15 of the hush project.

Read /Users/mrz/projects/hush/docs/sdd/SDD-15.md.

Run: /speckit-implement

After /speckit-implement completes, do these steps from repo root:

1. Gates (all must pass clean):
     magex format:fix && magex lint && magex test:race
2. Integration tests:
     magex test:race -tags=integration
3. Verify coverage ≥ 85% on internal/cli (init portion) and
   internal/keychain:
     go test -cover ./internal/cli/ -run Init
     go test -cover ./internal/keychain/
4. Manual smoke on macOS (if available): hush init server creates
   a vault that hush serve can open; the security command for
   the bot token includes "-T /usr/local/bin/hush" (or the
   resolved binary path).
5. Confirm `init` twice in a tempdir produces a deterministic
   tree structure (no hidden state between runs).
6. Append "Exported API — locked at SDD-15" section to
   docs/PACKAGE-MAP.md:
     - internal/cli: note the init subcommand registration
     - NEW entry for internal/keychain listing the locked API
       (Keychain, New, ErrKeychainItemNotFound,
       ErrKeychainPermissionDenied)
7. Update docs/AC-MATRIX.md AC-1, AC-6 rows with the new test
   file paths.
8. Mark SDD-15 status `done` in docs/SDD-PLAYBOOK.md.

DECLINE the after_implement auto-commit. Make one combined commit
instead:
  git add internal/cli/ internal/keychain/ docs/PACKAGE-MAP.md \
          docs/AC-MATRIX.md docs/SDD-PLAYBOOK.md \
          specs/<feature-dir>/tasks.md
  git commit -m "feat(cli,keychain): hush init server/client + Keychain ACL wrapper (SDD-15)"

Final message: confirm gates passed (unit + integration), race-
clean, coverage ≥ 85%, init twice produces deterministic tree,
Keychain ACL flag verified, AC-1 + AC-6 rows updated,
SDD-PLAYBOOK updated, and the combined commit created.
```
