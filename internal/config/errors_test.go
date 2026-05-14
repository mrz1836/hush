package config

import (
	"io/fs"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestSentinels_Catalog asserts every Err* sentinel is non-nil.
func TestSentinels_Catalog(t *testing.T) {
	t.Parallel()

	sentinels := []struct {
		name string
		err  error
	}{
		{"ErrTOMLDecode", ErrTOMLDecode},
		{"ErrUnknownField", ErrUnknownField},
		{"ErrMissingRequiredField", ErrMissingRequiredField},
		{"ErrInvalidDuration", ErrInvalidDuration},
		{"ErrTailscaleBindRequired", ErrTailscaleBindRequired},
		{"ErrListenLoopback", ErrListenLoopback},
		{"ErrListenUnspecified", ErrListenUnspecified},
		{"ErrListenPublic", ErrListenPublic},
		{"ErrListenMalformed", ErrListenMalformed},
		{"ErrTailscaleRequired", ErrTailscaleRequired},
		{"ErrPathPrefixInvalid", ErrPathPrefixInvalid},
		{"ErrAuditLogEscape", ErrAuditLogEscape},
		{"ErrStateDirNotFound", ErrStateDirNotFound},
		{"ErrStateDirUnsafe", ErrStateDirUnsafe},
		{"ErrAuditLogParentUnsafe", ErrAuditLogParentUnsafe},
		{"ErrArgonMemoryTooLow", ErrArgonMemoryTooLow},
		{"ErrArgonTimeTooLow", ErrArgonTimeTooLow},
		{"ErrArgonThreadsTooLow", ErrArgonThreadsTooLow},
		{"ErrSupervisorTTLOutOfRange", ErrSupervisorTTLOutOfRange},
	}
	for _, tc := range sentinels {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Error(t, tc.err)
		})
	}
}

// TestSentinels_WrapRelationships asserts the listen-addr family wraps the
// umbrella and that ErrStateDirNotFound wraps fs.ErrNotExist.
func TestSentinels_WrapRelationships(t *testing.T) {
	t.Parallel()

	require.ErrorIs(t, ErrListenLoopback, ErrTailscaleBindRequired,
		"ErrListenLoopback must wrap ErrTailscaleBindRequired")
	require.ErrorIs(t, ErrListenUnspecified, ErrTailscaleBindRequired,
		"ErrListenUnspecified must wrap ErrTailscaleBindRequired")
	require.ErrorIs(t, ErrListenPublic, ErrTailscaleBindRequired,
		"ErrListenPublic must wrap ErrTailscaleBindRequired")
	require.ErrorIs(t, ErrStateDirNotFound, fs.ErrNotExist,
		"ErrStateDirNotFound must wrap fs.ErrNotExist")
}
