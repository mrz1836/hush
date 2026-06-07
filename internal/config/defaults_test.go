package config

import (
	"net/netip"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// TestDefaults_MatchSchema asserts every exported default/min/max var equals
// the value documented in docs/CONFIG-SCHEMA.md. This is the drift-detector:
// any change to a default must update the schema doc too.
func TestDefaults_MatchSchema(t *testing.T) {
	t.Parallel()

	assert.Equal(t, uint32(4), DefaultArgonTime, "DefaultArgonTime")
	assert.Equal(t, uint32(256), DefaultArgonMemoryMB, "DefaultArgonMemoryMB")
	assert.Equal(t, uint8(4), DefaultArgonThreads, "DefaultArgonThreads")
	assert.Equal(t, uint32(4), MinArgonTime, "MinArgonTime")
	assert.Equal(t, uint32(256), MinArgonMemoryMB, "MinArgonMemoryMB")
	assert.Equal(t, uint8(4), MinArgonThreads, "MinArgonThreads")

	assert.Equal(t, 8*time.Hour, DefaultJWTTTL, "DefaultJWTTTL")
	assert.Equal(t, 12*time.Hour, DefaultMaxInteractiveTTL, "DefaultMaxInteractiveTTL")
	assert.Equal(t, 20*time.Hour, DefaultMaxSupervisorTTL, "DefaultMaxSupervisorTTL")
	assert.Equal(t, 24*time.Hour, DefaultSupervisorTTLMax, "DefaultSupervisorTTLMax")
	assert.Equal(t, 50, DefaultMaxUses, "DefaultMaxUses")
	assert.Equal(t, 60*time.Second, DefaultNonceTTL, "DefaultNonceTTL")
	assert.Equal(t, 30*time.Second, DefaultClockSkew, "DefaultClockSkew")

	assert.Equal(t, "~/.hush", DefaultStateDir, "DefaultStateDir")
	assert.Equal(t, "~/.hush/audit.jsonl", DefaultAuditLog, "DefaultAuditLog")
	assert.Equal(t, "~/.hush/clients.json", DefaultClientRegistry, "DefaultClientRegistry")
	assert.Equal(t, 7743, DefaultListenPort, "DefaultListenPort")
	assert.Equal(t, "hush-discord", DefaultBotTokenKeychainItem, "DefaultBotTokenKeychainItem")
	assert.Equal(t, "hush-server", DefaultBotKeychainAccount, "DefaultBotKeychainAccount")

	assert.True(t, DefaultRequireTailscale, "DefaultRequireTailscale")
	assert.Equal(t, []string{"100.64.0.0/10"}, DefaultAllowedCIDRs, "DefaultAllowedCIDRs")

	assert.True(t, DefaultRequireFileModeChecks, "DefaultRequireFileModeChecks")
	assert.True(t, DefaultRequireKeychainACL, "DefaultRequireKeychainACL")
	assert.True(t, DefaultRequireNTPSync, "DefaultRequireNTPSync")
	assert.Equal(t, 60*time.Second, DefaultMaxClockDrift, "DefaultMaxClockDrift")

	assert.Equal(t, 6, MinPathPrefixLen, "MinPathPrefixLen")
	assert.Equal(t, 32, MaxPathPrefixLen, "MaxPathPrefixLen")

	expected := netip.MustParsePrefix("100.64.0.0/10")
	assert.Equal(t, expected, TailscaleCGNAT, "TailscaleCGNAT")
}
