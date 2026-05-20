package alerts

import "strings"

// classTemplate carries the per-class label prefix used by render.
// The struct is intentionally minimal: the rendered body is composed
// from a fixed allow-list of Alert fields ({SupervisorName,
// MachineName, Pattern, Detail}); the label prefix is the only
// per-class differentiator.
type classTemplate struct {
	labelPrefix string
}

// classToTemplate locks the 8 per-class label prefixes.
// Constructed at package declaration time; never mutated; no init().
// The prefixes form 8 unique two-bracket [TIER][class-slug] strings;
// uniqueness is asserted by TestTemplate_LabelPrefixUniqueAndStable.
//
//nolint:gochecknoglobals // immutable class→template binding table
var classToTemplate = map[AlertClass]classTemplate{
	AlertClassApprovalRequest:               {labelPrefix: "[CRITICAL][approval-request]"},
	AlertClassDaemonRefreshRequest:          {labelPrefix: "[CRITICAL][daemon-refresh]"},
	AlertClassValidatorStaleFailure:         {labelPrefix: "[WARNING][validator-stale]"},
	AlertClassChildExit78StaleFailure:       {labelPrefix: "[CRITICAL][child-exit-78]"},
	AlertClassLogPatternStaleWarning:        {labelPrefix: "[WARNING][log-pattern]"},
	AlertClassDiscordDisconnected:           {labelPrefix: "[WARNING][discord-disconnected]"},
	AlertClassDiscordReconnected:            {labelPrefix: "[INFO][discord-reconnected]"},
	AlertClassVaultUnreachableAtBootTimeout: {labelPrefix: "[CRITICAL][vault-unreachable]"},
}

// render composes the rendered alert body. The label prefix and the
// supervisor= segment are always emitted; machine=, pattern=, detail=
// are emitted only when the corresponding Alert field is non-empty
// (omit-empty-lines).
//
// Alert.Class, Alert.Tier, and Alert.Time are NEVER reachable from
// this output. Class is implicit in the label prefix; Tier is encoded
// by the [TIER] bracket; Time is excluded entirely.
func (t classTemplate) render(a Alert) string {
	var b strings.Builder
	b.WriteString(t.labelPrefix)
	b.WriteString(" supervisor=")
	b.WriteString(a.SupervisorName)
	if a.MachineName != "" {
		b.WriteString(" machine=")
		b.WriteString(a.MachineName)
	}
	if a.Pattern != "" {
		b.WriteString(" pattern=")
		b.WriteString(a.Pattern)
	}
	if a.Detail != "" {
		b.WriteString(" detail=")
		b.WriteString(a.Detail)
	}
	return b.String()
}
