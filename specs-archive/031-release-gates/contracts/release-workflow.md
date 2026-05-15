# Contract — `.github/workflows/release.yml`

**Feature**: SDD-31 release gates · **File**: `.github/workflows/release.yml`

## Trigger contract

```yaml
on:
  push:
    tags:
      - 'v*'              # matches v0.1.0, v1.2.3-rc1, etc.
  workflow_dispatch:      # operator escape hatch — must already have a release tag pushed
    inputs:
      tag:
        description: "Tag to release (must already exist)"
        required: true
```

Rationale:
- Tag push is the canonical trigger (FR-025 — "the GitHub Actions OIDC identity of the release workflow on the release tag ref"). The OIDC subject claim binds to the ref so Sigstore Fulcio issues a certificate whose `Subject Alternative Name` includes the tag ref — making cosign verification straightforwardly checkable by consumers.
- `workflow_dispatch` is the rerun lever if a transient upload fails after the tag was pushed; never used for tag-less builds.

## Permissions

```yaml
permissions:
  contents: write    # GitHub Release creation (GoReleaser writes here)
  id-token: write    # OIDC token for cosign keyless (Sigstore Fulcio)
  packages: write    # optional — only needed if release.yml ever pushes images (not in v1)
```

`id-token: write` is the load-bearing permission — without it cosign cannot mint a Fulcio cert (FR-025 lock).

## Concurrency

```yaml
concurrency:
  group: release-${{ github.ref }}
  cancel-in-progress: false   # NEVER cancel a release mid-flight
```

## Job: `release`

```yaml
jobs:
  release:
    runs-on: ubuntu-24.04
    env:
      CGO_ENABLED: "0"   # FR-023 lock; .goreleaser.yml also sets this — defence in depth
    steps:
      - uses: actions/checkout@v4
        with: { fetch-depth: 0 }   # GoReleaser needs full history for changelog

      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
          cache: true

      - uses: sigstore/cosign-installer@v3
        with:
          cosign-release: 'v2.4.0'   # pin to a tested version at implement-phase

      - name: install magex
        run: go install github.com/mrz1836/magex/cmd/magex@<pinned>

      # ── Race + lint re-applied per FR-026 ───────────────────────────────
      # GoReleaser's `before.hooks` (in .goreleaser.yml) runs `magex test`
      # which has the race detector on by default — no explicit step needed
      # here because the discipline is enforced by .goreleaser.yml itself
      # (this contract just promises that .goreleaser.yml's before hook
      #  matches the per-PR test bar). FR-026 lock.

      - name: GoReleaser
        uses: goreleaser/goreleaser-action@v6
        with:
          version: latest          # GoReleaser action self-pins minor; pin major in implement-phase
          args: release --clean
        env:
          GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}
```

## Artefacts produced (FR-024 lock)

| File pattern                                  | Purpose |
|-----------------------------------------------|---------|
| `hush_<ver>_darwin_amd64.tar.gz`              | release binary archive (darwin/amd64) |
| `hush_<ver>_darwin_arm64.tar.gz`              | release binary archive (darwin/arm64) |
| `hush_<ver>_linux_amd64.tar.gz`               | release binary archive (linux/amd64) |
| `hush_<ver>_linux_arm64.tar.gz`               | release binary archive (linux/arm64) |
| `hush_<ver>_checksums.txt`                    | SHA-256 manifest covering all four |
| `hush_<ver>_checksums.txt.sig`                | cosign signature of the manifest (FR-025) |
| `hush_<ver>_checksums.txt.pem`                | Fulcio cert (cert is verifier's anchor; Rekor entry is automatic) |

## `.goreleaser.yml` edits required by this workflow

The release workflow assumes `.goreleaser.yml` is updated to:

1. Promote `env: [CGO_ENABLED=0]` to top-level (currently only on builds[0]) — FR-023 belt-and-braces.
2. Change `builds[0].goos` from `[darwin]` to `[darwin, linux]` — FR-024.
3. Keep `builds[0].goarch: [amd64, arm64]` — already present.
4. Add a `signs:` block (cosign keyless via OIDC):

```yaml
signs:
  - cmd: cosign
    signature: "${artifact}.sig"
    certificate: "${artifact}.pem"
    args:
      - sign-blob
      - "--yes"
      - "--output-signature=${signature}"
      - "--output-certificate=${certificate}"
      - "${artifact}"
    artifacts: checksum     # only sign SHA256SUMS, not every binary
```

5. (Optional, deferred) Reduce the `before.hooks` to `magex test` (already there) — confirm race detector stays on.

## Failure semantics (FR-027 — fail closed)

- Any non-zero exit in `magex test` (race detector failure, lint regression bypassed somehow) → GoReleaser aborts → release fails closed → no partial publication.
- Any non-pure-Go build target sneaking in (e.g. an `import "C"` slipping through ci.yml's no-CGO gate) → `CGO_ENABLED=0` fails the Go compile → release fails closed.
- Cosign keyless failure (Fulcio unavailable, OIDC token denied) → `signs:` step fails → release fails closed (no unsigned manifest published).

## Verification (consumer-side)

Per SC-006 — a release consumer verifies:

```sh
# 1. Fetch artefact + signature + cert + manifest
curl -LO https://github.com/<org>/hush/releases/download/v0.1.0/hush_0.1.0_checksums.txt
curl -LO https://github.com/<org>/hush/releases/download/v0.1.0/hush_0.1.0_checksums.txt.sig
curl -LO https://github.com/<org>/hush/releases/download/v0.1.0/hush_0.1.0_checksums.txt.pem

# 2. Verify the signature against the cert + Rekor log
cosign verify-blob \
  --certificate hush_0.1.0_checksums.txt.pem \
  --signature   hush_0.1.0_checksums.txt.sig \
  --certificate-identity-regexp '^https://github.com/<org>/hush/.github/workflows/release.yml@refs/tags/v.*$' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  hush_0.1.0_checksums.txt

# 3. Then verify the binary's SHA matches the manifest entry
sha256sum -c hush_0.1.0_checksums.txt --ignore-missing
```

Both 2 and 3 must succeed for the release to be considered authenticated. The Rekor inclusion proof is checked automatically by `cosign verify-blob`.

## Out-of-contract

- Not bundling SBOMs (deferred).
- Not signing every binary individually (FR-025 spec lock — manifest-only signing).
- Not auto-promoting to package managers (homebrew tap, etc.) — deferred to a future chunk.
