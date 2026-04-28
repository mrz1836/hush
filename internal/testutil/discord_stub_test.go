package testutil

import (
	"context"
	"errors"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestApprovalRequest_LimitDescription(t *testing.T) {
	withUses := ApprovalRequest{TTL: 20 * time.Hour, MaxUses: 50}
	if got := withUses.LimitDescription(); got != "20h0m0s, 50 uses" {
		t.Errorf("LimitDescription with MaxUses: got %q, want %q", got, "20h0m0s, 50 uses")
	}

	ttlOnly := ApprovalRequest{TTL: 20 * time.Hour, MaxUses: 0}
	if got := ttlOnly.LimitDescription(); got != "20h0m0s, TTL-only" {
		t.Errorf("LimitDescription TTL-only: got %q, want %q", got, "20h0m0s, TTL-only")
	}
}

func TestDiscordStub_ApproveAll(t *testing.T) {
	stub := NewDiscordStub(t)
	stub.ApproveAll = true

	req := ApprovalRequest{RequesterHost: "host1", Scopes: []string{"s1"}, SessionType: "interactive", TTL: time.Hour, MaxUses: 5}
	d, err := stub.RequestApproval(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d != DecisionApprove {
		t.Errorf("expected DecisionApprove, got %v", d)
	}
	if calls := stub.Calls(); len(calls) != 1 {
		t.Errorf("expected 1 recorded call, got %d", len(calls))
	}
}

func TestDiscordStub_Queue(t *testing.T) {
	stub := NewDiscordStub(t)
	stub.Enqueue(DecisionApprove, DecisionDeny, DecisionApproveMute)

	ctx := context.Background()
	want := []Decision{DecisionApprove, DecisionDeny, DecisionApproveMute}
	for i, w := range want {
		d, err := stub.RequestApproval(ctx, ApprovalRequest{RequesterHost: "h"})
		if err != nil {
			t.Fatalf("call %d: unexpected error: %v", i, err)
		}
		if d != w {
			t.Errorf("call %d: got %v, want %v", i, d, w)
		}
	}
}

func TestDiscordStub_QueueThenApproveAll(t *testing.T) {
	stub := NewDiscordStub(t)
	stub.Enqueue(DecisionDeny)
	stub.ApproveAll = true

	ctx := context.Background()
	req := ApprovalRequest{RequesterHost: "h"}

	d, err := stub.RequestApproval(ctx, req)
	if err != nil || d != DecisionDeny {
		t.Errorf("call 1: got (%v, %v), want (DecisionDeny, nil)", d, err)
	}
	for i := 2; i <= 5; i++ {
		d, err = stub.RequestApproval(ctx, req)
		if err != nil || d != DecisionApprove {
			t.Errorf("call %d: got (%v, %v), want (DecisionApprove, nil)", i, d, err)
		}
	}
}

//nolint:gocognit // table-driven multi-field assertion; extracting helpers adds noise without reducing complexity
func TestDiscordStub_CallRecording(t *testing.T) {
	stub := NewDiscordStub(t)
	stub.ApproveAll = true

	ctx := context.Background()
	reqs := []ApprovalRequest{
		{RequesterHost: "alpha", Scopes: []string{"s1"}, SessionType: "interactive", TTL: time.Hour, MaxUses: 1},
		{RequesterHost: "beta", Scopes: []string{"s2"}, SessionType: "supervisor", TTL: 2 * time.Hour, MaxUses: 0},
		{RequesterHost: "gamma", Scopes: []string{"s3", "s4"}, SessionType: "interactive", TTL: 30 * time.Minute, MaxUses: 10},
	}
	for _, r := range reqs {
		if _, err := stub.RequestApproval(ctx, r); err != nil {
			t.Fatalf("RequestApproval: %v", err)
		}
	}

	calls := stub.Calls()
	if len(calls) != 3 {
		t.Fatalf("expected 3 calls, got %d", len(calls))
	}
	for i, c := range calls {
		if c.Index != i {
			t.Errorf("call %d: Index = %d, want %d", i, c.Index, i)
		}
		if c.Decision != DecisionApprove {
			t.Errorf("call %d: Decision = %v, want DecisionApprove", i, c.Decision)
		}
		if c.Err != nil {
			t.Errorf("call %d: Err = %v, want nil", i, c.Err)
		}
		if c.Request.RequesterHost != reqs[i].RequesterHost {
			t.Errorf("call %d: RequesterHost = %q, want %q", i, c.Request.RequesterHost, reqs[i].RequesterHost)
		}
	}
}

// TestDiscordStub_UnexpectedCall uses subprocess testing because the unexpected-call
// path calls t.Errorf, which marks the test handle as failed and propagates upward.
func TestDiscordStub_UnexpectedCall(t *testing.T) {
	const envKey = "HUSH_TESTUTIL_DISCORD_UNEXPECTED"
	if os.Getenv(envKey) == "1" {
		stub := NewDiscordStub(t)
		req := ApprovalRequest{RequesterHost: "h", Scopes: []string{"scope"}, SessionType: "interactive", TTL: time.Hour, MaxUses: 2}
		d, err := stub.RequestApproval(context.Background(), req)
		if d != DecisionDeny {
			t.Errorf("expected DecisionDeny, got %v", d)
		}
		if !errors.Is(err, ErrUnexpectedCall) {
			t.Errorf("expected ErrUnexpectedCall, got %v", err)
		}
		return
	}

	//nolint:gosec // subprocess test pattern: os.Args[0] is the compiled test binary, not external user input
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestDiscordStub_UnexpectedCall$", "-test.v")
	cmd.Env = append(os.Environ(), envKey+"=1")
	out, err := cmd.CombinedOutput()
	output := string(out)

	if err == nil {
		t.Fatalf("expected subprocess to fail; output: %q", output)
	}
	for _, want := range []string{"h", "scope", "interactive"} {
		if !strings.Contains(output, want) {
			t.Errorf("failure message missing %q; output: %q", want, output)
		}
	}
}

func TestDiscordStub_Concurrent(t *testing.T) {
	stub := NewDiscordStub(t)
	stub.ApproveAll = true

	const n = 100
	var wg sync.WaitGroup
	wg.Add(n)
	for i := range n {
		go func(i int) {
			defer wg.Done()
			req := ApprovalRequest{RequesterHost: "h", Scopes: []string{"s"}, SessionType: "interactive"}
			if _, err := stub.RequestApproval(context.Background(), req); err != nil {
				t.Errorf("goroutine %d: unexpected error: %v", i, err)
			}
		}(i)
	}
	wg.Wait()

	calls := stub.Calls()
	if len(calls) != n {
		t.Errorf("expected %d recorded calls, got %d", n, len(calls))
	}
	for _, c := range calls {
		if c.Decision != DecisionApprove {
			t.Errorf("call %d: Decision = %v, want DecisionApprove", c.Index, c.Decision)
		}
		if c.Err != nil {
			t.Errorf("call %d: Err = %v, want nil", c.Index, c.Err)
		}
	}
}

//nolint:gocognit // multi-level AST walk; linearising further hurts readability without reducing logic branches
func TestDiscordStub_NoNetwork(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	pkgDir := filepath.Dir(filename)

	fset := token.NewFileSet()
	//nolint:staticcheck // parser.ParseDir is deprecated since Go 1.25 but golang.org/x/tools/go/packages would add a new module dependency; our use case (import-only scan, no build tags) is safe with ParseDir
	pkgs, err := parser.ParseDir(fset, pkgDir, func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parser.ParseDir: %v", err)
	}

	banned := []string{"net", "net/http", "github.com/bwmarrin/discordgo"}
	for _, pkg := range pkgs {
		for _, f := range pkg.Files {
			for _, imp := range f.Imports {
				path := strings.Trim(imp.Path.Value, `"`)
				for _, b := range banned {
					if path == b || strings.HasPrefix(path, b+"/") {
						t.Errorf("production file imports network package %q", path)
					}
				}
			}
		}
	}
}

func TestDiscordStub_CallsDefensiveCopy(t *testing.T) {
	stub := NewDiscordStub(t)
	stub.ApproveAll = true

	if _, err := stub.RequestApproval(context.Background(), ApprovalRequest{RequesterHost: "h"}); err != nil {
		t.Fatalf("RequestApproval: %v", err)
	}

	first := stub.Calls()
	if len(first) != 1 {
		t.Fatalf("expected 1 call, got %d", len(first))
	}
	first[0].Decision = DecisionDeny

	second := stub.Calls()
	if second[0].Decision != DecisionApprove {
		t.Error("Calls() returned a non-defensive copy — mutation corrupted internal state")
	}
}

func TestDiscordStub_CleanupDrains(t *testing.T) {
	var stub *DiscordStub
	t.Run("inner", func(t *testing.T) {
		stub = NewDiscordStub(t)
		stub.ApproveAll = true
		if _, err := stub.RequestApproval(context.Background(), ApprovalRequest{RequesterHost: "h"}); err != nil {
			t.Fatalf("RequestApproval: %v", err)
		}
	})
	if calls := stub.Calls(); len(calls) != 0 {
		t.Errorf("after cleanup, stub.Calls() should be empty, got %d entries", len(calls))
	}
}
