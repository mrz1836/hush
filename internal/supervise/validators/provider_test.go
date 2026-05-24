package validators_test

import (
	"net/http"
	"testing"

	"github.com/mrz1836/hush/internal/supervise/validators"
)

// TestProvider_DispatchTable asserts the 5→1 struct consolidation didn't
// change the wiring: each public New* constructor still returns a Validator
// configured with the canonical per-provider name, endpoint (from the
// production constants exposed via CanonicalEndpointsForTest), extra-header
// set, and non-nil builder + client. Catches future drift if a constructor
// is edited without updating its row here.
//
//nolint:gocognit // table-driven test with multi-field assertion; complexity is structural
func TestProvider_DispatchTable(t *testing.T) {
	t.Parallel()
	endpoints := validators.CanonicalEndpointsForTest()
	extras := validators.CanonicalExtraHeadersForTest()
	cases := []struct {
		name string
		make func(*http.Client) validators.Validator
	}{
		{"anthropic", validators.NewAnthropic},
		{"anthropic-oauth", validators.NewAnthropicOAuth},
		{"openai", validators.NewOpenAI},
		{"google-ai", validators.NewGoogleAI},
		{"github", validators.NewGitHub},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			v := tc.make(nil)
			gotName, gotURL, builderSet, extra, hasClient, ok := validators.ProviderFieldsForTest(v)
			if !ok {
				t.Fatalf("constructor returned non-*provider Validator: %T", v)
			}
			if gotName != tc.name {
				t.Errorf("name = %q, want %q", gotName, tc.name)
			}
			if gotURL != endpoints[tc.name] {
				t.Errorf("url = %q, want %q", gotURL, endpoints[tc.name])
			}
			if !builderSet {
				t.Errorf("builder not set")
			}
			if !hasClient {
				t.Errorf("client not set")
			}
			if !extraEqual(extra, extras[tc.name]) {
				t.Errorf("extra headers = %v, want %v", extra, extras[tc.name])
			}
		})
	}
}

// extraEqual compares two http.Header-equivalent maps for full equality.
func extraEqual(a, b map[string][]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, av := range a {
		bv, ok := b[k]
		if !ok || len(av) != len(bv) {
			return false
		}
		for i := range av {
			if av[i] != bv[i] {
				return false
			}
		}
	}
	return true
}

// TestProvider_RegistryReturnsConfiguredValidators asserts every name the
// Registry exposes returns the SAME concrete *provider struct (now that
// there's only one type). Future-proofs the registry against accidentally
// returning a stub or wrapper.
func TestProvider_RegistryReturnsConfiguredValidators(t *testing.T) {
	t.Parallel()
	reg := validators.NewRegistry(nil)
	names := []string{"anthropic", "anthropic-oauth", "openai", "google-ai", "github"}
	for _, name := range names {
		v, ok := reg.Get(name)
		if !ok {
			t.Errorf("Registry.Get(%q): not found", name)
			continue
		}
		gotName, _, _, _, _, isProvider := validators.ProviderFieldsForTest(v)
		if !isProvider {
			t.Errorf("Registry.Get(%q): not *provider, got %T", name, v)
			continue
		}
		if gotName != name {
			t.Errorf("Registry.Get(%q): inner name %q mismatch", name, gotName)
		}
	}
	if _, ok := reg.Get("not-a-provider"); ok {
		t.Error("Registry.Get(\"not-a-provider\"): want false, got true")
	}
}
