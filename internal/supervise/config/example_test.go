package config

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// operatorSpecificForbidden is the seed list of identifiers that MUST
// NOT appear in deploy/examples/supervisors/example-daemon.toml per
// FR-007 / SC-003 of SDD-30. New forbidden identifiers are added one
// at a time as discoveries surface (clarification 1). Start empty —
// no historically-leaked identifiers are known at template authoring
// time.
//
//nolint:gochecknoglobals // sentinel-class: seed list referenced by a single test
var operatorSpecificForbidden = []string{}

// exampleTemplatePath is the relative path from this test file
// (internal/supervise/config/) to the canonical operator-facing supervisor
// TOML template at deploy/examples/supervisors/example-daemon.toml. The
// three ".." segments walk config -> supervise -> internal -> repo-root.
//
//nolint:gochecknoglobals // sentinel-class: relative path shared by two tests in this file
var exampleTemplatePath = filepath.Join(
	"..", "..", "..",
	"deploy", "examples", "supervisors", "example-daemon.toml",
)

// TestExamples_GenericTOMLValidates feeds the canonical operator-facing
// supervisor TOML template through the SDD-18 loader and asserts zero
// validation error. Guards FR-005 / SC-001 of SDD-30.
func TestExamples_GenericTOMLValidates(t *testing.T) {
	t.Parallel()

	sup, err := Load(context.Background(), exampleTemplatePath)
	require.NoError(t, err, "the canonical operator-facing template MUST validate against the SDD-18 loader as-shipped (FR-005)")
	require.NotNil(t, sup)

	assert.Equal(t, "example-daemon", sup.Name)
	assert.Equal(t, "supervisor", sup.SessionType)
}

// TestExamples_NoOperatorSpecificNames asserts that no entry in the
// operatorSpecificForbidden seed list appears anywhere in the
// canonical operator-facing supervisor TOML template. Guards FR-007 /
// SC-003 of SDD-30. The seed list starts empty per clarification 1;
// new forbidden identifiers are appended one at a time as discoveries
// surface.
func TestExamples_NoOperatorSpecificNames(t *testing.T) {
	t.Parallel()

	body, err := os.ReadFile(exampleTemplatePath)
	require.NoError(t, err)

	contents := string(body)
	for _, forbidden := range operatorSpecificForbidden {
		assert.False(
			t,
			strings.Contains(contents, forbidden),
			"the canonical operator-facing template MUST NOT contain %q (FR-007)",
			forbidden,
		)
	}
}
