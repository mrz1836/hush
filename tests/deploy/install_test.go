//go:build integration

package deploy_test

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// testPaths resolves repository-anchored paths for the deploy
// artefacts and test fixtures. It is built from runtime.Caller(0) so the
// tests work no matter where `go test` is invoked from.
type testPaths struct {
	repoRoot   string
	installSh  string
	tmutilStub string
	fakeHush   string
}

func resolveTestPaths(t *testing.T) testPaths {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed; cannot locate test file")
	}
	repoRoot := filepath.Join(filepath.Dir(file), "..", "..")
	return testPaths{
		repoRoot:   repoRoot,
		installSh:  filepath.Join(repoRoot, "deploy", "install.sh"),
		tmutilStub: filepath.Join(filepath.Dir(file), "testdata", "tmutil_stub.sh"),
		fakeHush:   filepath.Join(filepath.Dir(file), "testdata", "fake-hush"),
	}
}

// installRun captures the result of a single install.sh invocation.
type installRun struct {
	stdout   []byte
	stderr   []byte
	exitCode int
}

// runInstall executes install.sh once with the supplied env additions
// (key=value strings, appended to a sanitised PATH-augmented env).
func runInstall(t *testing.T, paths testPaths, extraEnv []string) installRun {
	t.Helper()
	cmd := exec.Command(paths.installSh)
	cmd.Env = append(os.Environ(), extraEnv...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	code := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else {
			t.Fatalf("install.sh: unexpected exec error: %v\nstderr:\n%s", err, stderr.String())
		}
	}
	return installRun{stdout: stdout.Bytes(), stderr: stderr.Bytes(), exitCode: code}
}

// snapshotTree returns a deterministic string of the filesystem state
// under root, listing every entry's relative path, mode, and content
// hash. The output is sorted so two snapshots of identical filesystems
// compare byte-for-byte.
func snapshotTree(t *testing.T, root string) string {
	t.Helper()
	var lines []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		if rel == "." {
			return nil
		}
		info, infoErr := d.Info()
		if infoErr != nil {
			return infoErr
		}
		mode := info.Mode()
		switch {
		case mode.IsDir():
			lines = append(lines, fmt.Sprintf("d %s %04o", rel, mode.Perm()))
		case mode&os.ModeSymlink != 0:
			target, _ := os.Readlink(path)
			lines = append(lines, fmt.Sprintf("l %s -> %s", rel, target))
		default:
			data, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			sum := sha256.Sum256(data)
			lines = append(lines, fmt.Sprintf("f %s %04o %x", rel, mode.Perm(), sum))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("snapshotTree: %v", err)
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

// stageInstallEnv prepares a t.TempDir() layout for an install.sh run:
//
//	${tmp}/install-root/        ← HUSH_INSTALL_ROOT (already exists)
//	${tmp}/stub-bin/tmutil      ← copy of tmutil_stub.sh (executable)
//	${tmp}/tmutil.log           ← created by stub on first invocation
//
// Returns the install root, log path, and the prepared env additions.
func stageInstallEnv(t *testing.T, paths testPaths, withTmutilStub bool) (installRoot, logPath string, env []string) {
	t.Helper()
	tmp := t.TempDir()
	installRoot = filepath.Join(tmp, "install-root")
	if err := os.MkdirAll(installRoot, 0o755); err != nil {
		t.Fatalf("mkdir install-root: %v", err)
	}
	logPath = filepath.Join(tmp, "tmutil.log")

	pathPrefix := ""
	if withTmutilStub {
		stubDir := filepath.Join(tmp, "stub-bin")
		if err := os.MkdirAll(stubDir, 0o755); err != nil {
			t.Fatalf("mkdir stub-bin: %v", err)
		}
		stubData, err := os.ReadFile(paths.tmutilStub)
		if err != nil {
			t.Fatalf("read tmutil stub: %v", err)
		}
		stubPath := filepath.Join(stubDir, "tmutil")
		if err := os.WriteFile(stubPath, stubData, 0o755); err != nil {
			t.Fatalf("write tmutil stub: %v", err)
		}
		pathPrefix = stubDir + string(os.PathListSeparator)
	}

	env = []string{
		"PATH=" + pathPrefix + os.Getenv("PATH"),
		"HUSH_INSTALL_ROOT=" + installRoot,
		"HUSH_SOURCE_BIN=" + paths.fakeHush,
		"TMUTIL_LOG=" + logPath,
	}
	return installRoot, logPath, env
}

// resolvedStateDir returns the absolute path install.sh writes the
// vault state directory to, given the OS-specific default.
func resolvedStateDir(installRoot string) string {
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(installRoot, "usr", "local", "var", "hush")
	default:
		return filepath.Join(installRoot, "var", "lib", "hush")
	}
}

// resolvedBinForACL returns the operator-facing binary path used in
// the banner's `-T` arg. It deliberately omits HUSH_INSTALL_ROOT — the
// banner shows the real install path the Keychain ACL must reference.
func resolvedBinForACL() string {
	return "/usr/local/bin/hush"
}

func TestDeploy_InstallIdempotent(t *testing.T) {
	paths := resolveTestPaths(t)
	installRoot, logPath, env := stageInstallEnv(t, paths, true)

	first := runInstall(t, paths, env)
	if first.exitCode != 0 {
		t.Fatalf("first run exit %d; stderr:\n%s", first.exitCode, first.stderr)
	}
	snap1 := snapshotTree(t, installRoot)

	second := runInstall(t, paths, env)
	if second.exitCode != 0 {
		t.Fatalf("second run exit %d; stderr:\n%s", second.exitCode, second.stderr)
	}
	snap2 := snapshotTree(t, installRoot)

	if !bytes.Equal(first.stdout, second.stdout) {
		t.Errorf("banner stdout drifted across runs\nrun1:\n%s\nrun2:\n%s", first.stdout, second.stdout)
	}
	if snap1 != snap2 {
		t.Errorf("filesystem snapshot drifted across runs\nrun1:\n%s\nrun2:\n%s", snap1, snap2)
	}

	stateDir := resolvedStateDir(installRoot)
	if runtime.GOOS == "darwin" {
		assertTmutilLogExactlyOnce(t, logPath, stateDir)
		assertBannerKeychainACL(t, first.stdout)
	}
}

// assertTmutilLogExactlyOnce reads logPath and asserts exactly one
// `addexclusion <stateDir>` line was recorded across all install.sh
// runs.
func assertTmutilLogExactlyOnce(t *testing.T, logPath, stateDir string) {
	t.Helper()
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read tmutil log: %v", err)
	}
	want := "addexclusion " + stateDir
	matches := 0
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == want {
			matches++
		}
	}
	if matches != 1 {
		t.Errorf("expected exactly one `%s` line in tmutil log, got %d\nlog content:\n%s",
			want, matches, string(data))
	}
}

// assertBannerKeychainACL enforces the banner sub-contract on
// captured install.sh stdout: the resolved binary path appears exactly
// once as the `-T` arg, and the banner contains neither a wildcard `-T`
// nor an `-A` token (no allow-all-apps ACL).
func assertBannerKeychainACL(t *testing.T, stdout []byte) {
	t.Helper()
	bin := resolvedBinForACL()
	want := `-T "` + bin + `"`
	occurrences := bytes.Count(stdout, []byte(want))
	if occurrences != 1 {
		t.Errorf("expected exactly one `%s` in banner, got %d\nbanner:\n%s",
			want, occurrences, stdout)
	}
	if bytes.Contains(stdout, []byte(`-T "*"`)) || bytes.Contains(stdout, []byte(`-T '*'`)) {
		t.Errorf("wildcard -T ACL found in banner\nbanner:\n%s", stdout)
	}
	// `-A` would grant allow-all-apps access. Match whole-word so we
	// don't trip on hyphenated text inside the prose body.
	scanner := bufio.NewScanner(bytes.NewReader(stdout))
	for scanner.Scan() {
		for _, tok := range strings.Fields(scanner.Text()) {
			if tok == "-A" {
				t.Errorf("allow-all-apps -A token found in banner\nbanner:\n%s", stdout)
				return
			}
		}
	}
}

func TestDeploy_InstallRefusesUnsupportedOS(t *testing.T) {
	paths := resolveTestPaths(t)
	_, _, env := stageInstallEnv(t, paths, false)
	env = append(env, "HUSH_FORCE_OS=plan9")
	res := runInstall(t, paths, env)
	if res.exitCode != 2 {
		t.Errorf("expected exit 2 on unsupported OS, got %d\nstderr:\n%s", res.exitCode, res.stderr)
	}
	if !strings.Contains(string(res.stderr), "install.sh:") {
		t.Errorf("stderr does not follow `install.sh: <stage>: <reason>` format:\n%s", res.stderr)
	}
}

func TestDeploy_InstallRefusesMissingBinary(t *testing.T) {
	paths := resolveTestPaths(t)
	_, _, env := stageInstallEnv(t, paths, true)
	// Override HUSH_SOURCE_BIN with a path that doesn't exist.
	filtered := make([]string, 0, len(env))
	for _, kv := range env {
		if !strings.HasPrefix(kv, "HUSH_SOURCE_BIN=") {
			filtered = append(filtered, kv)
		}
	}
	filtered = append(filtered, "HUSH_SOURCE_BIN=/nonexistent/hush-binary-that-must-not-exist")
	res := runInstall(t, paths, filtered)
	if res.exitCode != 2 {
		t.Errorf("expected exit 2 on missing binary, got %d\nstderr:\n%s", res.exitCode, res.stderr)
	}
	if !strings.Contains(string(res.stderr), "install.sh:") {
		t.Errorf("stderr does not follow `install.sh: <stage>: <reason>` format:\n%s", res.stderr)
	}
}

func TestDeploy_InstallRefusesMissingTmutil(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("tmutil hard-fail only applies on darwin")
	}
	paths := resolveTestPaths(t)
	_, _, env := stageInstallEnv(t, paths, false)
	// Build a minimal PATH that excludes the directory containing
	// `tmutil` (typically /usr/bin/tmutil on macOS). We stage symlinks
	// to every other tool install.sh needs inside an isolated tempdir
	// and point PATH there.
	mockBin := filepath.Join(t.TempDir(), "mock-bin")
	if err := os.MkdirAll(mockBin, 0o755); err != nil {
		t.Fatalf("mkdir mock-bin: %v", err)
	}
	required := []string{
		"bash", "sed", "mkdir", "chmod", "install", "uname",
		"awk", "grep", "cat", "dirname", "rm", "printf", "ln",
		"sh", "env", "true", "false", "head", "tail", "sort",
	}
	for _, tool := range required {
		real, err := exec.LookPath(tool)
		if err != nil {
			t.Skipf("required host tool %q missing from PATH: %v", tool, err)
		}
		if err := os.Symlink(real, filepath.Join(mockBin, tool)); err != nil {
			t.Fatalf("symlink %s: %v", tool, err)
		}
	}
	// Strip the existing PATH= entry and replace it with the mock dir.
	filtered := make([]string, 0, len(env))
	for _, kv := range env {
		if !strings.HasPrefix(kv, "PATH=") {
			filtered = append(filtered, kv)
		}
	}
	filtered = append(filtered, "PATH="+mockBin)

	res := runInstall(t, paths, filtered)
	if res.exitCode != 4 {
		t.Errorf("hard-fail: expected exit 4 when tmutil missing, got %d\nstderr:\n%s",
			res.exitCode, res.stderr)
	}
	if !strings.Contains(string(res.stderr), "tmutil") {
		t.Errorf("hard-fail: stderr should name `tmutil`:\n%s", res.stderr)
	}
}

func TestDeploy_InstallBannerByteIdentical(t *testing.T) {
	paths := resolveTestPaths(t)
	_, _, env := stageInstallEnv(t, paths, true)

	first := runInstall(t, paths, env)
	if first.exitCode != 0 {
		t.Fatalf("first run exit %d; stderr:\n%s", first.exitCode, first.stderr)
	}
	second := runInstall(t, paths, env)
	if second.exitCode != 0 {
		t.Fatalf("second run exit %d; stderr:\n%s", second.exitCode, second.stderr)
	}
	if !bytes.Equal(first.stdout, second.stdout) {
		t.Errorf("banner regression: stdout differs between runs\nrun1:\n%s\nrun2:\n%s",
			first.stdout, second.stdout)
	}
}
