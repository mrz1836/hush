package validators

import "log/slog"

// SetLoggerForTest is a test-build-only seam: it lets tests inject a
// *slog.Logger into a concrete provider Validator so they can capture
// the log records without racing slog.Default(). The panic on the
// default branch guards an invariant that can only fire under test
// development (someone passes a non-*provider Validator).
func SetLoggerForTest(v Validator, logger *slog.Logger) {
	concrete, ok := v.(*provider)
	if !ok {
		panic("validators: SetLoggerForTest called with non-*provider Validator")
	}
	concrete.logger = logger
}

// ProviderFieldsForTest extracts the unexported (name, url, builder presence,
// extra-headers, http.Client) fields from a Validator constructed via the
// public New* constructors. Returns ok=false if v isn't a *provider.
//
// Used by TestProvider_DispatchTable to assert each constructor wires the
// correct per-provider configuration after the 5→1 consolidation.
func ProviderFieldsForTest(v Validator) (name, url string, builderSet bool, extra map[string][]string, hasClient, ok bool) {
	concrete, isProvider := v.(*provider)
	if !isProvider {
		return "", "", false, nil, false, false
	}
	out := make(map[string][]string, len(concrete.extra))
	for k, vv := range concrete.extra {
		out[k] = append([]string(nil), vv...)
	}
	return concrete.name, concrete.url, concrete.builder != nil, out, concrete.client != nil, true
}

// CanonicalEndpointsForTest exposes the per-provider URL constants to
// dispatch-table tests in package validators_test. Exporting them via this
// test-only seam keeps the literal production hostnames out of *_test.go
// (which TestPackage_NoLiveProviderHosts forbids outside fixture context).
func CanonicalEndpointsForTest() map[string]string {
	return map[string]string{
		anthropicName:      anthropicEndpoint,
		anthropicOAuthName: anthropicEndpoint,
		openaiName:         openaiEndpoint,
		googleAIName:       googleAIEndpoint,
		githubName:         githubEndpoint,
	}
}

// CanonicalExtraHeadersForTest exposes the expected per-provider extra-header
// set. Test-only; same rationale as CanonicalEndpointsForTest.
func CanonicalExtraHeadersForTest() map[string]map[string][]string {
	return map[string]map[string][]string{
		anthropicName:      {"anthropic-version": {anthropicVersionHeader}},
		anthropicOAuthName: {"anthropic-version": {anthropicVersionHeader}},
		openaiName:         {},
		googleAIName:       {},
		githubName:         {"Accept": {"application/vnd.github+json"}},
	}
}
