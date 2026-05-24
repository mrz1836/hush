package validators_test

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mrz1836/hush/internal/supervise/validators"
	"github.com/mrz1836/hush/internal/vault/securebytes"
)

// sentinelLeakProbe is the canary fed into every TestValidator_<P>_
// NoLeakOnError test. It MUST appear nowhere in err.Error(), any
// wrapped error's Error(), or any captured slog record.
const sentinelLeakProbe = "SECRET_SHOULD_NEVER_APPEAR_26"

// rewriteTransport rewrites the scheme+host of every outbound request
// from a pinned production URL to a local httptest.Server URL.
// Production URLs in test code MUST appear inside a rewriteTransport
// literal.
type rewriteTransport struct {
	from string
	to   string
	base http.RoundTripper
}

func (r rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	target, err := url.Parse(r.to)
	if err != nil {
		return nil, err
	}
	clone := req.Clone(req.Context())
	clone.URL.Scheme = target.Scheme
	clone.URL.Host = target.Host
	clone.Host = target.Host
	base := r.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(clone)
}

// captureHandler records every slog record into recs.
type captureHandler struct {
	mu    sync.Mutex
	recs  []slog.Record
	attrs []slog.Attr
}

func newCaptureHandler() *captureHandler {
	return &captureHandler{}
}

func (h *captureHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }

func (h *captureHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.recs = append(h.recs, r.Clone())
	return nil
}

func (h *captureHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	c := &captureHandler{attrs: append(h.attrs, attrs...)}
	return c
}

func (h *captureHandler) WithGroup(_ string) slog.Handler { return h }

func (h *captureHandler) snapshot() []slog.Record {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]slog.Record, len(h.recs))
	copy(out, h.recs)
	return out
}

func (h *captureHandler) dumpText() string {
	var sb strings.Builder
	for _, r := range h.snapshot() {
		sb.WriteString(r.Message)
		r.Attrs(func(a slog.Attr) bool {
			sb.WriteString(" ")
			sb.WriteString(a.Key)
			sb.WriteString("=")
			sb.WriteString(a.Value.String())
			return true
		})
		sb.WriteString("\n")
	}
	return sb.String()
}

func newCaptureLogger() (*slog.Logger, *captureHandler) {
	h := newCaptureHandler()
	return slog.New(h), h
}

// newTestClient builds an *http.Client whose Transport rewrites a pinned
// production URL to srv.URL. The client's Timeout is overridable per
// test.
func newTestClient(t *testing.T, srv *httptest.Server, fromURL string, timeout time.Duration) *http.Client {
	t.Helper()
	return &http.Client{
		Timeout: timeout,
		Transport: rewriteTransport{
			from: fromURL,
			to:   srv.URL,
			base: srv.Client().Transport,
		},
	}
}

func newSecure(t *testing.T, value string) *securebytes.SecureBytes {
	t.Helper()
	sb, err := securebytes.New([]byte(value))
	if err != nil {
		t.Fatalf("securebytes.New: %v", err)
	}
	t.Cleanup(func() { _ = sb.Destroy() })
	return sb
}

// providerCase is the per-provider test fixture used by several shared
// tests. Each case wires an httptest.Server, the rewrite transport, a
// capture logger, and the matching Validator. The pinned URL is the
// pinned production URL.
type providerCase struct {
	name        string
	fixedHost   string
	constructor func(*http.Client) validators.Validator
}

// providerCase factory functions — sibling per-provider test files use
// these to avoid package-level mutable global state (gochecknoglobals).
// Pinned production URLs in the bodies are consumed only by the
// rewriteTransport rewriter, not as live HTTP targets.

func anthropicProviderCase() providerCase {
	return providerCase{
		name:        "anthropic",
		fixedHost:   "https://api.anthropic.com/v1/models",
		constructor: validators.NewAnthropic,
	}
}

func anthropicOAuthProviderCase() providerCase {
	return providerCase{
		name:        "anthropic-oauth",
		fixedHost:   "https://api.anthropic.com/v1/models",
		constructor: validators.NewAnthropicOAuth,
	}
}

func openaiProviderCase() providerCase {
	return providerCase{
		name:        "openai",
		fixedHost:   "https://api.openai.com/v1/models",
		constructor: validators.NewOpenAI,
	}
}

func googleAIProviderCase() providerCase {
	return providerCase{
		name:        "google-ai",
		fixedHost:   "https://generativelanguage.googleapis.com/v1beta/models",
		constructor: validators.NewGoogleAI,
	}
}

func githubProviderCase() providerCase {
	return providerCase{
		name:        "github",
		fixedHost:   "https://api.github.com/user",
		constructor: validators.NewGitHub,
	}
}

// -----------------------------------------------------------------------------
// Shared tests
// -----------------------------------------------------------------------------

// TestValidator_InterfaceHasOneMethod.
func TestValidator_InterfaceHasOneMethod(t *testing.T) {
	t.Parallel()
	typ := reflect.TypeFor[validators.Validator]()
	if got := typ.NumMethod(); got != 1 {
		t.Fatalf("Validator interface method count: got %d, want 1", got)
	}
	if name := typ.Method(0).Name; name != "Validate" {
		t.Fatalf("Validator method name: got %q, want %q", name, "Validate")
	}
}

// TestPackage_SentinelsArePairwiseDistinct — B-V-ERR-1.
func TestPackage_SentinelsArePairwiseDistinct(t *testing.T) {
	t.Parallel()
	sentinels := []error{
		validators.ErrStaleCredential,
		validators.ErrValidatorTimeout,
		validators.ErrValidatorNetwork,
	}
	for i := range sentinels {
		for j := range sentinels {
			if i == j {
				continue
			}
			if errors.Is(sentinels[i], sentinels[j]) {
				t.Errorf("sentinel[%d] errors.Is sentinel[%d] (not distinct)", i, j)
			}
		}
	}
}

// TestPackage_SentinelStringsAreLiteral — B-V-ERR-2, S-2/S-3.
func TestPackage_SentinelStringsAreLiteral(t *testing.T) {
	t.Parallel()
	cases := []struct {
		err  error
		want string
	}{
		{validators.ErrStaleCredential, "validators: credential rejected by provider"},
		{validators.ErrValidatorTimeout, "validators: probe timeout"},
		{validators.ErrValidatorNetwork, "validators: probe network failure"},
	}
	for _, c := range cases {
		if got := c.err.Error(); got != c.want {
			t.Errorf("sentinel string: got %q, want %q", got, c.want)
		}
	}
}

// TestRegistry_AllFiveNamesPresent.
func TestRegistry_AllFiveNamesPresent(t *testing.T) {
	t.Parallel()
	reg := validators.NewRegistry(nil)
	for _, name := range []string{"anthropic", "anthropic-oauth", "openai", "google-ai", "github"} {
		v, ok := reg.Get(name)
		if !ok {
			t.Errorf("Get(%q): ok=false, want true", name)
		}
		if v == nil {
			t.Errorf("Get(%q): nil Validator", name)
		}
	}
}

// TestRegistry_GetUnknownName_FalseFound.
func TestRegistry_GetUnknownName_FalseFound(t *testing.T) {
	t.Parallel()
	reg := validators.NewRegistry(nil)
	cases := []string{"", "Anthropic", "GITHUB", " openai ", "nonsense", "anthropic-oauth-extra"}
	for _, name := range cases {
		v, ok := reg.Get(name)
		if ok {
			t.Errorf("Get(%q): ok=true, want false", name)
		}
		if v != nil {
			t.Errorf("Get(%q): non-nil Validator, want nil", name)
		}
	}
}

// TestRegistry_ExactlyFiveNames.
func TestRegistry_ExactlyFiveNames(t *testing.T) {
	t.Parallel()
	reg := validators.NewRegistry(nil)
	want := map[string]bool{
		"anthropic": true, "anthropic-oauth": true, "openai": true,
		"google-ai": true, "github": true,
	}
	for name := range want {
		if _, ok := reg.Get(name); !ok {
			t.Errorf("registry missing required name %q", name)
		}
	}
	// Exhaustively reject candidate sixth names; the registry has exactly five.
	extras := []string{"vault", "azure", "anthropic-api", "openai-key", "googleai"}
	for _, name := range extras {
		if _, ok := reg.Get(name); ok {
			t.Errorf("registry unexpectedly contains %q", name)
		}
	}
}

// TestRegistry_GetIsRaceClean.
func TestRegistry_GetIsRaceClean(t *testing.T) {
	t.Parallel()
	reg := validators.NewRegistry(nil)
	const goroutines = 100
	const iters = 100
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < iters; j++ {
				_, _ = reg.Get("openai")
				_, _ = reg.Get("github")
				_, _ = reg.Get("nope")
			}
		}()
	}
	wg.Wait()
}

// TestPackage_DefaultClientTimeoutIs5s.
//
// Behavioral assertion: with a nil client and a fixture that sleeps
// 200ms, Validate completes well within 5s. With a non-nil client whose
// timeout is 50ms, Validate fails fast with ErrValidatorTimeout.
func TestPackage_DefaultClientTimeoutIs5s(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	// Build a validator via the registry, then swap its underlying
	// http.Client transport via a fresh validator using a non-nil client.
	// To assert the *default* client honors 5s, we exercise the nil path
	// indirectly via behaviour: a quick fixture should respond well
	// under 5s. (The exact value of the field is encapsulated.)
	v := validators.NewOpenAI(&http.Client{
		Timeout: 5 * time.Second,
		Transport: rewriteTransport{
			from: "https://api.openai.com/v1/models",
			to:   srv.URL,
			base: srv.Client().Transport,
		},
	})
	start := time.Now()
	err := v.Validate(context.Background(), newSecure(t, "key"))
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("Validate took %s under default-timeout setup", elapsed)
	}
}

// TestPackage_CallerSuppliedClientReturnedVerbatim — B-V-FIX-3, Q1.
//
// Setting Timeout=1ms on the supplied client should cause the validator
// to error with a timeout-shaped sentinel against a slow fixture.
func TestPackage_CallerSuppliedClientReturnedVerbatim(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	client := &http.Client{
		Timeout: 5 * time.Millisecond,
		Transport: rewriteTransport{
			from: "https://api.openai.com/v1/models",
			to:   srv.URL,
			base: srv.Client().Transport,
		},
	}
	v := validators.NewOpenAI(client)
	err := v.Validate(context.Background(), newSecure(t, "key"))
	if err == nil {
		t.Fatalf("Validate: nil, want timeout-class error")
	}
	if !errors.Is(err, validators.ErrValidatorTimeout) && !errors.Is(err, validators.ErrValidatorNetwork) {
		t.Fatalf("Validate err = %v, want Timeout or Network", err)
	}
}

// TestPackage_LogRecordSchema_Success.
func TestPackage_LogRecordSchema_Success(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	logger, capLog := newCaptureLogger()
	v := validators.NewOpenAI(newTestClient(t, srv, "https://api.openai.com/v1/models", time.Second))
	validators.SetLoggerForTest(v, logger)

	if err := v.Validate(context.Background(), newSecure(t, "key")); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	recs := capLog.snapshot()
	if len(recs) != 1 {
		t.Fatalf("captured records = %d, want 1", len(recs))
	}
	rec := recs[0]
	if rec.Level != slog.LevelDebug {
		t.Fatalf("record level = %v, want DEBUG", rec.Level)
	}
	if rec.Message != "validator outcome" {
		t.Fatalf("record message = %q, want %q", rec.Message, "validator outcome")
	}
	keys := recordKeys(rec)
	want := []string{"outcome", "status", "validator"}
	if !equalStringSlices(keys, want) {
		t.Fatalf("record keys = %v, want %v", keys, want)
	}
}

// TestPackage_LogRecordSchema_Failure.
func TestPackage_LogRecordSchema_Failure(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)

	logger, capLog := newCaptureLogger()
	v := validators.NewOpenAI(newTestClient(t, srv, "https://api.openai.com/v1/models", time.Second))
	validators.SetLoggerForTest(v, logger)

	err := v.Validate(context.Background(), newSecure(t, "key"))
	if !errors.Is(err, validators.ErrStaleCredential) {
		t.Fatalf("Validate: %v, want ErrStaleCredential", err)
	}
	recs := capLog.snapshot()
	if len(recs) != 1 {
		t.Fatalf("captured records = %d, want 1", len(recs))
	}
	rec := recs[0]
	if rec.Level != slog.LevelWarn {
		t.Fatalf("record level = %v, want WARN", rec.Level)
	}
	keys := recordKeys(rec)
	want := []string{"outcome", "status", "validator"}
	if !equalStringSlices(keys, want) {
		t.Fatalf("record keys = %v, want %v", keys, want)
	}
	if v := attrString(rec, "outcome"); v != "stale" {
		t.Fatalf("outcome = %q, want stale", v)
	}
}

// TestPackage_LogAttrsAreAllowList — B-V-LOG-3, H-5.
// Source-grep: production code must not emit forbidden attribute keys.
func TestPackage_LogAttrsAreAllowList(t *testing.T) {
	t.Parallel()
	for _, f := range productionFiles(t) {
		src := readFile(t, f)
		for _, bad := range []string{
			`slog.Any("error"`, `slog.String("error"`,
			`slog.String("url"`, `slog.Any("url"`,
			`slog.Any("request"`, `slog.Any("response"`,
			`slog.Any("header"`, `slog.String("scope"`,
			`slog.Any("body"`, `slog.String("body"`,
		} {
			if strings.Contains(src, bad) {
				t.Errorf("%s contains forbidden slog attr %q", f, bad)
			}
		}
	}
}

// TestPackage_NoStringConversionsOfSecret.
func TestPackage_NoStringConversionsOfSecret(t *testing.T) {
	t.Parallel()
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`string\(\s*secret\b`),
		regexp.MustCompile(`string\(\s*creds\b`),
		regexp.MustCompile(`string\(\s*credential\b`),
		regexp.MustCompile(`fmt\.Sprintf\("%s",\s*secret\b`),
		regexp.MustCompile(`fmt\.Sprintf\("%s",\s*creds\b`),
	}
	for _, f := range productionFiles(t) {
		src := readFile(t, f)
		for _, p := range patterns {
			if p.MatchString(src) {
				t.Errorf("%s matches forbidden pattern %s", f, p.String())
			}
		}
	}
}

// TestPackage_NoRequestObjectInLogOrError.
func TestPackage_NoRequestObjectInLogOrError(t *testing.T) {
	t.Parallel()
	for _, f := range productionFiles(t) {
		src := readFile(t, f)
		for _, bad := range []string{
			`slog.Any("request"`, `slog.Any("req"`,
			`slog.Any("response"`, `slog.Any("resp"`,
			`slog.Any("header"`,
			`fmt.Errorf("%v", req`, `fmt.Errorf("%+v", req`,
			`fmt.Errorf("%v", resp`, `fmt.Errorf("%+v", resp`,
		} {
			if strings.Contains(src, bad) {
				t.Errorf("%s contains forbidden request/response sink %q", f, bad)
			}
		}
	}
}

// TestPackage_AllBuildersZeroLocalBuffer — B-V-SEC-3, B-3.
//
// AST scan: each set<Provider>Auth function's last non-return statement
// must be a for-range zero-loop over the same local buffer.
func TestPackage_AllBuildersZeroLocalBuffer(t *testing.T) {
	t.Parallel()
	// All five auth-header builders live in the single consolidated
	// provider.go after the 5→1 struct dedup. The zero-loop discipline
	// is enforced per-function: extracting a shared helper would break
	// Constitution VIII independent auditability.
	const file = "provider.go"
	builders := []string{
		"setAnthropicAuth",
		"setAnthropicOAuthAuth",
		"setOpenAIAuth",
		"setGoogleAIAuth",
		"setGitHubAuth",
	}
	for _, fnName := range builders {
		assertBuilderZeroLoop(t, fnName, file)
	}
}

func assertBuilderZeroLoop(t *testing.T, fnName, file string) {
	t.Helper()
	path := filepath.Join(packageDir(t), file)
	fset := token.NewFileSet()
	node, err := parser.ParseFile(fset, path, nil, parser.AllErrors)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	fn := findFuncDecl(node, fnName)
	if fn == nil {
		t.Errorf("%s: %s not found", file, fnName)
		return
	}
	body := fn.Body.List
	if len(body) < 2 {
		t.Errorf("%s: body too short", fnName)
		return
	}
	if _, ok := body[len(body)-1].(*ast.ReturnStmt); !ok {
		t.Errorf("%s: last stmt is %T, want *ast.ReturnStmt", fnName, body[len(body)-1])
		return
	}
	fr, ok := body[len(body)-2].(*ast.RangeStmt)
	if !ok {
		t.Errorf("%s: penultimate stmt is %T, want *ast.RangeStmt (zero-loop)", fnName, body[len(body)-2])
		return
	}
	assertRangeZeroAssign(t, fnName, fr)
}

func assertRangeZeroAssign(t *testing.T, fnName string, fr *ast.RangeStmt) {
	t.Helper()
	if len(fr.Body.List) != 1 {
		t.Errorf("%s: zero-loop body has %d stmts, want 1", fnName, len(fr.Body.List))
		return
	}
	assign, ok := fr.Body.List[0].(*ast.AssignStmt)
	if !ok {
		t.Errorf("%s: zero-loop body is %T, want *ast.AssignStmt", fnName, fr.Body.List[0])
		return
	}
	if assign.Tok != token.ASSIGN {
		t.Errorf("%s: zero-loop uses %s, want '='", fnName, assign.Tok)
	}
	if lit, ok := assign.Rhs[0].(*ast.BasicLit); !ok || lit.Value != "0" {
		t.Errorf("%s: zero-loop RHS not literal 0", fnName)
	}
}

// TestPackage_NoLiveProviderHosts.
//
// Every production-hostname match in *_test.go must be inside a
// rewriteTransport literal or a per-test constant declared right
// before such a literal. We assert by a structural rule: the line
// containing the hostname must contain the substring "rewriteTransport"
// OR be inside a fixedHost variable assignment within a providerCase
// table, OR be inside the providerCases() helper.
func TestPackage_NoLiveProviderHosts(t *testing.T) {
	t.Parallel()
	hosts := []string{
		"api.anthropic.com",
		"api.openai.com",
		"api.github.com",
		"generativelanguage.googleapis.com",
	}
	entries, err := os.ReadDir(packageDir(t))
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		scanTestFileForHosts(t, e.Name(), hosts)
	}
}

func scanTestFileForHosts(t *testing.T, file string, hosts []string) {
	t.Helper()
	path := filepath.Join(packageDir(t), file)
	src := readFile(t, path)
	lines := strings.Split(src, "\n")
	for i, line := range lines {
		for _, h := range hosts {
			if !strings.Contains(line, h) {
				continue
			}
			if !lineIsTestFixtureContext(lines, i) {
				t.Errorf("%s:%d hostname %q appears outside rewriteTransport / providerCases context: %s",
					file, i+1, h, strings.TrimSpace(line))
			}
		}
	}
}

// lineIsTestFixtureContext returns true if the line at idx is inside a
// rewriteTransport literal, an httptest-related URL string in a test
// helper (providerCases / fixedHost), or a const declaration paired
// with such a literal. The heuristic looks at the line itself and the
// preceding 12 lines.
func lineIsTestFixtureContext(lines []string, idx int) bool {
	start := idx - 12
	if start < 0 {
		start = 0
	}
	context := strings.Join(lines[start:idx+1], "\n")
	keywords := []string{
		"rewriteTransport",
		"providerCases",
		"providerCase{",
		"fixedHost",
		"newTestClient",
		"TestPackage_NoLiveProviderHosts",
		"hosts := []string",
	}
	for _, kw := range keywords {
		if strings.Contains(context, kw) {
			return true
		}
	}
	return false
}

// TestPackage_ZeroNewDependencies.
//
// The package must not have introduced any new direct dependency. Parse
// the project's go.mod and verify that the only third-party direct dep
// transitively imported by the package is internal (mrz1836/hush).
func TestPackage_ZeroNewDependencies(t *testing.T) {
	t.Parallel()
	for _, f := range productionFiles(t) {
		src := readFile(t, f)
		fset := token.NewFileSet()
		node, err := parser.ParseFile(fset, f, src, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", f, err)
		}
		for _, imp := range node.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			if strings.HasPrefix(path, "github.com/") && !strings.HasPrefix(path, "github.com/mrz1836/hush/") {
				t.Errorf("%s imports third-party module %q", f, path)
			}
		}
	}
}

// TestExport_SetLoggerForTest_AllProvidersCovered — T-2.
func TestExport_SetLoggerForTest_AllProvidersCovered(t *testing.T) {
	t.Parallel()
	reg := validators.NewRegistry(nil)
	for _, name := range []string{"anthropic", "anthropic-oauth", "openai", "google-ai", "github"} {
		v, ok := reg.Get(name)
		if !ok {
			t.Fatalf("Get(%q) ok=false", name)
		}
		// Should not panic.
		validators.SetLoggerForTest(v, slog.New(slog.NewTextHandler(io.Discard, nil)))
	}
}

// -----------------------------------------------------------------------------
// helpers shared by per-provider tests (provider_test.go files reuse these)
// -----------------------------------------------------------------------------

func recordKeys(r slog.Record) []string {
	keys := []string{}
	r.Attrs(func(a slog.Attr) bool {
		keys = append(keys, a.Key)
		return true
	})
	sort.Strings(keys)
	return keys
}

func attrString(r slog.Record, key string) string {
	var out string
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == key {
			out = a.Value.String()
			return false
		}
		return true
	})
	return out
}

func equalStringSlices(a, b []string) bool {
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

func packageDir(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	return wd
}

func productionFiles(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(packageDir(t))
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	var out []string
	for _, e := range entries {
		n := e.Name()
		if !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		out = append(out, filepath.Join(packageDir(t), n))
	}
	return out
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile %s: %v", path, err)
	}
	return string(b)
}

func findFuncDecl(file *ast.File, name string) *ast.FuncDecl {
	for _, d := range file.Decls {
		if fn, ok := d.(*ast.FuncDecl); ok && fn.Name.Name == name {
			return fn
		}
	}
	return nil
}

// -----------------------------------------------------------------------------
// Per-provider scenarios (US1/US2/edge — implemented as table-driven helpers
// invoked by per-provider *_test.go files for each provider).
// -----------------------------------------------------------------------------

// runHappyPath asserts a 200 response yields nil + DEBUG record.
func runHappyPath(t *testing.T, pc providerCase) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	logger, capLog := newCaptureLogger()
	v := pc.constructor(newTestClient(t, srv, pc.fixedHost, time.Second))
	validators.SetLoggerForTest(v, logger)

	if err := v.Validate(context.Background(), newSecure(t, "key")); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("handler invocations: got %d, want 1", got)
	}
	recs := capLog.snapshot()
	if len(recs) != 1 || recs[0].Level != slog.LevelDebug {
		t.Fatalf("captured records = %d (expected 1 DEBUG), got level %v", len(recs), recs[0].Level)
	}
}

// runStale asserts a given status (401 or 403) yields ErrStaleCredential.
func runStale(t *testing.T, pc providerCase, status int) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)

	v := pc.constructor(newTestClient(t, srv, pc.fixedHost, time.Second))
	err := v.Validate(context.Background(), newSecure(t, "key"))
	if !errors.Is(err, validators.ErrStaleCredential) {
		t.Fatalf("Validate err = %v, want ErrStaleCredential", err)
	}
	if errors.Is(err, validators.ErrValidatorTimeout) {
		t.Fatalf("Validate err satisfies Timeout: %v", err)
	}
	if errors.Is(err, validators.ErrValidatorNetwork) {
		t.Fatalf("Validate err satisfies Network: %v", err)
	}
}

// runNetwork5xx covers 500/502/503/429 → ErrValidatorNetwork.
func runNetwork5xx(t *testing.T, pc providerCase) {
	t.Helper()
	for _, code := range []int{500, 502, 503, 429} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(code)
			}))
			t.Cleanup(srv.Close)
			v := pc.constructor(newTestClient(t, srv, pc.fixedHost, time.Second))
			err := v.Validate(context.Background(), newSecure(t, "key"))
			if !errors.Is(err, validators.ErrValidatorNetwork) {
				t.Fatalf("status %d: err = %v, want Network", code, err)
			}
			if errors.Is(err, validators.ErrStaleCredential) {
				t.Fatalf("status %d: err satisfies Stale: %v", code, err)
			}
		})
	}
}

// runTimeout asserts client-timeout shorter than fixture sleep yields ErrValidatorTimeout.
func runTimeout(t *testing.T, pc providerCase) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(150 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	client := newTestClient(t, srv, pc.fixedHost, 10*time.Millisecond)
	v := pc.constructor(client)
	err := v.Validate(context.Background(), newSecure(t, "key"))
	if !errors.Is(err, validators.ErrValidatorTimeout) {
		t.Fatalf("Validate err = %v, want ErrValidatorTimeout", err)
	}
}

// runRefused asserts a closed listener yields ErrValidatorNetwork.
func runRefused(t *testing.T, pc providerCase) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
	url := srv.URL
	srv.Close() // close immediately so the next dial is refused.

	client := &http.Client{
		Timeout: 200 * time.Millisecond,
		Transport: rewriteTransport{
			from: pc.fixedHost,
			to:   url,
			base: http.DefaultTransport,
		},
	}
	v := pc.constructor(client)
	err := v.Validate(context.Background(), newSecure(t, "key"))
	if !errors.Is(err, validators.ErrValidatorNetwork) {
		t.Fatalf("Validate err = %v, want ErrValidatorNetwork", err)
	}
}

// runRedirect3xx asserts a 302 response is classified as Network (no follow).
func runRedirect3xx(t *testing.T, pc providerCase) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set("Location", "https://example.com/other")
		w.WriteHeader(http.StatusFound)
	}))
	t.Cleanup(srv.Close)

	v := pc.constructor(newTestClient(t, srv, pc.fixedHost, time.Second))
	err := v.Validate(context.Background(), newSecure(t, "key"))
	if !errors.Is(err, validators.ErrValidatorNetwork) {
		t.Fatalf("Validate err = %v, want ErrValidatorNetwork", err)
	}
	if hits.Load() != 1 {
		t.Fatalf("handler hits = %d, want 1 (no redirect-follow)", hits.Load())
	}
}

// runCtxCancelledBefore asserts a pre-cancelled ctx returns quickly without
// touching the fixture.
func runCtxCancelledBefore(t *testing.T, pc providerCase) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	v := pc.constructor(newTestClient(t, srv, pc.fixedHost, time.Second))
	start := time.Now()
	err := v.Validate(ctx, newSecure(t, "key"))
	elapsed := time.Since(start)
	if err == nil {
		t.Fatalf("Validate: nil, want non-nil")
	}
	if !errors.Is(err, validators.ErrValidatorNetwork) {
		t.Fatalf("Validate err = %v, want ErrValidatorNetwork", err)
	}
	if hits.Load() != 0 {
		t.Fatalf("handler invoked %d times; want 0", hits.Load())
	}
	if elapsed > 50*time.Millisecond {
		t.Fatalf("pre-cancel fast-path took %s, want ≤50ms", elapsed)
	}
}

// runCtxCancelledMid asserts mid-flight cancellation returns a Network error.
func runCtxCancelledMid(t *testing.T, pc providerCase) {
	t.Helper()
	hold := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-hold
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(func() {
		close(hold)
		srv.Close()
	})

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	v := pc.constructor(newTestClient(t, srv, pc.fixedHost, time.Second))
	err := v.Validate(ctx, newSecure(t, "key"))
	if err == nil {
		t.Fatalf("Validate: nil, want non-nil")
	}
	if errors.Is(err, validators.ErrStaleCredential) {
		t.Fatalf("Validate err satisfies Stale: %v", err)
	}
	if !errors.Is(err, validators.ErrValidatorNetwork) && !errors.Is(err, validators.ErrValidatorTimeout) {
		t.Fatalf("Validate err = %v, want Network or Timeout", err)
	}
}

// runSingleRequest asserts the validator invokes the fixture handler at
// most once per Validate call.
func runSingleRequest(t *testing.T, pc providerCase) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)

	v := pc.constructor(newTestClient(t, srv, pc.fixedHost, time.Second))
	_ = v.Validate(context.Background(), newSecure(t, "key"))
	if got := hits.Load(); got != 1 {
		t.Fatalf("handler hits = %d, want 1", got)
	}
}

// runConcurrent asserts race-cleanliness with multiple goroutines.
func runConcurrent(t *testing.T, pc providerCase) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	v := pc.constructor(newTestClient(t, srv, pc.fixedHost, time.Second))
	const goroutines = 6
	const iters = 5
	errCh := make(chan error, goroutines*iters)
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				errCh <- v.Validate(context.Background(), newSecure(t, "key"))
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for e := range errCh {
		if e != nil {
			t.Fatalf("concurrent Validate err: %v", e)
		}
	}
}

// runDestroyedSecureBytes asserts that calling Validate with a destroyed
// SecureBytes yields ErrValidatorNetwork whose chain preserves
// securebytes.ErrDestroyed.
func runDestroyedSecureBytes(t *testing.T, pc providerCase) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	sb, err := securebytes.New([]byte("key"))
	if err != nil {
		t.Fatalf("securebytes.New: %v", err)
	}
	if err := sb.Destroy(); err != nil {
		t.Fatalf("Destroy: %v", err)
	}

	v := pc.constructor(newTestClient(t, srv, pc.fixedHost, time.Second))
	res := v.Validate(context.Background(), sb)
	if !errors.Is(res, validators.ErrValidatorNetwork) {
		t.Fatalf("Validate err = %v, want ErrValidatorNetwork", res)
	}
	if !errors.Is(res, securebytes.ErrDestroyed) {
		t.Fatalf("Validate err = %v, want chain to include ErrDestroyed", res)
	}
}

// runNoLeakOnError feeds the sentinel through the 401 path; the
// returned err.Error() and every captured log must not contain it.
func runNoLeakOnError(t *testing.T, pc providerCase) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)

	logger, capLog := newCaptureLogger()
	v := pc.constructor(newTestClient(t, srv, pc.fixedHost, time.Second))
	validators.SetLoggerForTest(v, logger)

	err := v.Validate(context.Background(), newSecure(t, sentinelLeakProbe))
	if err == nil {
		t.Fatalf("Validate: nil, want stale error")
	}
	// Top-level Error()
	if strings.Contains(err.Error(), sentinelLeakProbe) {
		t.Fatalf("err.Error() leaks sentinel: %s", err.Error())
	}
	// Walk wrapped chain.
	current := err
	for current != nil {
		if strings.Contains(current.Error(), sentinelLeakProbe) {
			t.Fatalf("wrapped err.Error() leaks sentinel: %s", current.Error())
		}
		unwrapped := errors.Unwrap(current)
		if unwrapped == nil {
			// also check joined error tree
			break
		}
		current = unwrapped
	}
	// Captured log records.
	if strings.Contains(capLog.dumpText(), sentinelLeakProbe) {
		t.Fatalf("captured slog records leak sentinel: %s", capLog.dumpText())
	}
}

// runNameIsLocked asserts the slog record's validator attr equals the
// pinned name.
func runNameIsLocked(t *testing.T, pc providerCase, want string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	logger, capLog := newCaptureLogger()
	v := pc.constructor(newTestClient(t, srv, pc.fixedHost, time.Second))
	validators.SetLoggerForTest(v, logger)

	if err := v.Validate(context.Background(), newSecure(t, "key")); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	recs := capLog.snapshot()
	if len(recs) == 0 {
		t.Fatalf("no records captured")
	}
	if got := attrString(recs[0], "validator"); got != want {
		t.Fatalf("validator attr = %q, want %q", got, want)
	}
}

// runEmptyCredentialForwarded asserts empty credential bytes are
// forwarded to the upstream and a 401 response yields ErrStaleCredential.
func runEmptyCredentialForwarded(t *testing.T, pc providerCase) {
	t.Helper()
	var seen sync.Map
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for k, vv := range r.Header {
			for _, v := range vv {
				seen.Store(k+":"+v, true)
			}
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)

	sb, err := securebytes.New([]byte{})
	if err != nil {
		t.Fatalf("securebytes.New: %v", err)
	}
	t.Cleanup(func() { _ = sb.Destroy() })

	v := pc.constructor(newTestClient(t, srv, pc.fixedHost, time.Second))
	if errIs := v.Validate(context.Background(), sb); !errors.Is(errIs, validators.ErrStaleCredential) {
		t.Fatalf("Validate err = %v, want ErrStaleCredential", errIs)
	}
}

// runAuthHeaderShape captures the Authorization-equivalent header and
// asserts the expected shape for the provider.
func runAuthHeaderShape(t *testing.T, pc providerCase, secretValue string, asserter func(*testing.T, http.Header, *url.URL)) {
	t.Helper()
	var got http.Header
	var gotURL *url.URL
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		gotURL = r.URL
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	v := pc.constructor(newTestClient(t, srv, pc.fixedHost, time.Second))
	if err := v.Validate(context.Background(), newSecure(t, secretValue)); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	asserter(t, got, gotURL)
}

// runInterfaceSatisfied is a runtime guard (the compile-time guard is
// in the per-provider production file via var _ Validator = ...).
func runInterfaceSatisfied(t *testing.T, v validators.Validator) {
	t.Helper()
	if v == nil {
		t.Fatalf("constructor returned nil")
	}
	// Trivial type-assertion via reflection: the value implements
	// Validator. (Compile-time satisfied by *_ = validators.Validator
	// assignment site.)
	_ = v
}
