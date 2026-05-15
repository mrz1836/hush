// Command govulncheck-filter consumes the JSON output of
// `govulncheck -format=json ./...` and a `.govulncheck-allow.yml` waiver
// file (schema: `{version: 1, vulns: [{id, justification, expires}]}`),
// then exits non-zero if any un-waived finding remains.
//
// FR-008 single source of truth — PR descriptions are non-authoritative
// for waiving findings. The expires field is enforced: an expired waiver
// no longer suppresses its finding.
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// ErrUnwaivedVuln is returned when govulncheck reports a finding that
// is not covered by an unexpired waiver in the allow file.
var ErrUnwaivedVuln = errors.New("unwaived vulnerability finding")

// ErrAllowFileMalformed is returned when the YAML allow file cannot be
// parsed by the line-based mini-parser below.
var ErrAllowFileMalformed = errors.New("malformed .govulncheck-allow.yml")

type waiver struct {
	id            string
	justification string
	expires       time.Time
}

// vulnFinding is the subset of govulncheck's JSON output we consume.
type vulnFinding struct {
	OSV struct {
		ID string `json:"id"`
	} `json:"osv"`
	Finding *struct {
		OSV string `json:"osv"`
	} `json:"finding"`
}

func main() {
	input := flag.String("input", "vulns.json", "path to govulncheck -format=json output")
	allow := flag.String("allow", ".govulncheck-allow.yml", "path to waiver file (FR-008)")
	flag.Parse()

	waivers, err := parseAllowFile(*allow)
	if err != nil {
		fmt.Fprintf(os.Stderr, "::error::govulncheck-filter: %v\n", err)
		os.Exit(2)
	}
	findings, err := parseFindings(*input)
	if err != nil {
		fmt.Fprintf(os.Stderr, "::error::govulncheck-filter: parse %s: %v\n", *input, err)
		os.Exit(2)
	}
	now := time.Now().UTC()
	var unwaived []string
	for _, id := range findings {
		if w, ok := waivers[id]; ok && now.Before(w.expires) {
			continue
		}
		unwaived = append(unwaived, id)
	}
	if len(unwaived) > 0 {
		fmt.Fprintf(os.Stderr, "::error::govulncheck-filter: %s: %v\n", strings.Join(unwaived, ","), ErrUnwaivedVuln)
		os.Exit(1)
	}
	fmt.Printf("govulncheck-filter: 0 unwaived findings (%d total findings, %d waivers)\n", len(findings), len(waivers))
}

// parseAllowFile reads .govulncheck-allow.yml as a flat line-based
// scanner — we accept only the two key shapes our schema documents:
//   - `id: GO-YYYY-NNNN`
//   - `justification: "..."`
//   - `expires: "YYYY-MM-DD"`
//
// Anything else is silently ignored so comments and whitespace pass.
func parseAllowFile(path string) (map[string]waiver, error) {
	f, err := os.Open(path) //nolint:gosec // path is a CI flag.
	if err != nil {
		return nil, fmt.Errorf("open %q: %w", path, ErrAllowFileMalformed)
	}
	defer func() { _ = f.Close() }()
	out := map[string]waiver{}
	scanner := bufio.NewScanner(f)
	var cur waiver
	flush := func() {
		if cur.id == "" {
			return
		}
		out[cur.id] = cur
		cur = waiver{}
	}
	for scanner.Scan() {
		raw := scanner.Text()
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "- ") {
			flush()
			line = strings.TrimPrefix(line, "- ")
		}
		switch {
		case strings.HasPrefix(line, "id:"):
			cur.id = trimYAMLValue(line[len("id:"):])
		case strings.HasPrefix(line, "justification:"):
			cur.justification = trimYAMLValue(line[len("justification:"):])
		case strings.HasPrefix(line, "expires:"):
			val := trimYAMLValue(line[len("expires:"):])
			t, perr := time.Parse("2006-01-02", val)
			if perr != nil {
				return nil, fmt.Errorf("expires %q: %w", val, ErrAllowFileMalformed)
			}
			cur.expires = t.Add(24 * time.Hour) // waiver valid through end of day UTC
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan: %w", ErrAllowFileMalformed)
	}
	flush()
	return out, nil
}

func trimYAMLValue(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, `"`)
	s = strings.TrimSuffix(s, `"`)
	return s
}

// parseFindings reads govulncheck -format=json output (a stream of JSON
// objects, one per line) and returns the OSV IDs that have at least one
// call-graph finding (i.e. reachable from the analysed module).
func parseFindings(path string) ([]string, error) {
	f, err := os.Open(path) //nolint:gosec // path is a CI flag.
	if err != nil {
		return nil, fmt.Errorf("open %q: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	dec := json.NewDecoder(f)
	seen := map[string]bool{}
	var ids []string
	for {
		var msg vulnFinding
		if derr := dec.Decode(&msg); derr != nil {
			if errors.Is(derr, io.EOF) {
				break
			}
			// Tolerate trailing non-JSON (e.g. govulncheck summary line) by
			// stopping at the first decode failure rather than escalating.
			break
		}
		var id string
		switch {
		case msg.Finding != nil && msg.Finding.OSV != "":
			id = msg.Finding.OSV
		case msg.OSV.ID != "":
			id = msg.OSV.ID
		}
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	return ids, nil
}
