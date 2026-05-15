package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
)

// modulePrefix is stripped from cover.out file paths to derive a package path
// rooted at the module. cover.out emits absolute import paths; we want
// "internal/keys" not "github.com/mrz1836/hush/internal/keys".
const modulePrefix = "github.com/mrz1836/hush/"

const (
	beginMarker = "<!-- security-critical-packages: BEGIN"
	endMarker   = "<!-- security-critical-packages: END -->"
)

// ErrMalformedCoverOut is returned when cover.out is missing, empty, or
// cannot be parsed per Go's text coverage format (FR-015).
var ErrMalformedCoverOut = errors.New("malformed cover.out")

// ErrCoverageBelowThreshold is returned when project-wide coverage is
// below the configured minimum or any security-critical package is
// below 100% (FR-013 / FR-014).
var ErrCoverageBelowThreshold = errors.New("coverage below threshold")

// ErrConstitutionMismatch is returned when the security-critical list
// hardcoded in this tool diverges from the fenced block in the
// constitution (FR-016 byte-equality self-test).
var ErrConstitutionMismatch = errors.New("security-critical list diverges from constitution")

// securityCriticalPackages is the canonical security-critical set. It
// MUST match the fenced block in .specify/memory/constitution.md
// byte-for-byte; verifyConstitutionList enforces this.
//
//nolint:gochecknoglobals // FR-016 byte-equality anchor — see TestSecurityCriticalListMatchesConstitution.
var securityCriticalPackages = []string{
	"internal/keys",
	"internal/vault",
	"internal/vault/securebytes",
	"internal/token",
	"internal/transport/sign",
	"internal/transport/ecies",
	"internal/audit",
}

// snapshot is the per-package + project totals derived from cover.out.
type snapshot struct {
	totalPct       float64
	perPkgPct      map[string]float64
	missingSecCrit []string
}

// parseCoverOut reads a Go coverage profile (cover.out) and returns a
// snapshot. Returns ErrMalformedCoverOut wrapped with the offending line
// for any deviation from Go's documented format.
func parseCoverOut(r io.Reader) (snapshot, error) {
	scanner := bufio.NewScanner(r)
	if !scanner.Scan() {
		return snapshot{}, fmt.Errorf("empty input: %w", ErrMalformedCoverOut)
	}
	mode := scanner.Text()
	switch mode {
	case "mode: atomic", "mode: count", "mode: set":
	default:
		return snapshot{}, fmt.Errorf("first line %q: %w", mode, ErrMalformedCoverOut)
	}

	covered := map[string]int64{}
	total := map[string]int64{}
	var projCovered, projTotal int64
	lines := 0
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		lines++
		fields := strings.Fields(line)
		if len(fields) != 3 {
			return snapshot{}, fmt.Errorf("line %q: expected 3 fields, got %d: %w", line, len(fields), ErrMalformedCoverOut)
		}
		colon := strings.IndexByte(fields[0], ':')
		if colon <= 0 {
			return snapshot{}, fmt.Errorf("line %q: missing file:range delimiter: %w", line, ErrMalformedCoverOut)
		}
		filePath := fields[0][:colon]
		stmts, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			return snapshot{}, fmt.Errorf("line %q: numStatements %q: %w", line, fields[1], ErrMalformedCoverOut)
		}
		hits, err := strconv.ParseInt(fields[2], 10, 64)
		if err != nil {
			return snapshot{}, fmt.Errorf("line %q: hitCount %q: %w", line, fields[2], ErrMalformedCoverOut)
		}
		pkg := packageFromFile(filePath)
		if pkg == "" {
			continue
		}
		total[pkg] += stmts
		projTotal += stmts
		if hits > 0 {
			covered[pkg] += stmts
			projCovered += stmts
		}
	}
	if err := scanner.Err(); err != nil {
		return snapshot{}, fmt.Errorf("scan: %w", ErrMalformedCoverOut)
	}
	if lines == 0 || projTotal == 0 {
		return snapshot{}, fmt.Errorf("zero statement lines: %w", ErrMalformedCoverOut)
	}

	perPkg := make(map[string]float64, len(total))
	for pkg, tot := range total {
		if tot == 0 {
			continue
		}
		perPkg[pkg] = float64(covered[pkg]) / float64(tot) * 100.0
	}
	return snapshot{
		totalPct:  float64(projCovered) / float64(projTotal) * 100.0,
		perPkgPct: perPkg,
	}, nil
}

// packageFromFile turns a cover.out file path into a module-relative
// package path. Returns "" for paths outside the module (vendored,
// generated, or stdlib).
func packageFromFile(filePath string) string {
	if !strings.HasPrefix(filePath, modulePrefix) {
		return ""
	}
	rel := strings.TrimPrefix(filePath, modulePrefix)
	return path.Dir(rel)
}

// checkThresholds returns nil iff project coverage ≥ minProject AND
// every security-critical package is present AND at 100.0%.
func checkThresholds(s snapshot, minProject float64) error {
	var failures []string
	if s.totalPct < minProject {
		failures = append(failures, fmt.Sprintf("project %.1f%% < %.1f%%", s.totalPct, minProject))
	}
	for _, pkg := range securityCriticalPackages {
		pct, ok := s.perPkgPct[pkg]
		if !ok {
			failures = append(failures, fmt.Sprintf("%s: missing from coverage report (FR-015 missing-report-is-failure)", pkg))
			continue
		}
		if pct < 100.0 {
			failures = append(failures, fmt.Sprintf("%s: %.1f%% < 100.0%%", pkg, pct))
		}
	}
	if len(failures) == 0 {
		return nil
	}
	return fmt.Errorf("%s: %w", strings.Join(failures, "; "), ErrCoverageBelowThreshold)
}

// verifyConstitutionList reads the constitution at constitutionPath and
// asserts that the fenced security-critical block matches
// securityCriticalPackages byte-for-byte (FR-016).
func verifyConstitutionList(constitutionPath string) error {
	data, err := os.ReadFile(constitutionPath) //nolint:gosec // constitutionPath is a CI-controlled flag.
	if err != nil {
		return fmt.Errorf("read constitution %q: %w", constitutionPath, ErrConstitutionMismatch)
	}
	begin := bytes.Index(data, []byte(beginMarker))
	end := bytes.Index(data, []byte(endMarker))
	if begin < 0 || end < 0 || end < begin {
		return fmt.Errorf("constitution missing BEGIN/END markers: %w", ErrConstitutionMismatch)
	}
	beginLineEnd := bytes.IndexByte(data[begin:], '\n')
	if beginLineEnd < 0 {
		return fmt.Errorf("constitution malformed BEGIN line: %w", ErrConstitutionMismatch)
	}
	block := data[begin+beginLineEnd+1 : end]
	got := strings.TrimSpace(string(block))
	want := strings.Join(securityCriticalPackages, "\n")
	if got != want {
		return fmt.Errorf("got %q want %q: %w", got, want, ErrConstitutionMismatch)
	}
	return nil
}

// writeReport prints the PASS summary to w in the deterministic order
// security-critical packages were declared.
func writeReport(w io.Writer, s snapshot, minProject float64) {
	fmt.Fprintf(w, "coverage-threshold: project %.1f%% >= %.1f%% PASS\n", s.totalPct, minProject)
	keys := make([]string, 0, len(securityCriticalPackages))
	keys = append(keys, securityCriticalPackages...)
	sort.Strings(keys)
	for _, pkg := range securityCriticalPackages {
		fmt.Fprintf(w, "coverage-threshold: %-32s %.1f%% = 100.0%% PASS\n", pkg, s.perPkgPct[pkg])
	}
	fmt.Fprintf(w, "coverage-threshold: PASS (%d checks)\n", 1+len(securityCriticalPackages))
}
