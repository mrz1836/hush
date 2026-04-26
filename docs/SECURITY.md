# Security

## Threat model

The main enemy is commodity malware scanning for secrets in common file locations.

Examples:
- `~/.zshrc`
- `~/.config/gh/hosts.yml`
- `~/.aws/credentials`
- `.env`
- `*.pem`, `*.key`, `signing.key`

hush exists to remove that attack surface from agent machines.

## Security posture

### Agent machines
- zero secrets at rest
- no dotfile exports
- no gh auth login
- no aws credentials files
- no tool-specific credential stores

### Vault host
- encrypted vault at rest
- derived keys only, no key files
- secrets in protected memory
- approval gate before each fresh session

### Network
- Tailscale-only
- vault never public
- IP-bound session validation

### Crypto layers
- Argon2id
- AES-256-GCM
- BIP32 derivation
- ES256K JWTs
- ECIES secret transport
- ECDSA client request signing

## Daemon-specific security

`hush supervise` exists because daemon restart behavior is a security and reliability issue.

Without a supervisor:
- crashes trigger new approvals repeatedly
- overnight failures become outages
- humans get trained to auto-approve

With a supervisor:
- one approval covers a bounded session
- child restarts do not imply new approval
- stale credentials are surfaced explicitly

## Phase 0 security goal

By end of bootstrap, nobody reading this repo should misunderstand:

- what threat is being solved
- what is out of scope
- why the supervisor model is mandatory for daemons
