# CLAUDE.md

See [.github/CLAUDE.md](.github/CLAUDE.md) for the canonical agent guide.

<!-- SPECKIT START -->
Active feature plan: [specs/012-server-claim-handler/plan.md](specs/012-server-claim-handler/plan.md) — SDD-12 `internal/server` `POST /h/<prefix>/claim` handler. Pipeline: shape → CanonicalJSON+Verify (SDD-08) → NonceCache → IsFreshTimestamp → IP allowlist → TTL cap (`min(req.TTL, cfg.Crypto.MaxInteractive/SupervisorTTL)`) → `Approver.RequestApproval` → `token.Issue` → audit. Status map (locked): 200 `{jwt,expires_at,jti}` / 400 bad_request / 403 bad_signature|nonce_replay|stale_timestamp|ip_not_allowed|denied / 408 approval_timeout / 429 rate_limited / 503 discord_unavailable|unknown_outcome. Adds `Crypto.ClaimApprovalTimeout` (default 60 s) to config, optional `Deps.ClientKeyResolver`, five chassis sentinels (`ErrApproverDenied/Timeout/Unavailable/RateLimited`, `ErrClientUnknown`), `(s *Server).RegisterHandlers()`. Constitution II: zero auto-approve knobs. Sentinel-leak test asserts `reason` never appears in response bodies or operational logs. AC-1/3/4. Coverage target 95%.
<!-- SPECKIT END -->
