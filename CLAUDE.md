# CLAUDE.md

See [.github/CLAUDE.md](.github/CLAUDE.md) for the canonical agent guide.

<!-- SPECKIT START -->
Active feature plan: [specs/010-server-skeleton/plan.md](specs/010-server-skeleton/plan.md) — SDD-10 `internal/server` (HTTP chassis: stdlib `net/http.ServeMux` at `/h/<prefix>/...`, ordered startup checks `clock_sync → file_modes → tailscale_bind → state_dir`, SIGHUP atomic vault reload with 30s drain window, middleware request ID → IP allow-list → panic recover (no body in logs); AC-1 / AC-2 / AC-8).
<!-- SPECKIT END -->
