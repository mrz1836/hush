//go:build integration

package deploy_test

import (
	"bufio"
	"bytes"
	"encoding/xml"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// deployDir returns the absolute path to the committed deploy/ tree.
func deployDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed")
	}
	return filepath.Join(filepath.Dir(file), "..", "..", "deploy")
}

// plistDoc is a minimal `encoding/xml` decode target for the launchd
// plist's <dict> body. We only validate the keys we lock; richer
// plist-schema validation is out of scope.
type plistDoc struct {
	XMLName xml.Name `xml:"plist"`
	Dict    struct {
		Items []plistItem `xml:",any"`
	} `xml:"dict"`
}

// plistItem is a single element inside <dict>. Apple plists are
// key/value pairs encoded as sibling elements — we record the tag and
// the inner-text so the test can walk pairs.
type plistItem struct {
	XMLName xml.Name
	Value   string   `xml:",chardata"`
	Strings []string `xml:"string"`
}

func TestDeploy_PlistParsesAsXML(t *testing.T) {
	plistPath := filepath.Join(deployDir(t), "hush.plist")
	data, err := os.ReadFile(plistPath)
	if err != nil {
		t.Fatalf("read plist: %v", err)
	}
	var doc plistDoc
	if err := xml.Unmarshal(data, &doc); err != nil {
		t.Fatalf("plist does not parse as XML: %v", err)
	}

	// Walk the flat <dict> body looking for the <key>UserName</key>
	// followed by its sibling <string>, and the <key>ProgramArguments</key>
	// followed by its sibling <array>. We use a small state machine.
	var (
		userName  string
		firstArg  string
		nextIsVal string
	)
	for _, item := range doc.Dict.Items {
		switch item.XMLName.Local {
		case "key":
			nextIsVal = strings.TrimSpace(item.Value)
		case "string":
			if nextIsVal == "UserName" {
				userName = strings.TrimSpace(item.Value)
			}
			nextIsVal = ""
		case "array":
			if nextIsVal == "ProgramArguments" && len(item.Strings) > 0 {
				firstArg = strings.TrimSpace(item.Strings[0])
			}
			nextIsVal = ""
		default:
			nextIsVal = ""
		}
	}

	if userName == "" {
		t.Fatalf("plist missing <key>UserName</key>")
	}
	if userName == "root" || userName == "0" {
		t.Errorf("UserName must be non-root, got %q", userName)
	}
	if firstArg != "/usr/local/bin/hush" {
		t.Errorf("first ProgramArguments entry must be /usr/local/bin/hush, got %q", firstArg)
	}
}

func TestDeploy_ServiceParsesAsINI(t *testing.T) {
	unitPath := filepath.Join(deployDir(t), "hush.service")
	data, err := os.ReadFile(unitPath)
	if err != nil {
		t.Fatalf("read unit: %v", err)
	}

	var (
		section       string
		seenUnit      bool
		seenService   bool
		seenInstall   bool
		userInService string
		execStart     string
	)
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.TrimSuffix(strings.TrimPrefix(line, "["), "]")
			switch section {
			case "Unit":
				seenUnit = true
			case "Service":
				seenService = true
			case "Install":
				seenInstall = true
			}
			continue
		}
		if section == "Service" {
			if strings.HasPrefix(line, "User=") {
				userInService = strings.TrimPrefix(line, "User=")
			}
			if strings.HasPrefix(line, "ExecStart=") {
				execStart = strings.TrimPrefix(line, "ExecStart=")
			}
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan unit: %v", err)
	}

	if !seenUnit || !seenService || !seenInstall {
		t.Errorf("unit missing required sections (Unit=%v Service=%v Install=%v)",
			seenUnit, seenService, seenInstall)
	}
	if userInService != "@HUSH_USER@" {
		t.Errorf("committed User= must be @HUSH_USER@ before substitution, got %q", userInService)
	}
	if !strings.HasPrefix(execStart, "/usr/local/bin/hush") {
		t.Errorf("ExecStart= must begin with /usr/local/bin/hush, got %q", execStart)
	}
}

func TestDeploy_LauncherTemplateExecsSupervise(t *testing.T) {
	tmpl := filepath.Join(deployDir(t), "supervise-launch.sh.template")
	data, err := os.ReadFile(tmpl)
	if err != nil {
		t.Fatalf("read template: %v", err)
	}

	// (a) bash -n parses cleanly.
	cmd := exec.Command("bash", "-n", tmpl)
	if out, runErr := cmd.CombinedOutput(); runErr != nil {
		t.Fatalf("bash -n failed on launcher template: %v\n%s", runErr, out)
	}

	body := string(data)

	// (b) the literal `hush supervise` appears at least once.
	if !strings.Contains(body, "hush supervise") {
		t.Errorf("launcher template must contain `hush supervise`")
	}

	// (c) zero active (non-comment) `hush request --exec` lines.
	scanner := bufio.NewScanner(strings.NewReader(body))
	lineNum := 0
	activeMatches := 0
	for scanner.Scan() {
		lineNum++
		line := scanner.Text()
		trimmed := strings.TrimLeft(line, " \t")
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.Contains(line, "hush request --exec") {
			activeMatches++
			t.Errorf("active `hush request --exec` found at line %d: %q", lineNum, line)
		}
	}
	if activeMatches != 0 {
		t.Errorf("%d active `hush request --exec` invocation(s) found", activeMatches)
	}

	// (d) all three placeholder tokens must appear.
	for _, ph := range []string{"<NAME>", "<KEYCHAIN_ITEM>", "<CONFIG_PATH>"} {
		if !strings.Contains(body, ph) {
			t.Errorf("placeholder %s missing from template", ph)
		}
	}

	// (e) header comment block (first ~40 lines) documents placeholders
	// and includes a DO-NOT warning.
	headLines := strings.SplitN(body, "\n", 41)
	head := strings.Join(headLines[:len(headLines)-1], "\n")
	if !strings.Contains(head, "SUBSTITUTE") {
		t.Errorf("header should contain a SUBSTITUTE instruction block")
	}
	if !strings.Contains(head, "DO NOT") {
		t.Errorf("header should contain a DO NOT warning against `hush request --exec`")
	}
}

func TestDeploy_NoOperatorSpecificNames(t *testing.T) {
	dir := deployDir(t)
	files := []string{
		filepath.Join(dir, "hush.plist"),
		filepath.Join(dir, "hush.service"),
		filepath.Join(dir, "install.sh"),
		filepath.Join(dir, "supervise-launch.sh.template"),
	}
	deny := regexp.MustCompile(`(?i)openclaw|hermes|mrz|100\.90\.|tag:trusted`)
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Errorf("read %s: %v", f, err)
			continue
		}
		if loc := deny.FindIndex(data); loc != nil {
			t.Errorf("operator-specific token in %s at byte offset %d: %q",
				f, loc[0], string(data[loc[0]:loc[1]]))
		}
	}
}

func TestDeploy_AllShellFilesParse(t *testing.T) {
	dir := deployDir(t)
	var shellFiles []string
	err := filepath.Walk(dir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			return nil
		}
		name := info.Name()
		if strings.HasSuffix(name, ".sh") || strings.HasSuffix(name, ".template") {
			shellFiles = append(shellFiles, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk deploy/: %v", err)
	}
	if len(shellFiles) == 0 {
		t.Fatal("no shell files found under deploy/")
	}
	for _, f := range shellFiles {
		out, runErr := exec.Command("bash", "-n", f).CombinedOutput()
		if runErr != nil {
			t.Errorf("bash -n failed on %s: %v\n%s", f, runErr, out)
		}
	}
}
