package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// minProjectDefault matches the CLI flag default; tests use it directly.
const minProjectDefault = 90.0

// secCritOnly is a synthetic cover.out body that puts every
// security-critical package at exactly 100% and one bystander package
// at 100% to lift the project total.
func secCritOnly(modePrefix string, perPkg map[string]int) string {
	var b strings.Builder
	b.WriteString(modePrefix)
	for pkg, n := range perPkg {
		for i := 0; i < n; i++ {
			b.WriteString(modulePrefix + pkg + "/x.go:1.1,1.2 1 1\n")
		}
	}
	return b.String()
}

func TestCoverageThreshold_ProjectGEThreshold(t *testing.T) {
	body := "mode: atomic\n"
	for _, pkg := range securityCriticalPackages {
		body += modulePrefix + pkg + "/x.go:1.1,1.2 1 1\n"
	}
	body += modulePrefix + "internal/extra/x.go:1.1,1.2 100 100\n"
	s, err := parseCoverOut(strings.NewReader(body))
	if err != nil {
		t.Fatalf("parseCoverOut: unexpected error: %v", err)
	}
	if err := checkThresholds(s, minProjectDefault); err != nil {
		t.Fatalf("checkThresholds: want nil, got %v", err)
	}
}

func TestCoverageThreshold_SecurityCriticalEQ100(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantErr error
	}{
		{
			name: "all_sec_crit_at_100",
			body: func() string {
				body := "mode: atomic\n"
				for _, pkg := range securityCriticalPackages {
					body += modulePrefix + pkg + "/x.go:1.1,1.2 1 1\n"
				}
				body += modulePrefix + "internal/extra/x.go:1.1,1.2 10 10\n"
				return body
			}(),
			wantErr: nil,
		},
		{
			name: "one_sec_crit_at_99.9",
			body: func() string {
				body := "mode: atomic\n"
				for _, pkg := range securityCriticalPackages {
					if pkg == "internal/audit" {
						body += modulePrefix + pkg + "/a.go:1.1,1.2 999 1\n"
						body += modulePrefix + pkg + "/a.go:2.1,2.2 1 0\n"
					} else {
						body += modulePrefix + pkg + "/x.go:1.1,1.2 1 1\n"
					}
				}
				body += modulePrefix + "internal/extra/x.go:1.1,1.2 10 10\n"
				return body
			}(),
			wantErr: ErrCoverageBelowThreshold,
		},
		{
			name: "non_sec_crit_at_99.9_passes",
			body: func() string {
				body := "mode: atomic\n"
				for _, pkg := range securityCriticalPackages {
					body += modulePrefix + pkg + "/x.go:1.1,1.2 1 1\n"
				}
				body += modulePrefix + "internal/extra/x.go:1.1,1.2 999 1\n"
				body += modulePrefix + "internal/extra/x.go:2.1,2.2 1 0\n"
				return body
			}(),
			wantErr: nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, err := parseCoverOut(strings.NewReader(tc.body))
			if err != nil {
				t.Fatalf("parseCoverOut: unexpected error: %v", err)
			}
			got := checkThresholds(s, minProjectDefault)
			if tc.wantErr == nil && got != nil {
				t.Fatalf("checkThresholds: want nil, got %v", got)
			}
			if tc.wantErr != nil && !errors.Is(got, tc.wantErr) {
				t.Fatalf("checkThresholds: want %v, got %v", tc.wantErr, got)
			}
		})
	}
}

func TestCoverageThreshold_FailsBelowThreshold(t *testing.T) {
	body := "mode: atomic\n"
	for _, pkg := range securityCriticalPackages {
		body += modulePrefix + pkg + "/x.go:1.1,1.2 1 1\n"
	}
	// Drag the project total down below 90% with a heavily-uncovered extra package.
	body += modulePrefix + "internal/extra/x.go:1.1,1.2 1000 0\n"
	s, err := parseCoverOut(strings.NewReader(body))
	if err != nil {
		t.Fatalf("parseCoverOut: unexpected error: %v", err)
	}
	got := checkThresholds(s, minProjectDefault)
	if !errors.Is(got, ErrCoverageBelowThreshold) {
		t.Fatalf("checkThresholds: want ErrCoverageBelowThreshold, got %v", got)
	}
}

func TestCoverageThreshold_FailsOnMissingPackage(t *testing.T) {
	body := "mode: atomic\n"
	for _, pkg := range securityCriticalPackages {
		if pkg == "internal/audit" {
			continue
		}
		body += modulePrefix + pkg + "/x.go:1.1,1.2 1 1\n"
	}
	body += modulePrefix + "internal/extra/x.go:1.1,1.2 10 10\n"
	s, err := parseCoverOut(strings.NewReader(body))
	if err != nil {
		t.Fatalf("parseCoverOut: unexpected error: %v", err)
	}
	got := checkThresholds(s, minProjectDefault)
	if !errors.Is(got, ErrCoverageBelowThreshold) {
		t.Fatalf("checkThresholds: want ErrCoverageBelowThreshold for missing internal/audit, got %v", got)
	}
	if !strings.Contains(got.Error(), "internal/audit") {
		t.Fatalf("checkThresholds: error %q missing 'internal/audit'", got)
	}
}

func TestCoverageThreshold_FailsOnMalformedCoverOut(t *testing.T) {
	_, err := parseCoverOut(strings.NewReader("this is not a cover.out"))
	if !errors.Is(err, ErrMalformedCoverOut) {
		t.Fatalf("parseCoverOut: want ErrMalformedCoverOut, got %v", err)
	}
}

func TestCoverageThreshold_FailsOnEmptyCoverOut(t *testing.T) {
	_, err := parseCoverOut(strings.NewReader("mode: atomic\n"))
	if !errors.Is(err, ErrMalformedCoverOut) {
		t.Fatalf("parseCoverOut: want ErrMalformedCoverOut for body-less input, got %v", err)
	}
}

func TestSecurityCriticalListMatchesConstitution(t *testing.T) {
	// The test file lives at .github/scripts/coverage-threshold/compute_test.go;
	// constitution is at .specify/memory/constitution.md from repo root.
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	path := filepath.Join(wd, "..", "..", "..", ".specify", "memory", "constitution.md")
	if err := verifyConstitutionList(path); err != nil {
		t.Fatalf("verifyConstitutionList: want nil, got %v", err)
	}
}
