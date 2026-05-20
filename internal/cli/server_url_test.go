package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestServerURL_PrintsURLFromConfig(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// t.TempDir's subdir is created at 0755 (umask); the config
	// validator requires the audit_log parent dir to be 0700.
	require.NoError(t, os.Chmod(dir, 0o700))
	configPath := filepath.Join(dir, "config.toml")
	body := buildServerDecodedFromDefaults(serverInputs{
		listenAddr:        testListenAddrInput,
		pathPrefix:        "TESTPREFIX12",
		ownerID:           testOwnerIDInput,
		applicationID:     testApplicationIDIn,
		stateDir:          dir,
		approvalChannelID: "111111111111111111",
		auditChannelID:    "222222222222222222",
		botTokenKeychain:  "hush-discord",
	})
	require.NoError(t, writeConfigTOMLAtomic(configPath, body))

	var stdout, stderr bytes.Buffer
	err := runServerURL(context.Background(), newStream(&stdout, false, true), newStream(&stderr, false, true), configPath)
	require.NoError(t, err)
	require.Empty(t, stderr.String())
	require.Equal(t, "http://"+testListenAddrInput+"/h/TESTPREFIX12\n", stdout.String())
}

func TestServerURL_SubcommandRegisteredOnRoot(t *testing.T) {
	t.Parallel()
	root := newRootCmd(&outputContext{
		stdout: newStream(&bytes.Buffer{}, false, true),
		stderr: newStream(&bytes.Buffer{}, false, true),
	})
	for _, c := range root.Commands() {
		if c.Use == "server-url" {
			return
		}
	}
	t.Fatalf("`server-url` subcommand not registered on root")
}
