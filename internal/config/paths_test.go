package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var errFakeHomeDir = errors.New("no home directory")

func TestPaths_ExpandHome(t *testing.T) {
	t.Parallel()

	home, err := os.UserHomeDir()
	require.NoError(t, err)

	tests := []struct {
		name    string
		input   string
		wantErr bool
		check   func(t *testing.T, got string)
	}{
		{
			name:  "tilde-slash expands to home",
			input: "~/foo",
			check: func(t *testing.T, got string) {
				assert.Equal(t, filepath.Join(home, "foo"), got)
			},
		},
		{
			name:  "bare tilde expands to home",
			input: "~",
			check: func(t *testing.T, got string) {
				assert.Equal(t, home, got)
			},
		},
		{
			name:  "tilde-user treated as literal",
			input: "~user/foo",
			check: func(t *testing.T, got string) {
				assert.Equal(t, "~user/foo", got)
			},
		},
		{
			name:  "dollar-var not expanded",
			input: "$HOME/foo",
			check: func(t *testing.T, got string) {
				assert.Equal(t, "$HOME/foo", got)
			},
		},
		{
			name:  "absolute path unchanged",
			input: "/etc/hosts",
			check: func(t *testing.T, got string) {
				assert.Equal(t, "/etc/hosts", got)
			},
		},
		{
			name:  "relative path unchanged",
			input: "relative/path",
			check: func(t *testing.T, got string) {
				assert.Equal(t, "relative/path", got)
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := expandHome(tc.input)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			tc.check(t, got)
		})
	}
}

func TestPaths_AbsPath(t *testing.T) {
	t.Parallel()

	home, err := os.UserHomeDir()
	require.NoError(t, err)

	tests := []struct {
		name  string
		input string
		check func(t *testing.T, got string)
	}{
		{
			name:  "tilde expanded and made absolute",
			input: "~/foo",
			check: func(t *testing.T, got string) {
				assert.Equal(t, filepath.Join(home, "foo"), got)
				assert.True(t, filepath.IsAbs(got))
			},
		},
		{
			name:  "already absolute passes through",
			input: "/etc/hosts",
			check: func(t *testing.T, got string) {
				assert.Equal(t, "/etc/hosts", got)
			},
		},
		{
			name:  "relative path becomes absolute",
			input: "relative",
			check: func(t *testing.T, got string) {
				assert.True(t, filepath.IsAbs(got))
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := absPath(tc.input)
			require.NoError(t, err)
			tc.check(t, got)
		})
	}
}

func TestPaths_IsUnderStateDir(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		audit    string
		stateDir string
		want     bool
	}{
		{
			name:     "directly under state dir",
			stateDir: "/home/user/.hush",
			audit:    "/home/user/.hush/audit.jsonl",
			want:     true,
		},
		{
			name:     "nested under state dir",
			stateDir: "/home/user/.hush",
			audit:    "/home/user/.hush/logs/audit.jsonl",
			want:     true,
		},
		{
			name:     "parent traversal rejected",
			stateDir: "/home/user/.hush",
			audit:    "/home/user/.hush/../etc/passwd",
			want:     false,
		},
		{
			name:     "absolute escape rejected",
			stateDir: "/home/user/.hush",
			audit:    "/etc/passwd",
			want:     false,
		},
		{
			name:     "drive-letter false-positive rejected",
			stateDir: "/usr",
			audit:    "/usrlocal/bin/something",
			want:     false,
		},
		{
			name:     "equal paths rejected (audit == stateDir)",
			stateDir: "/home/user/.hush",
			audit:    "/home/user/.hush",
			want:     false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// Canonicalise inputs as the real code does
			absAudit, _ := filepath.Abs(tc.audit)
			absSD, _ := filepath.Abs(tc.stateDir)
			got := isUnderStateDir(absAudit, absSD)
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestExpandHome_UserHomeDirError covers the error branches in expandHome when
// os.UserHomeDir fails. These branches are otherwise unreachable in a normal
// test environment.
func TestExpandHome_UserHomeDirError(t *testing.T) {
	// Cannot use t.Parallel — we mutate the package-level userHomeDir.
	orig := userHomeDir
	userHomeDir = func() (string, error) { return "", errFakeHomeDir }
	defer func() { userHomeDir = orig }()

	_, err := expandHome("~/foo")
	require.ErrorIs(t, err, errFakeHomeDir)

	_, err = expandHome("~")
	require.ErrorIs(t, err, errFakeHomeDir)
}

// TestAbsPath_UserHomeDirError covers the error propagation in absPath when
// expandHome fails due to a UserHomeDir error.
func TestAbsPath_UserHomeDirError(t *testing.T) {
	// Cannot use t.Parallel — we mutate the package-level userHomeDir.
	orig := userHomeDir
	userHomeDir = func() (string, error) { return "", errFakeHomeDir }
	defer func() { userHomeDir = orig }()

	_, err := absPath("~/foo")
	require.ErrorIs(t, err, errFakeHomeDir)
}
