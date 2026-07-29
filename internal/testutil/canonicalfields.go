package testutil

import (
	"reflect"
	"sort"
)

// ClaimSignedPayloadFields returns the authoritative field set of the
// /claim signed payload — the exact JSON key list that sign.CanonicalJSON
// emits for the struct the client signs and the server verifies.
//
// It is a function, not a package-level var, so no caller can mutate the
// list out from under a parallel test.
//
// Four packages hand-maintain their own copy of this struct:
//
//	internal/server        signedPayload         (the source of truth)
//	internal/cli           claimSignedPayload    (interactive `hush request`)
//	internal/supervise     claimSignedPayload    (supervisor claims)
//	tests/integration/…    claimSignedPayloadJSON (harness)
//
// CanonicalJSON emits every exported field and never consults omitempty,
// so a field present in one copy and absent from another changes the
// signed bytes and breaks every claim from that client with
// bad_signature — at runtime only, with no compile-time symptom.
//
// That is not hypothetical: adding standing_lease + client_machine_index
// to the server without adding them to internal/cli silently broke every
// interactive `hush request` host-wide for weeks. The integration harness
// keeps its own correct copy, so the integration suite could not see it.
//
// A sixth copy lives in internal/cli's own tests
// (serverClaimSignedPayloadForTest), which stands in for the server when
// asserting that a client signature verifies. It drifted too — so the one
// test whose entire purpose was to catch this bug compared the client
// against an equally stale stand-in and passed.
//
// Each copy has a test asserting it matches this list. Adding a field to
// the signed payload therefore fails every one of those tests until all
// copies are updated in lockstep — which is the point.
func ClaimSignedPayloadFields() []string {
	return []string{
		"agent_identity",
		"agent_model",
		"client_machine_index",
		"command_preview",
		"ephemeral_pubkey",
		"force_approval",
		"machine_name",
		"nonce",
		"reason",
		"recent_summary",
		"request_id",
		"scope",
		"session_type",
		"standing_lease",
		"supervisor_name",
		"timestamp",
		"tool_name",
		"ttl",
	}
}

// CanonicalFieldNames returns the sorted JSON key names that
// sign.CanonicalJSON would emit for v's struct type. It deliberately
// reimplements the name resolution in internal/transport/sign rather than
// calling it, so a regression in that resolver cannot mask a drift by
// changing both sides at once.
//
// v may be a struct or a pointer to one. Unexported fields are skipped.
// A field with no json tag (or a "-" / empty tag) resolves to its Go
// field name, matching the resolver's behaviour.
func CanonicalFieldNames(v any) []string {
	t := reflect.TypeOf(v)
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == nil || t.Kind() != reflect.Struct {
		return nil
	}

	names := make([]string, 0, t.NumField())
	for i := range t.NumField() {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		names = append(names, canonicalFieldName(f))
	}
	sort.Strings(names)
	return names
}

// canonicalFieldName mirrors sign.resolveFieldName: the first
// comma-separated token of the json tag, falling back to the Go field
// name when the tag is absent, empty, or "-".
func canonicalFieldName(f reflect.StructField) string {
	tag, ok := f.Tag.Lookup("json")
	if !ok || tag == "" || tag == "-" {
		return f.Name
	}
	for i, c := range tag {
		if c == ',' {
			tag = tag[:i]
			break
		}
	}
	if tag == "" {
		return f.Name
	}
	return tag
}
