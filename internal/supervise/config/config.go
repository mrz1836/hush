package config

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/pelletier/go-toml/v2"
)

// ---- Public types -----------------------------------------------------------

// Supervisor is the fully-materialized per-supervisor configuration. It is
// read-only after Load returns; consumers MUST NOT mutate any field, including
// slice and map elements. No field carries a secret value (Constitution X /
// FR-014); the reference fields hold non-secret labels only — scoped secret
// names, validator type names, env-var names, log-pattern strings.
type Supervisor struct {
	Name                   string
	Reason                 string
	ServerURL              string
	ClientMachineIndex     uint32
	ClientKeyFile          string
	SessionType            string
	RequestedTTL           time.Duration
	RefreshWindow          string
	RefreshNudgeBefore     time.Duration
	BootRetryTimeout       time.Duration
	CacheSecretsForRestart bool
	CacheGraceTTL          time.Duration
	StatusSocket           string
	PIDFile                string
	AuditLog               string
	LogLevel               string
	Scope                  []string

	Child      Child
	Discord    DiscordRouting
	Validators map[string]Validator
	Watchdog   Watchdog
}

// Child is the [child] section of the supervisor config.
type Child struct {
	Command            []string
	WorkingDir         string
	EnvPassthrough     []string
	RestartOnCleanExit bool
	RestartOnExit78    bool
}

// DiscordRouting is the [discord] section of the supervisor config. Both
// fields are non-secret labels (snowflakes are public IDs in Discord's UI);
// the bot token itself lives in Keychain on the server, not here.
type DiscordRouting struct {
	DaemonLabel    string
	AlertChannelID string
}

// Watchdog is the [watchdog] section of the supervisor config.
type Watchdog struct {
	Enabled          bool
	Patterns         []string
	MaxAlertsPerHour int
}

// Validator is the constrained-string typedef used for [validators] map
// values. A Validator value held by a successfully loaded *Supervisor is
// guaranteed to be in the package-level allow-list; SC-005 asserts this
// invariant.
type Validator string

// ---- Wire-shape (decoded) types — INTERNAL ----------------------------------

// supervisorDecoded mirrors Supervisor but uses pointer / empty-string
// sentinels to distinguish "absent in TOML" from "set to zero".
type supervisorDecoded struct {
	Name                   string   `toml:"name"`
	Reason                 string   `toml:"reason"`
	ServerURL              string   `toml:"server_url"`
	ClientMachineIndex     *uint32  `toml:"client_machine_index"`
	ClientKeyFile          string   `toml:"client_key_file"`
	SessionType            string   `toml:"session_type"`
	RequestedTTL           string   `toml:"requested_ttl"`
	RefreshWindow          string   `toml:"refresh_window"`
	RefreshNudgeBefore     string   `toml:"refresh_nudge_before"`
	BootRetryTimeout       string   `toml:"boot_retry_timeout"`
	CacheSecretsForRestart *bool    `toml:"cache_secrets_for_restart"`
	CacheGraceTTL          *string  `toml:"cache_grace_ttl"`
	StatusSocket           string   `toml:"status_socket"`
	PIDFile                string   `toml:"pid_file"`
	AuditLog               string   `toml:"audit_log"`
	LogLevel               string   `toml:"log_level"`
	Scope                  []string `toml:"scope"`

	// Distinguish "scope absent" from "scope = []" so the scope-empty validator
	// fires the same sentinel for both per FR-008.
	scopePresent bool

	Child      childDecoded      `toml:"child"`
	Discord    discordDecoded    `toml:"discord"`
	Validators map[string]string `toml:"validators"`
	Watchdog   *watchdogDecoded  `toml:"watchdog"`
}

type childDecoded struct {
	Command            []string `toml:"command"`
	WorkingDir         string   `toml:"working_dir"`
	EnvPassthrough     []string `toml:"env_passthrough"`
	RestartOnCleanExit *bool    `toml:"restart_on_clean_exit"`
	RestartOnExit78    *bool    `toml:"restart_on_exit_78"`
}

type discordDecoded struct {
	DaemonLabel    string `toml:"daemon_label"`
	AlertChannelID string `toml:"alert_channel_id"`
}

type watchdogDecoded struct {
	Enabled          *bool    `toml:"enabled"`
	Patterns         []string `toml:"patterns"`
	MaxAlertsPerHour *int     `toml:"max_alerts_per_hour"`
}

// ---- Load -------------------------------------------------------------------

// Load opens path, decodes it as a strict TOML supervisor config, applies
// defaults to every absent optional field, and validates all rules. On
// success it returns a fully populated *Supervisor with no secret material.
// On any failure it returns (nil, err) where err wraps one of the package's
// sentinel errors.
//
// Load is single-shot, synchronous, idempotent, and safe for concurrent calls
// (it touches no package-level mutable state). It spawns no goroutines and
// performs no writes to the filesystem (Constitution IX).
func Load(ctx context.Context, path string) (*Supervisor, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	f, err := os.Open(path) //nolint:gosec // operator-supplied config path
	if err != nil {
		return nil, fmt.Errorf("hush/supervise/config: open %q: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	decoded, err := decodeStrict(f)
	if err != nil {
		return nil, err
	}

	if reqErr := requiredFieldGate(decoded); reqErr != nil {
		return nil, reqErr
	}

	s, err := materialize(decoded)
	if err != nil {
		return nil, err
	}
	return s, nil
}

// decodeStrict TOML-decodes from f with unknown-field rejection, mapping
// strict-missing errors to ErrUnknownField and any other decode failure to
// ErrTOMLDecode.
func decodeStrict(f *os.File) (supervisorDecoded, error) {
	var decoded supervisorDecoded
	dec := toml.NewDecoder(f)
	dec.DisallowUnknownFields()
	if decErr := dec.Decode(&decoded); decErr != nil {
		var strictErr *toml.StrictMissingError
		if errors.As(decErr, &strictErr) {
			return decoded, fmt.Errorf("%w: %s", ErrUnknownField, strictErr.Error())
		}
		return decoded, fmt.Errorf("%w: %s", ErrTOMLDecode, decErr.Error())
	}
	// Distinguish "scope absent" from "scope = []" by re-decoding into a
	// presence-detector struct. pelletier/v2 fills the slice with nil for
	// "absent" and an empty-but-non-nil slice for "scope = []", so the nil
	// check does the job without a second pass over the file.
	decoded.scopePresent = decoded.Scope != nil
	return decoded, nil
}

// ---- materialize ------------------------------------------------------------

// materialize applies defaults to absent fields, parses durations, expands
// paths, runs per-field and cross-field validation, and constructs the public
// Supervisor value. Returns (nil, err) on any failure.
//
//nolint:cyclop,gocognit,gocyclo,funlen // rule-engine: long by design, mirrors SDD-06 materialize
func materialize(d supervisorDecoded) (*Supervisor, error) {
	s := &Supervisor{}

	s.Name = d.Name
	s.Reason = d.Reason
	s.ServerURL = d.ServerURL
	if err := validateServerURL(d.ServerURL); err != nil {
		return nil, err
	}
	if d.ClientMachineIndex != nil {
		s.ClientMachineIndex = *d.ClientMachineIndex
	}
	if d.ClientKeyFile != "" {
		clientKeyFile, ckErr := absPath(d.ClientKeyFile)
		if ckErr != nil {
			return nil, fmt.Errorf("hush/supervise/config: client_key_file expand: %w", ckErr)
		}
		s.ClientKeyFile = clientKeyFile
	}

	if d.SessionType != "supervisor" {
		return nil, fmt.Errorf("%w: got %q", ErrSessionTypeInvalid, d.SessionType)
	}
	s.SessionType = d.SessionType

	ttl, err := parseDuration(d.RequestedTTL, DefaultRequestedTTL, "requested_ttl")
	if err != nil {
		return nil, err
	}
	if ttl > MaxRequestedTTL {
		return nil, fmt.Errorf("%w: requested_ttl=%s, ceiling=%s", ErrRequestedTTLOutOfRange, ttl, MaxRequestedTTL)
	}
	s.RequestedTTL = ttl

	rw := d.RefreshWindow
	if rw == "" {
		rw = DefaultRefreshWindow
	}
	if rwErr := validateRefreshWindow(rw); rwErr != nil {
		return nil, rwErr
	}
	s.RefreshWindow = rw

	if s.RefreshNudgeBefore, err = parseDuration(d.RefreshNudgeBefore, DefaultRefreshNudgeBefore, "refresh_nudge_before"); err != nil {
		return nil, err
	}
	if s.RefreshNudgeBefore > MaxRefreshNudgeBefore {
		return nil, fmt.Errorf("%w: refresh_nudge_before=%s, cap=%s", ErrRefreshNudgeBeforeTooLong, s.RefreshNudgeBefore, MaxRefreshNudgeBefore)
	}
	if s.BootRetryTimeout, err = parseDuration(d.BootRetryTimeout, DefaultBootRetryTimeout, "boot_retry_timeout"); err != nil {
		return nil, err
	}
	if s.BootRetryTimeout > MaxBootRetryTimeout {
		return nil, fmt.Errorf("%w: boot_retry_timeout=%s, cap=%s", ErrBootRetryTimeoutTooLong, s.BootRetryTimeout, MaxBootRetryTimeout)
	}

	cacheEnabled := DefaultCacheSecretsForRestart
	if d.CacheSecretsForRestart != nil {
		cacheEnabled = *d.CacheSecretsForRestart
	}
	s.CacheSecretsForRestart = cacheEnabled

	// Grace-cache: contradiction-guard FIRST (per research.md R-005), then
	// cap-enforcement, then default-application.
	if d.CacheGraceTTL != nil && !cacheEnabled {
		return nil, fmt.Errorf("%w", ErrGraceTTLWithoutCache)
	}
	if d.CacheGraceTTL != nil {
		gt, gtErr := parseDuration(*d.CacheGraceTTL, DefaultGraceWindow, "cache_grace_ttl")
		if gtErr != nil {
			return nil, gtErr
		}
		if gt > MaxGraceWindow {
			return nil, fmt.Errorf("%w: cache_grace_ttl=%s, cap=%s", ErrGraceWindowTooLong, gt, MaxGraceWindow)
		}
		s.CacheGraceTTL = gt
	} else if cacheEnabled {
		s.CacheGraceTTL = DefaultGraceWindow
	}

	statusSocket, err := absPath(d.StatusSocket)
	if err != nil {
		return nil, fmt.Errorf("hush/supervise/config: status_socket expand: %w", err)
	}
	s.StatusSocket = statusSocket

	pidFile, err := absPath(d.PIDFile)
	if err != nil {
		return nil, fmt.Errorf("hush/supervise/config: pid_file expand: %w", err)
	}
	s.PIDFile = pidFile

	// audit_log defaults to <dirname(pid_file)>/<name>-audit.jsonl when
	// absent. Operators may override with an explicit path; both code paths
	// go through absPath for tilde-expand + lexical-clean enforcement.
	if d.AuditLog == "" {
		s.AuditLog = filepath.Join(filepath.Dir(pidFile), s.Name+"-audit.jsonl")
	} else {
		auditLog, alErr := absPath(d.AuditLog)
		if alErr != nil {
			return nil, fmt.Errorf("hush/supervise/config: audit_log expand: %w", alErr)
		}
		s.AuditLog = auditLog
	}

	logLevel := d.LogLevel
	if logLevel == "" {
		logLevel = DefaultLogLevel
	}
	if _, ok := logLevelAllowList[logLevel]; !ok {
		return nil, fmt.Errorf("%w: got %q", ErrLogLevelInvalid, logLevel)
	}
	s.LogLevel = logLevel

	// scope: absence and emptiness both → ErrScopeEmpty (FR-008).
	if !d.scopePresent || len(d.Scope) == 0 {
		return nil, fmt.Errorf("%w", ErrScopeEmpty)
	}
	s.Scope = append([]string{}, d.Scope...)

	// [child]
	if len(d.Child.Command) == 0 {
		return nil, fmt.Errorf("%w", ErrCommandEmpty)
	}
	if !filepathIsAbs(d.Child.Command[0]) {
		return nil, fmt.Errorf("%w: got %q", ErrCommandPathRelative, d.Child.Command[0])
	}
	s.Child.Command = append([]string{}, d.Child.Command...)
	if d.Child.WorkingDir != "" {
		wd, err := expandHome(d.Child.WorkingDir)
		if err != nil {
			return nil, fmt.Errorf("hush/supervise/config: child.working_dir expand: %w", err)
		}
		s.Child.WorkingDir = wd
	}
	if d.Child.EnvPassthrough != nil {
		s.Child.EnvPassthrough = append([]string{}, d.Child.EnvPassthrough...)
	} else {
		s.Child.EnvPassthrough = []string{}
	}
	if d.Child.RestartOnCleanExit != nil {
		s.Child.RestartOnCleanExit = *d.Child.RestartOnCleanExit
	} else {
		s.Child.RestartOnCleanExit = DefaultRestartOnCleanExit
	}
	if d.Child.RestartOnExit78 != nil {
		s.Child.RestartOnExit78 = *d.Child.RestartOnExit78
	} else {
		s.Child.RestartOnExit78 = DefaultRestartOnExit78
	}

	// [discord]
	s.Discord.DaemonLabel = d.Discord.DaemonLabel
	s.Discord.AlertChannelID = d.Discord.AlertChannelID

	// [validators]
	s.Validators = make(map[string]Validator, len(d.Validators))
	for secretName, validatorName := range d.Validators {
		if _, ok := validatorAllowList[validatorName]; !ok {
			return nil, fmt.Errorf("hush/supervise/config: unknown validator %q: %w", validatorName, ErrUnknownValidator)
		}
		s.Validators[secretName] = Validator(validatorName)
	}

	// [watchdog] — section absent ≡ all fields absent (Clarification 4 / R-008).
	wd := d.Watchdog
	if wd == nil {
		wd = &watchdogDecoded{}
	}
	if wd.Enabled != nil {
		s.Watchdog.Enabled = *wd.Enabled
	} else {
		s.Watchdog.Enabled = DefaultWatchdogEnabled
	}
	if wd.Patterns != nil {
		s.Watchdog.Patterns = append([]string{}, wd.Patterns...)
	} else {
		s.Watchdog.Patterns = append([]string{}, DefaultWatchdogPatterns...)
	}
	if wd.MaxAlertsPerHour != nil {
		s.Watchdog.MaxAlertsPerHour = *wd.MaxAlertsPerHour
	} else {
		s.Watchdog.MaxAlertsPerHour = DefaultWatchdogMaxAlertsPerHour
	}
	if s.Watchdog.MaxAlertsPerHour <= 0 {
		return nil, fmt.Errorf("%w: got %d", ErrWatchdogRateInvalid, s.Watchdog.MaxAlertsPerHour)
	}

	return s, nil
}
