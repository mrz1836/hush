package supervise

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/mrz1836/hush/internal/supervise/config"
)

// envMap parses a KEY=VALUE slice into a map for deterministic lookups.
// The buildChildEnv result has no ordering guarantee across map keys
// (Env / overlay are Go maps), so tests compare via membership instead.
func envMap(t *testing.T, env []string) map[string]string {
	t.Helper()
	out := make(map[string]string, len(env))
	for _, kv := range env {
		idx := strings.IndexByte(kv, '=')
		if idx < 0 {
			t.Fatalf("env entry without '=': %q", kv)
		}
		key := kv[:idx]
		if _, dup := out[key]; dup {
			t.Fatalf("duplicate env key %q in %v", key, env)
		}
		out[key] = kv[idx+1:]
	}
	return out
}

// envKeysSorted is a stable view of the env keys used in precedence
// assertions where ordering between EnvPassthrough and Env matters.
func envKeysSorted(env []string) []string {
	keys := make([]string, 0, len(env))
	for _, kv := range env {
		idx := strings.IndexByte(kv, '=')
		if idx < 0 {
			continue
		}
		keys = append(keys, kv[:idx])
	}
	sort.Strings(keys)
	return keys
}

// minimalLifecycle returns a Lifecycle wired with only the fields
// buildChildEnv / buildChildEnvOverlay read. No goroutines start, no
// dependencies are required.
func minimalLifecycle(cfg *config.Supervisor) *Lifecycle {
	return &Lifecycle{config: cfg}
}

// scopedSecrets builds a secretSet for the supplied scope→value map
// and returns a cleanup func that destroys every entry.
func scopedSecrets(t *testing.T, secrets map[string]string) (secretSet, func()) {
	t.Helper()
	out := make(secretSet, len(secrets))
	for k, v := range secrets {
		out[k] = newSecureBytes(t, []byte(v))
	}
	cleanup := func() { destroySecrets(out) }
	return out, cleanup
}

// ---- TestBuildChildEnv_Precedence -------------------------------------------

func TestBuildChildEnv_PrecedenceOverlayOverEnv(t *testing.T) {
	t.Parallel()
	cfg := &config.Supervisor{
		Scope: []string{"OPENAI_API_KEY"},
		Child: config.Child{
			Env: map[string]string{
				"PORT":         "8080", // overridden by overlay
				"LOG_LEVEL":    "info", // preserved (no overlay/scope collision)
				"SHARED_TOKEN": "from-env",
			},
		},
	}
	l := minimalLifecycle(cfg)
	secrets, cleanup := scopedSecrets(t, map[string]string{
		"OPENAI_API_KEY": "sk-vault-secret",
	})
	defer cleanup()

	overlay := map[string]string{
		"PORT":           "9090",         // wins over Env
		"HUSH_BIND_PORT": "9090",         // new key
		"SHARED_TOKEN":   "from-overlay", // wins over Env
	}

	env, err := l.buildChildEnv(secrets, overlay)
	if err != nil {
		t.Fatalf("buildChildEnv: %v", err)
	}
	got := envMap(t, env)

	if got["PORT"] != "9090" {
		t.Errorf("PORT: want overlay value 9090, got %q", got["PORT"])
	}
	if got["LOG_LEVEL"] != "info" {
		t.Errorf("LOG_LEVEL: want preserved Env value info, got %q", got["LOG_LEVEL"])
	}
	if got["SHARED_TOKEN"] != "from-overlay" {
		t.Errorf("SHARED_TOKEN: want overlay value, got %q", got["SHARED_TOKEN"])
	}
	if got["HUSH_BIND_PORT"] != "9090" {
		t.Errorf("HUSH_BIND_PORT: want 9090, got %q", got["HUSH_BIND_PORT"])
	}
	if got["OPENAI_API_KEY"] != "sk-vault-secret" {
		t.Errorf("scope secret missing or wrong: %q", got["OPENAI_API_KEY"])
	}
}

func TestBuildChildEnv_ScopeWinsOverOverlay(t *testing.T) {
	t.Parallel()
	cfg := &config.Supervisor{
		Scope: []string{"OPENAI_API_KEY"},
		Child: config.Child{
			Env: map[string]string{"FOO": "bar"},
		},
	}
	l := minimalLifecycle(cfg)
	secrets, cleanup := scopedSecrets(t, map[string]string{
		"OPENAI_API_KEY": "sk-vault",
	})
	defer cleanup()

	// Overlay attempts to mask a scope name — MUST be dropped silently.
	overlay := map[string]string{
		"OPENAI_API_KEY": "sk-leaked-from-overlay",
		"HUSH_BIND_PORT": "12345",
	}
	env, err := l.buildChildEnv(secrets, overlay)
	if err != nil {
		t.Fatalf("buildChildEnv: %v", err)
	}
	got := envMap(t, env)

	if got["OPENAI_API_KEY"] != "sk-vault" {
		t.Fatalf("scope must win over overlay: got %q want sk-vault", got["OPENAI_API_KEY"])
	}
	if got["HUSH_BIND_PORT"] != "12345" {
		t.Fatalf("non-scope overlay key must apply: got %q", got["HUSH_BIND_PORT"])
	}
}

func TestBuildChildEnv_EnvWinsOverPassthrough(t *testing.T) {
	t.Parallel()
	// PATH is set in the supervisor's environment by Go's test harness;
	// using a name guaranteed to exist sidesteps the need to mutate
	// os.Environ() in tests.
	cfg := &config.Supervisor{
		Scope: []string{"OPENAI_API_KEY"},
		Child: config.Child{
			EnvPassthrough: []string{"PATH"},
			Env:            map[string]string{"PATH": "/explicit/from/env"},
		},
	}
	l := minimalLifecycle(cfg)
	secrets, cleanup := scopedSecrets(t, map[string]string{
		"OPENAI_API_KEY": "sk",
	})
	defer cleanup()

	env, err := l.buildChildEnv(secrets, nil)
	if err != nil {
		t.Fatalf("buildChildEnv: %v", err)
	}
	got := envMap(t, env)
	if got["PATH"] != "/explicit/from/env" {
		t.Fatalf("Env must win over EnvPassthrough on collision: got %q", got["PATH"])
	}
}

func TestBuildChildEnv_NilOverlayBehavesLikeEmpty(t *testing.T) {
	t.Parallel()
	cfg := &config.Supervisor{
		Scope: []string{"OPENAI_API_KEY"},
		Child: config.Child{
			Env: map[string]string{"FOO": "bar"},
		},
	}
	l := minimalLifecycle(cfg)
	secrets, cleanup := scopedSecrets(t, map[string]string{
		"OPENAI_API_KEY": "sk",
	})
	defer cleanup()

	gotNil, err := l.buildChildEnv(secrets, nil)
	if err != nil {
		t.Fatalf("nil-overlay buildChildEnv: %v", err)
	}
	secrets2, cleanup2 := scopedSecrets(t, map[string]string{
		"OPENAI_API_KEY": "sk",
	})
	defer cleanup2()
	gotEmpty, err := l.buildChildEnv(secrets2, map[string]string{})
	if err != nil {
		t.Fatalf("empty-overlay buildChildEnv: %v", err)
	}
	if got, want := envKeysSorted(gotNil), envKeysSorted(gotEmpty); !equalSlices(got, want) {
		t.Fatalf("nil/empty overlay produced different keys: %v vs %v", got, want)
	}
}

func TestBuildChildEnv_DeterministicScopeOrder(t *testing.T) {
	t.Parallel()
	cfg := &config.Supervisor{
		Scope: []string{"AAA", "BBB"},
	}
	l := minimalLifecycle(cfg)
	secrets, cleanup := scopedSecrets(t, map[string]string{
		"AAA": "v-aaa",
		"BBB": "v-bbb",
	})
	defer cleanup()

	env, err := l.buildChildEnv(secrets, nil)
	if err != nil {
		t.Fatalf("buildChildEnv: %v", err)
	}
	// Scope keys are emitted in config.Scope order. The buildChildEnv
	// loop iterates l.config.Scope linearly, so AAA precedes BBB.
	firstAAA := firstIndexWithPrefix(env, "AAA=")
	firstBBB := firstIndexWithPrefix(env, "BBB=")
	if firstAAA < 0 || firstBBB < 0 {
		t.Fatalf("scope env entries missing: AAA=%d BBB=%d env=%v", firstAAA, firstBBB, env)
	}
	if firstAAA > firstBBB {
		t.Fatalf("scope order not preserved: AAA at %d, BBB at %d", firstAAA, firstBBB)
	}
}

// firstIndexWithPrefix returns the first index in env whose entry has
// the supplied KEY= prefix, or -1 when no entry matches.
func firstIndexWithPrefix(env []string, prefix string) int {
	for i, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			return i
		}
	}
	return -1
}

// ---- TestBuildChildEnvOverlay -----------------------------------------------

func TestBuildChildEnvOverlay_NonReloadConfigEmpty(t *testing.T) {
	t.Parallel()
	cfg := &config.Supervisor{
		Scope: []string{"OPENAI_API_KEY"},
		Child: config.Child{
			// No Handoff → not reload-eligible.
		},
	}
	l := minimalLifecycle(cfg)
	overlay, err := l.buildChildEnvOverlay(context.Background())
	if err != nil {
		t.Fatalf("buildChildEnvOverlay: %v", err)
	}
	if len(overlay) != 0 {
		t.Fatalf("non-reload config overlay must be empty, got %v", overlay)
	}
	if l.backendPort != 0 {
		t.Fatalf("non-reload config must not allocate backendPort, got %d", l.backendPort)
	}
}

func TestBuildChildEnvOverlay_HTTPProxyAllocatesPort(t *testing.T) {
	t.Parallel()
	cfg := &config.Supervisor{
		Scope: []string{"OPENAI_API_KEY"},
		Child: config.Child{
			Handoff: &config.ChildHandoff{
				Mode:       config.HandoffModeHTTPProxy,
				ListenAddr: "127.0.0.1:0",
			},
		},
	}
	l := minimalLifecycle(cfg)
	overlay, err := l.buildChildEnvOverlay(context.Background())
	if err != nil {
		t.Fatalf("buildChildEnvOverlay: %v", err)
	}
	got, ok := overlay[config.EnvVarBindPort]
	if !ok {
		t.Fatalf("overlay missing %s; got %v", config.EnvVarBindPort, overlay)
	}
	port, err := strconv.ParseUint(got, 10, 16)
	if err != nil {
		t.Fatalf("overlay %s=%q not a valid uint16: %v", config.EnvVarBindPort, got, err)
	}
	if port == 0 {
		t.Fatalf("overlay %s must be non-zero", config.EnvVarBindPort)
	}
	if uint16(port) != l.backendPort {
		t.Fatalf("Lifecycle.backendPort=%d, overlay=%d", l.backendPort, port)
	}
}

// equalSlices is a small string-slice equality helper used by the
// nil/empty overlay test above.
func equalSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
