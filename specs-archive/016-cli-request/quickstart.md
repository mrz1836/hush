# Quickstart: `hush request`

**Feature**: SDD-16
**Branch**: `016-cli-request`
**Audience**: operators (Z) — running `hush request` after `hush init server` and `hush init client` are complete.

This document is a runnable cheat-sheet. The detailed flag table and
contract live in [contracts/cli-request.md](./contracts/cli-request.md).

---

## Prerequisites

1. **Server side** — `hush init server` has been run on the trusted
   host; `hush serve` is running and reachable on the Tailscale
   address.
2. **Client side** — `hush init client --machine-index N` has been run
   on **this** machine; the integer `N` is the value you'll pass to
   `--machine-index`.
3. The fingerprint emitted by `hush init client` has been registered
   server-side (via the operator's deployment workflow — out of scope
   for this chunk).

---

## Workflow 1 — wrap a shell with two secrets (`--exec`, recommended)

```bash
hush request \
  --server https://100.97.178.13:7743/h/abc123def \
  --scope ANTHROPIC_API_KEY,GITHUB_TOKEN \
  --reason "starting work session" \
  --ttl 8h \
  --max-uses 50 \
  --machine-index 0 \
  --exec /bin/zsh
```

What happens:

1. The command builds a signed `/claim` request and waits for your
   Discord approval (up to `--ttl`).
2. You tap **Approve** on your phone.
3. `zsh` starts with `ANTHROPIC_API_KEY` and `GITHUB_TOKEN` in its
   environment. No values are printed to your terminal scrollback.
4. When `zsh` exits (you type `exit`), the parent zeroes the
   ephemeral key + JWT and exits with the shell's exit code.

**Why this is the safe path.** The secrets exist only in the child
process's environment block — never in your terminal scrollback,
shell history, or any file.

---

## Workflow 2 — load a one-off secret into the current shell (`--format eval`)

```bash
eval "$(hush request \
  --server https://100.97.178.13:7743/h/abc123def \
  --scope GITHUB_TOKEN \
  --reason "ad-hoc gh call" \
  --ttl 15m \
  --max-uses 1 \
  --machine-index 0 \
  --format eval)"
```

What happens:

1. Same `/claim` + Discord approval as Workflow 1.
2. `hush request` writes one `export GITHUB_TOKEN='...'` line to
   stdout — captured by `eval` and applied to your current shell.
3. **A WARNING is printed to stderr** that you'll see in your
   terminal even though stdout is being piped to `eval`:

   ```
   WARNING: --format eval prints secret values to stdout. They may be captured by terminal scrollback, tmux, or script. Use --exec whenever possible.
   ```

**Use this only when `--exec` won't work** (e.g. you want the value in
your interactive shell session that's already running). The value will
be visible in your terminal's scroll buffer until you clear it; in
`script` recordings; in some `tmux` configurations.

---

## Workflow 3 — pass arguments to the child program

```bash
hush request \
  --server https://100.97.178.13:7743/h/abc123def \
  --scope GITHUB_TOKEN \
  --reason "test gh release" \
  --ttl 5m \
  --max-uses 1 \
  --machine-index 0 \
  --exec /usr/bin/gh -- release list --limit 3
```

Everything after `--` becomes the child's `argv[1:]` verbatim, in
order. The `--exec` value is the program path only (resolved through
`PATH`). There is **no shell parsing** of the `--exec` value or the
positional argv — characters like `*` and `$` are passed through
literally.

---

## What happens when things go wrong

| Situation | What you see | Exit code |
|-----------|--------------|-----------|
| You forget `--exec` and `--format` | `hush: request: must specify --exec or --format eval` | 2 |
| You set both | `hush: request: --exec and --format eval are mutually exclusive` | 2 |
| `--format json` | `hush: request: --format only accepts the literal value "eval"` | 2 |
| `--max-uses 1 --scope A,B,C` | `hush: request: --max-uses must be ≥ number of scopes` | 2 |
| You haven't run `hush init client --machine-index N` | `hush: request: client key not found in keychain — run \`hush init client --machine-index <N>\` first` | 1 |
| Server unreachable | `hush: request: could not connect to hush server at <url>: connection refused` | 1 |
| You tap **Deny** on Discord | `hush: request: approval denied on Discord` | 3 |
| You wait too long (no Discord tap) | `hush: request: approval wait exceeded --ttl` | 1 |
| The vault doesn't hold one of the requested scopes | `hush: request: secret "X" not present in vault; aborting before child start` | 4 |
| You hit Ctrl-C during the wait | `hush: request: interrupted; pending request will expire server-side at --ttl` | 1 |

In every failure case: no secret bytes are decrypted, no child program
is started, no JWT is written anywhere.

---

## Verification — "did anything leak?"

After a successful `--exec` run, you can verify the lifecycle
property by hand:

```bash
# 1. No secret value in any file in your homedir
grep -rE 'sk-(ant|proj)|ghp_|AKIA' ~ 2>/dev/null
# (expected: no matches — same posture as before hush request ran)

# 2. No JWT in any file
grep -rE 'eyJ[A-Za-z0-9_-]{16,}' ~ 2>/dev/null
# (expected: no matches in hush state directory)
```

If either grep hits something `hush request` produced, file a bug —
it would be a constitutional violation (Principle I + Principle X).
