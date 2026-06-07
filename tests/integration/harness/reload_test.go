//go:build integration

package harness

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/hush/internal/supervise"
	superviseconfig "github.com/mrz1836/hush/internal/supervise/config"
	"github.com/mrz1836/hush/internal/testutil"
)

// newReloadSupervisor composes a reload-eligible supervisor using the
// supplied ReloadOpts.
func newReloadSupervisor(t *testing.T, name string, ropts *ReloadOpts) *TestSupervisor {
	t.Helper()
	logger := NewLogCapture(t)
	vault := NewVault(t, map[string]string{"ANTHROPIC_API_KEY": testutil.SentinelSecret(18)})
	discord := NewDiscord(t)
	discord.Stub().ApproveAll = true
	srv := NewServer(t, ServerOpts{Vault: vault, Logger: logger, Discord: discord})
	return NewSupervisor(t, SupervisorOpts{
		Vault:   vault,
		Server:  srv,
		Discord: discord,
		Logger:  logger,
		Name:    name,
		Scopes:  []string{"ANTHROPIC_API_KEY"},
		Reload:  ropts,
	})
}

// TestHarnessRepoRoot covers the go.mod locator.
func TestHarnessRepoRoot(t *testing.T) {
	root, err := harnessRepoRoot()
	require.NoError(t, err)
	assert.FileExists(t, filepath.Join(root, "go.mod"))
}

// TestBuildReloadTOMLFragmentsDefaults covers the default fragment layout.
func TestBuildReloadTOMLFragmentsDefaults(t *testing.T) {
	frag := buildReloadTOMLFragments(t, &ReloadOpts{})

	require.Len(t, frag.commandArgv, 1)
	assert.FileExists(t, frag.commandArgv[0])
	assert.Contains(t, frag.envBlock, "[child.env]")
	assert.Contains(t, frag.envBlock, `HUSH_CHILD_VERSION = "v0"`)
	assert.Contains(t, frag.readiness, "[child.readiness]")
	assert.Contains(t, frag.shutdown, "[child.shutdown]")
	assert.Contains(t, frag.handoff, "[child.handoff]")
	assert.Contains(t, frag.handoff, superviseconfig.HandoffModeHTTPProxy)
}

// TestBuildReloadTOMLFragmentsKnobs covers every optional knob.
func TestBuildReloadTOMLFragmentsKnobs(t *testing.T) {
	full := buildReloadTOMLFragments(t, &ReloadOpts{
		Version:           "v9",
		ForceUnready:      true,
		IgnoreSIGTERM:     true,
		ReadinessTimeout:  3 * time.Second,
		ReadinessInterval: 40 * time.Millisecond,
		ShutdownGrace:     750 * time.Millisecond,
		HandoffListenAddr: "127.0.0.1:0",
	})
	assert.Contains(t, full.envBlock, `HUSH_CHILD_VERSION = "v9"`)
	assert.Contains(t, full.envBlock, "HUSH_CHILD_FORCE_UNREADY")
	assert.Contains(t, full.envBlock, "HUSH_CHILD_IGNORE_SIGTERM")
	assert.Contains(t, full.readiness, "3s")
	assert.Contains(t, full.readiness, "40ms")
	assert.Contains(t, full.shutdown, "750ms")

	// OmitReadiness / OmitHandoff drop the corresponding blocks.
	omit := buildReloadTOMLFragments(t, &ReloadOpts{OmitReadiness: true, OmitHandoff: true})
	assert.Empty(t, omit.readiness)
	assert.Empty(t, omit.handoff)
	assert.NotEmpty(t, omit.shutdown)

	// InvalidHandoffMode overrides the mode string.
	invalid := buildReloadTOMLFragments(t, &ReloadOpts{InvalidHandoffMode: "fd-inheritance"})
	assert.Contains(t, invalid.handoff, "fd-inheritance")
}

// TestReloadAttachProxyAndGet drives the reload-eligible wiring end to end:
// the proxy stands up, forwards /health to the boot child, and the
// getters expose the socket + config paths. Idempotency is asserted.
func TestReloadAttachProxyAndGet(t *testing.T) {
	sup := newReloadSupervisor(t, "reload-attach", &ReloadOpts{Version: "v0"})
	sup.Run()
	sup.WaitState(t, supervise.StateRunning, 5*time.Second)

	proxy := sup.AttachProxyForReload(t)
	require.NotNil(t, proxy)
	// Idempotent: a second attach returns the same proxy.
	assert.Same(t, proxy, sup.AttachProxyForReload(t))

	code, body, headers := sup.ProxyGet(t, "/health")
	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, "ready", body)
	assert.NotEmpty(t, headers)

	assert.NotEmpty(t, sup.StatusSocketPath())
	assert.FileExists(t, sup.ConfigPath())
}

// TestReloadAttachProxySkipHealthWait covers attachProxy(waitHealth=false)
// against a child that is unready forever; the proxy still binds.
func TestReloadAttachProxySkipHealthWait(t *testing.T) {
	sup := newReloadSupervisor(t, "reload-skip-health", &ReloadOpts{
		Version:           "v0",
		ForceUnready:      true,
		ReadinessTimeout:  300 * time.Millisecond,
		ReadinessInterval: 25 * time.Millisecond,
	})
	sup.Run()
	sup.WaitState(t, supervise.StateRunning, 5*time.Second)

	proxy := sup.AttachProxyForReloadSkipHealthWait(t)
	require.NotNil(t, proxy)
	assert.NotEmpty(t, proxy.ListenAddr())

	// Because the health gate was skipped, the boot child may still be
	// spinning up (502 from the proxy) or already serving its forced-503;
	// either way it is never a healthy 200.
	code, _, _ := sup.ProxyGet(t, "/health")
	assert.NotEqual(t, http.StatusOK, code)
}

// TestReloadAttachHandlerOnlyAndSwapRefusal covers AttachReloadHandlerOnly
// plus the statusServerReloadHandler body on a non-eligible supervisor
// (SwapChild refuses), and the ProxyGet-without-proxy fatal guard.
func TestReloadAttachHandlerOnlyAndSwapRefusal(t *testing.T) {
	sup := newSupervisorFixture(t, "reload-handler-only") // plain, not reload-eligible
	sup.Run()
	sup.WaitState(t, supervise.StateRunning, 5*time.Second)

	sup.AttachReloadHandlerOnly(t)

	// The handler body runs and SwapChild refuses (not eligible).
	_, err := sup.statusServerReloadHandler(context.Background(), supervise.ReloadRequest{})
	require.Error(t, err)

	// ProxyGet without an attached proxy fatals.
	expectFatal(t, "proxy-not-attached", func(ft *testing.T) {
		_, _, _ = sup.ProxyGet(ft, "/health")
	})
}

// TestBuildReloadTOMLFragmentsCachesBinary confirms repeated calls reuse
// the singleton-built binary path.
func TestBuildReloadTOMLFragmentsCachesBinary(t *testing.T) {
	a := buildReloadTOMLFragments(t, &ReloadOpts{})
	b := buildReloadTOMLFragments(t, &ReloadOpts{Version: "vX"})
	require.NotEmpty(t, a.commandArgv)
	require.NotEmpty(t, b.commandArgv)
	assert.Equal(t, a.commandArgv[0], b.commandArgv[0])

	// Sanity: the cached path is an absolute path ending in reload-child.
	assert.True(t, strings.HasSuffix(a.commandArgv[0], "reload-child"))
	info, err := os.Stat(a.commandArgv[0])
	require.NoError(t, err)
	assert.False(t, info.IsDir())
}
