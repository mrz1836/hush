package config

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/netip"
	"os"
	"path/filepath"
	"time"

	"github.com/pelletier/go-toml/v2"
)

// ---- Public types -----------------------------------------------------------

// Server is the fully-materialized server configuration. It is read-only after
// LoadServer returns; consumers MUST NOT mutate any field.
type Server struct {
	Server   ServerSection
	Discord  DiscordSection
	Crypto   CryptoSection
	Network  NetworkSection
	Security SecuritySection

	// rawListenAddr and rawHealthBind carry the original TOML strings so that
	// Validate can produce precise error messages. They are unexported and not
	// part of the locked API surface.
	rawListenAddr string
	rawHealthBind string
}

// ServerSection holds the [server] TOML table.
type ServerSection struct {
	ListenAddr               netip.AddrPort // pre-parsed; Tailscale CGNAT validated
	PathPrefix               string         // [A-Za-z0-9_-]{6,32}
	StateDir                 string         // absolute, ~-expanded, must exist + be a directory
	AuditLog                 string         // absolute, ~-expanded, must be under StateDir
	DiscordOwnerID           string         // Discord snowflake (non-secret)
	ClientRegistry           string         // absolute, ~-expanded
	DiscordApprovalChannelID string         // optional; empty == approve by DM
	DiscordAuditChannelID    string         // optional; empty == not configured
}

// DiscordSection holds the [discord] TOML table.
// BotTokenKeychainItem is a Keychain item NAME, not the token itself (Constitution X).
type DiscordSection struct {
	BotTokenKeychainItem string // e.g. "hush-discord" — Keychain item name only
	ApplicationID        string // Discord app/bot ID — non-secret snowflake
}

// CryptoSection holds the [crypto] TOML table.
type CryptoSection struct {
	ArgonTime            uint32
	ArgonMemoryMB        uint32
	ArgonThreads         uint8
	JWTDefaultTTL        time.Duration
	MaxInteractiveTTL    time.Duration
	MaxSupervisorTTL     time.Duration
	DefaultMaxUses       int
	NonceTTL             time.Duration
	ClockSkew            time.Duration
	ClaimApprovalTimeout time.Duration
}

// NetworkSection holds the [network] TOML table.
type NetworkSection struct {
	RequireTailscale bool
	AllowedCIDRs     []string
	HealthBind       netip.AddrPort // inherits from ListenAddr when absent in TOML
}

// SecuritySection holds the [security] TOML table.
type SecuritySection struct {
	RequireFileModeChecks bool
	RequireKeychainACL    bool
	RequireNTPSync        bool
	MaxClockDrift         time.Duration
}

// ---- Wire-shape (decoded) types — INTERNAL ----------------------------------

type serverDecoded struct {
	Server   serverSectionDecoded   `toml:"server"`
	Discord  discordSectionDecoded  `toml:"discord"`
	Crypto   cryptoSectionDecoded   `toml:"crypto"`
	Network  networkSectionDecoded  `toml:"network"`
	Security securitySectionDecoded `toml:"security"`
}

type serverSectionDecoded struct {
	ListenAddr               string `toml:"listen_addr"`
	PathPrefix               string `toml:"path_prefix"`
	StateDir                 string `toml:"state_dir"`
	AuditLog                 string `toml:"audit_log"`
	DiscordOwnerID           string `toml:"discord_owner_id"`
	ClientRegistry           string `toml:"client_registry"`
	DiscordApprovalChannelID string `toml:"discord_approval_channel_id"`
	DiscordAuditChannelID    string `toml:"discord_audit_channel_id"`
}

type discordSectionDecoded struct {
	BotTokenKeychainItem string `toml:"bot_token_keychain_item"`
	ApplicationID        string `toml:"application_id"`
}

type cryptoSectionDecoded struct {
	ArgonTime            *uint32 `toml:"argon_time"`
	ArgonMemoryMB        *uint32 `toml:"argon_memory_mb"`
	ArgonThreads         *uint8  `toml:"argon_threads"`
	JWTDefaultTTL        string  `toml:"jwt_default_ttl"`
	MaxInteractiveTTL    string  `toml:"max_interactive_ttl"`
	MaxSupervisorTTL     string  `toml:"max_supervisor_ttl"`
	DefaultMaxUses       *int    `toml:"default_max_uses"`
	NonceTTL             string  `toml:"nonce_ttl"`
	ClockSkew            string  `toml:"clock_skew"`
	ClaimApprovalTimeout string  `toml:"claim_approval_timeout"`
}

type networkSectionDecoded struct {
	RequireTailscale *bool    `toml:"require_tailscale"`
	AllowedCIDRs     []string `toml:"allowed_cidrs"`
	HealthBind       string   `toml:"health_bind"`
}

type securitySectionDecoded struct {
	RequireFileModeChecks *bool  `toml:"require_file_mode_checks"`
	RequireKeychainACL    *bool  `toml:"require_keychain_acl"`
	RequireNTPSync        *bool  `toml:"require_ntp_sync"`
	MaxClockDrift         string `toml:"max_clock_drift"`
}

// ---- LoadServer -------------------------------------------------------------

// LoadServer opens path, decodes it as a strict TOML config, applies defaults
// to every absent optional field, and validates all rules. On success it
// returns a fully populated *Server. On any failure it returns (nil, err)
// where err wraps one of the package's sentinel errors.
func LoadServer(ctx context.Context, path string) (*Server, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	f, err := os.Open(path) //nolint:gosec // operator-supplied config path
	if err != nil {
		return nil, fmt.Errorf("hush/config: open %q: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	decoded, err := decodeStrict(f)
	if err != nil {
		return nil, err
	}

	s, err := materialize(decoded)
	if err != nil {
		return nil, err
	}

	if s.Security.RequireFileModeChecks {
		if err := enforceConfigFileMode(f, path); err != nil {
			return nil, err
		}
		if err := enforceAuditLogParentMode(s.Server.AuditLog, s.Server.StateDir); err != nil {
			return nil, err
		}
	}

	if err := s.Validate(); err != nil {
		return nil, err
	}
	return s, nil
}

// enforceAuditLogParentMode rejects any audit_log whose immediate parent
// directory is group- or other-accessible (Perm()&0o077 != 0). The audit
// chain is the only tamper-evident record of approvals; if its parent is
// world-writable (e.g., /tmp), any local user can rename, truncate, or
// pre-seed the chain file before hush opens it. Mirrors the vault's
// parent-dir guard in internal/vault/file.go and is gated by the same
// Security.RequireFileModeChecks flag as enforceConfigFileMode so unit
// tests on systems without perm semantics can opt out.
//
// Short-circuits to nil when audit_log lexically escapes state_dir or its
// parent does not exist; both conditions are diagnosed more specifically
// by Validate's containment rule (ErrAuditLogEscape).
func enforceAuditLogParentMode(auditLog, stateDir string) error {
	if !isUnderStateDir(auditLog, stateDir) {
		return nil
	}
	parent := filepath.Dir(auditLog)
	info, statErr := os.Stat(parent)
	if statErr != nil {
		// Deliberate swallow: a missing parent is diagnosed downstream
		// (Writer.Run fails on first append) or by Validate's containment
		// rule when the path lies outside state_dir. Surfacing it here
		// would mask the more specific error and break the multi-violation
		// joining tests.
		return nil //nolint:nilerr // see comment above
	}
	if mode := info.Mode().Perm(); mode&0o077 != 0 {
		return fmt.Errorf("hush/config: audit_log parent %q mode %#o: %w", parent, mode, ErrAuditLogParentUnsafe)
	}
	return nil
}

// decodeStrict TOML-decodes from r with unknown-field rejection, mapping
// strict-missing errors to ErrUnknownField and any other decode failure to
// ErrTOMLDecode.
func decodeStrict(f *os.File) (serverDecoded, error) {
	var decoded serverDecoded
	dec := toml.NewDecoder(f)
	dec.DisallowUnknownFields()
	if decErr := dec.Decode(&decoded); decErr != nil {
		var strictErr *toml.StrictMissingError
		if errors.As(decErr, &strictErr) {
			return decoded, fmt.Errorf("%w: %s", ErrUnknownField, strictErr.Error())
		}
		return decoded, fmt.Errorf("%w: %s", ErrTOMLDecode, decErr.Error())
	}
	return decoded, nil
}

// enforceConfigFileMode rejects any config file whose perms are not exactly
// 0600. Even though the config never carries a credential (Keychain item names
// only — Constitution X), it does carry topology (Tailscale CIDRs, audit log
// paths, Discord IDs) that should not be group/world-readable on a shared
// host. Gated by Security.RequireFileModeChecks (default true) so unit tests
// on systems without perm semantics can opt out.
func enforceConfigFileMode(f *os.File, path string) error {
	fi, err := f.Stat()
	if err != nil {
		return fmt.Errorf("hush/config: stat %q: %w", path, err)
	}
	if mode := fi.Mode().Perm(); mode != 0o600 {
		return fmt.Errorf("hush/config: %s has perms %#o, want 0600: %w",
			path, mode, ErrConfigFileMode)
	}
	return nil
}

// ---- materialize ------------------------------------------------------------

// parseDuration converts a duration string to time.Duration, returning def when
// raw is empty, or ErrInvalidDuration when parsing fails.
func parseDuration(raw string, def time.Duration, fieldName string) (time.Duration, error) {
	if raw == "" {
		return def, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%w: field %s value %q", ErrInvalidDuration, fieldName, raw)
	}
	return d, nil
}

// materialize applies defaults to absent fields, parses durations, expands
// paths, and checks state_dir existence. It returns (nil, err) on any failure.
func materialize(d serverDecoded) (*Server, error) { //nolint:cyclop,gocognit,gocyclo // rule-engine: long by design
	s := &Server{}

	// ---- [server] ----
	s.rawListenAddr = d.Server.ListenAddr
	if d.Server.ListenAddr != "" {
		if ap, err := netip.ParseAddrPort(d.Server.ListenAddr); err == nil {
			s.Server.ListenAddr = ap
		}
		// parse failure is diagnosed in Validate via rawListenAddr
	}

	s.Server.PathPrefix = d.Server.PathPrefix

	// state_dir — required field with a default
	rawStateDir := d.Server.StateDir
	if rawStateDir == "" {
		rawStateDir = DefaultStateDir
	}
	absSD, err := absPath(rawStateDir)
	if err != nil {
		return nil, fmt.Errorf("hush/config: state_dir expand: %w", err)
	}
	info, statErr := os.Stat(absSD)
	if statErr != nil {
		if errors.Is(statErr, fs.ErrNotExist) {
			return nil, fmt.Errorf("hush/config: state_dir %q: %w", absSD, ErrStateDirNotFound)
		}
		return nil, fmt.Errorf("hush/config: state_dir %q: %w", absSD, statErr)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("hush/config: state_dir %q: %w", absSD, ErrStateDirUnsafe)
	}
	s.Server.StateDir = absSD

	// audit_log
	rawAuditLog := d.Server.AuditLog
	if rawAuditLog == "" {
		rawAuditLog = DefaultAuditLog
	}
	absAL, err := absPath(rawAuditLog)
	if err != nil {
		return nil, fmt.Errorf("hush/config: audit_log expand: %w", err)
	}
	s.Server.AuditLog = absAL

	// client_registry
	rawCR := d.Server.ClientRegistry
	if rawCR == "" {
		rawCR = DefaultClientRegistry
	}
	absCR, err := absPath(rawCR)
	if err != nil {
		return nil, fmt.Errorf("hush/config: client_registry expand: %w", err)
	}
	s.Server.ClientRegistry = absCR

	s.Server.DiscordOwnerID = d.Server.DiscordOwnerID
	s.Server.DiscordApprovalChannelID = d.Server.DiscordApprovalChannelID
	s.Server.DiscordAuditChannelID = d.Server.DiscordAuditChannelID

	// ---- [discord] ----
	s.Discord.BotTokenKeychainItem = d.Discord.BotTokenKeychainItem
	s.Discord.ApplicationID = d.Discord.ApplicationID

	// ---- [crypto] ----
	if d.Crypto.ArgonTime != nil {
		s.Crypto.ArgonTime = *d.Crypto.ArgonTime
	} else {
		s.Crypto.ArgonTime = DefaultArgonTime
	}
	if d.Crypto.ArgonMemoryMB != nil {
		s.Crypto.ArgonMemoryMB = *d.Crypto.ArgonMemoryMB
	} else {
		s.Crypto.ArgonMemoryMB = DefaultArgonMemoryMB
	}
	if d.Crypto.ArgonThreads != nil {
		s.Crypto.ArgonThreads = *d.Crypto.ArgonThreads
	} else {
		s.Crypto.ArgonThreads = DefaultArgonThreads
	}
	if d.Crypto.DefaultMaxUses != nil {
		s.Crypto.DefaultMaxUses = *d.Crypto.DefaultMaxUses
	} else {
		s.Crypto.DefaultMaxUses = DefaultMaxUses
	}

	if s.Crypto.JWTDefaultTTL, err = parseDuration(d.Crypto.JWTDefaultTTL, DefaultJWTTTL, "jwt_default_ttl"); err != nil {
		return nil, err
	}
	if s.Crypto.MaxInteractiveTTL, err = parseDuration(d.Crypto.MaxInteractiveTTL, DefaultMaxInteractiveTTL, "max_interactive_ttl"); err != nil {
		return nil, err
	}
	if s.Crypto.MaxSupervisorTTL, err = parseDuration(d.Crypto.MaxSupervisorTTL, DefaultMaxSupervisorTTL, "max_supervisor_ttl"); err != nil {
		return nil, err
	}
	if s.Crypto.NonceTTL, err = parseDuration(d.Crypto.NonceTTL, DefaultNonceTTL, "nonce_ttl"); err != nil {
		return nil, err
	}
	if s.Crypto.ClockSkew, err = parseDuration(d.Crypto.ClockSkew, DefaultClockSkew, "clock_skew"); err != nil {
		return nil, err
	}
	if s.Crypto.ClaimApprovalTimeout, err = parseDuration(d.Crypto.ClaimApprovalTimeout, DefaultClaimApprovalTimeout, "claim_approval_timeout"); err != nil {
		return nil, err
	}

	// ---- [network] ----
	if d.Network.RequireTailscale != nil {
		s.Network.RequireTailscale = *d.Network.RequireTailscale
	} else {
		s.Network.RequireTailscale = DefaultRequireTailscale
	}
	if d.Network.AllowedCIDRs != nil {
		s.Network.AllowedCIDRs = d.Network.AllowedCIDRs
	} else {
		s.Network.AllowedCIDRs = append([]string{}, DefaultAllowedCIDRs...)
	}

	// health_bind: inherit from listen_addr when absent
	s.rawHealthBind = d.Network.HealthBind
	if d.Network.HealthBind != "" {
		if hb, parseErr := netip.ParseAddrPort(d.Network.HealthBind); parseErr == nil {
			s.Network.HealthBind = hb
		}
		// parse failure diagnosed in Validate via rawHealthBind
	} else {
		// inherit from listen_addr
		s.Network.HealthBind = s.Server.ListenAddr
	}

	// ---- [security] ----
	if d.Security.RequireFileModeChecks != nil {
		s.Security.RequireFileModeChecks = *d.Security.RequireFileModeChecks
	} else {
		s.Security.RequireFileModeChecks = DefaultRequireFileModeChecks
	}
	if d.Security.RequireKeychainACL != nil {
		s.Security.RequireKeychainACL = *d.Security.RequireKeychainACL
	} else {
		s.Security.RequireKeychainACL = DefaultRequireKeychainACL
	}
	if d.Security.RequireNTPSync != nil {
		s.Security.RequireNTPSync = *d.Security.RequireNTPSync
	} else {
		s.Security.RequireNTPSync = DefaultRequireNTPSync
	}
	if s.Security.MaxClockDrift, err = parseDuration(d.Security.MaxClockDrift, DefaultMaxClockDrift, "max_clock_drift"); err != nil {
		return nil, err
	}

	return s, nil
}
