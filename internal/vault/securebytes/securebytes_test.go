package securebytes

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Static test sentinels — satisfy err113 (no dynamic errors in function bodies).
var (
	errTestMLock   = errors.New("simulated mlock failure")
	errTestMUnlock = errors.New("simulated munlock failure")
)

// TestSecureBytes_New_CopiesAndZeroesInput covers G1 (FR-003, FR-004, SC-005, SC-006).
func TestSecureBytes_New_CopiesAndZeroesInput(t *testing.T) { //nolint:gocognit,gocyclo // table-driven sub-tests; complexity is structural, not accidental
	t.Run("nil input", func(t *testing.T) {
		sb, err := New(nil)
		if err != nil {
			t.Fatalf("New(nil) error: %v", err)
		}
		if sb == nil {
			t.Fatal("New(nil) returned nil container")
		}
		if sb.Len() != 0 {
			t.Errorf("Len() = %d, want 0", sb.Len())
		}
		_ = sb.Destroy()
	})

	t.Run("empty slice", func(t *testing.T) {
		sb, err := New([]byte{})
		if err != nil {
			t.Fatalf("New([]byte{}) error: %v", err)
		}
		if sb == nil {
			t.Fatal("New([]byte{}) returned nil container")
		}
		if sb.Len() != 0 {
			t.Errorf("Len() = %d, want 0", sb.Len())
		}
		_ = sb.Destroy()
	})

	t.Run("non-zero 32-byte input", func(t *testing.T) {
		original := make([]byte, 32)
		for i := range original {
			original[i] = byte(i + 1)
		}
		want := make([]byte, 32)
		copy(want, original)
		input := make([]byte, 32)
		copy(input, original)

		sb, err := New(input)
		if err != nil {
			t.Fatalf("New error: %v", err)
		}
		if sb == nil {
			t.Fatal("New returned nil container")
		}

		// Input slice must be zeroed.
		for i, b := range input {
			if b != 0 {
				t.Errorf("input[%d] = %d after New, want 0", i, b)
			}
		}

		// Container length must match original.
		if sb.Len() != len(want) {
			t.Errorf("Len() = %d, want %d", sb.Len(), len(want))
		}

		// Bytes inside container must equal original payload.
		var got []byte
		if err := sb.Use(func(b []byte) {
			got = make([]byte, len(b))
			copy(got, b)
		}); err != nil {
			t.Fatalf("Use error: %v", err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("container holds %v, want %v", got, want)
		}

		_ = sb.Destroy()
	})
}

// TestSecureBytes_Use_DeliversPayload covers G2 (FR-006, FR-008).
func TestSecureBytes_Use_DeliversPayload(t *testing.T) { //nolint:gocognit // multiple sub-tests for concurrent, panic, and payload paths; complexity is structural
	payload := []byte("correct-horse-battery-staple")
	input := make([]byte, len(payload))
	copy(input, payload)

	sb, err := New(input)
	if err != nil {
		t.Fatalf("New error: %v", err)
	}
	defer func() { _ = sb.Destroy() }()

	t.Run("callback receives correct bytes", func(t *testing.T) {
		var got []byte
		if err := sb.Use(func(b []byte) {
			got = make([]byte, len(b))
			copy(got, b)
		}); err != nil {
			t.Fatalf("Use error: %v", err)
		}
		if !bytes.Equal(got, payload) {
			t.Errorf("Use delivered %v, want %v", got, payload)
		}
	})

	t.Run("concurrent borrows see correct bytes", func(t *testing.T) {
		const n = 16
		var wg sync.WaitGroup
		var ready sync.WaitGroup
		ready.Add(1)
		var failed atomic.Int64

		for i := 0; i < n; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				ready.Wait()
				if err := sb.Use(func(b []byte) {
					if !bytes.Equal(b, payload) {
						failed.Add(1)
					}
				}); err != nil {
					failed.Add(1)
				}
			}()
		}

		ready.Done()
		wg.Wait()

		if failed.Load() != 0 {
			t.Errorf("%d concurrent Use calls saw wrong bytes", failed.Load())
		}
	})

	t.Run("panic in callback leaves container live", func(t *testing.T) {
		defer func() {
			if r := recover(); r == nil {
				t.Error("expected panic to propagate")
			}
		}()

		_ = sb.Use(func(_ []byte) {
			panic("test panic")
		})
	})

	// After the panic, the container must still be usable.
	t.Run("container still live after panicking callback", func(t *testing.T) {
		if err := sb.Use(func(_ []byte) {}); err != nil {
			t.Errorf("Use after panic returned error: %v", err)
		}
	})
}

// TestSecureBytes_Render_RedactsAllPaths covers G5 LIVE path (FR-014, FR-015, FR-016).
func TestSecureBytes_Render_RedactsAllPaths(t *testing.T) {
	input := []byte("super-secret-value")
	sb, err := New(input)
	if err != nil {
		t.Fatalf("New error: %v", err)
	}
	defer func() { _ = sb.Destroy() }()

	if got := sb.LogValue(); !got.Equal(slog.StringValue("[redacted]")) {
		t.Errorf("LogValue() = %v, want [redacted]", got)
	}
	if got := sb.String(); got != "[redacted]" {
		t.Errorf("String() = %q, want [redacted]", got)
	}
	if got := fmt.Sprintf("%v", sb); got != "[redacted]" {
		t.Errorf("%%v = %q, want [redacted]", got)
	}
	if got := sb.String(); got != "[redacted]" { // %s calls String(); tested via String() directly
		t.Errorf("%%s = %q, want [redacted]", got)
	}
	j, err := sb.MarshalJSON()
	if err != nil {
		t.Fatalf("MarshalJSON error: %v", err)
	}
	if string(j) != `"[redacted]"` {
		t.Errorf("MarshalJSON() = %s, want \"[redacted]\"", j)
	}
}

// TestSecureBytes_RedactionSentinel covers G6 (SC-001).
func TestSecureBytes_RedactionSentinel(t *testing.T) { //nolint:gocyclo // asserts multiple output paths (slog, %s, %v, json); complexity is exhaustiveness
	const sentinel = "SECRET_SHOULD_NEVER_APPEAR_2"
	sb, err := New([]byte(sentinel))
	if err != nil {
		t.Fatalf("New error: %v", err)
	}
	defer func() { _ = sb.Destroy() }()

	// slog.JSONHandler capture
	var buf bytes.Buffer
	handler := slog.NewJSONHandler(&buf, nil)
	logger := slog.New(handler)
	logger.Info("entry", "secret", sb)

	slogOut := buf.Bytes()
	if !bytes.Contains(slogOut, []byte("[redacted]")) {
		t.Errorf("slog output missing [redacted]: %s", slogOut)
	}
	if bytes.Contains(slogOut, []byte(sentinel)) {
		t.Errorf("slog output contains sentinel: %s", slogOut)
	}

	// fmt captures — %s calls String(), %v goes through fmt.Stringer
	fmtS := sb.String()
	if fmtS != "[redacted]" {
		t.Errorf("%%s = %q, want [redacted]", fmtS)
	}
	if strings.Contains(fmtS, sentinel) {
		t.Errorf("%%s output contains sentinel: %s", fmtS)
	}

	fmtV := fmt.Sprintf("%v", sb)
	if fmtV != "[redacted]" {
		t.Errorf("%%v = %q, want [redacted]", fmtV)
	}
	if strings.Contains(fmtV, sentinel) {
		t.Errorf("%%v output contains sentinel: %s", fmtV)
	}

	// json.Marshal capture
	j, err := json.Marshal(sb)
	if err != nil {
		t.Fatalf("json.Marshal error: %v", err)
	}
	if !bytes.Contains(j, []byte("[redacted]")) {
		t.Errorf("json.Marshal output missing [redacted]: %s", j)
	}
	if bytes.Contains(j, []byte(sentinel)) {
		t.Errorf("json.Marshal output contains sentinel: %s", j)
	}
}

// TestSecureBytes_Destroy_ZeroesAndIdempotent covers G3 (FR-010, FR-011, SC-002, SC-007).
func TestSecureBytes_Destroy_ZeroesAndIdempotent(t *testing.T) {
	payload := []byte{1, 2, 3, 4, 5}
	input := make([]byte, len(payload))
	copy(input, payload)

	sb, err := New(input)
	if err != nil {
		t.Fatalf("New error: %v", err)
	}

	// Capture a copy of the buffer pointer via Use before Destroy.
	var captured []byte
	if err := sb.Use(func(b []byte) {
		captured = b // deliberate retain for white-box verification
	}); err != nil {
		t.Fatalf("Use error: %v", err)
	}

	if err := sb.Destroy(); err != nil {
		t.Errorf("first Destroy returned error: %v", err)
	}

	// The buffer that was handed to the callback must now be zeroed.
	for i, b := range captured {
		if b != 0 {
			t.Errorf("captured[%d] = %d after Destroy, want 0", i, b)
		}
	}

	// Second Destroy must be a no-op.
	if err := sb.Destroy(); err != nil {
		t.Errorf("second Destroy returned error: %v", err)
	}
}

// TestSecureBytes_PostDestroy_ReturnsErrDestroyed covers G4 (FR-009, FR-012, FR-018, SC-008).
func TestSecureBytes_PostDestroy_ReturnsErrDestroyed(t *testing.T) {
	sb, err := New([]byte("sensitive"))
	if err != nil {
		t.Fatalf("New error: %v", err)
	}
	err = sb.Destroy()
	if err != nil {
		t.Fatalf("Destroy error: %v", err)
	}

	// Use must return ErrDestroyed and NOT invoke the callback.
	invoked := false
	useErr := sb.Use(func(_ []byte) { invoked = true })
	if !errors.Is(useErr, ErrDestroyed) {
		t.Errorf("Use after Destroy = %v, want ErrDestroyed", useErr)
	}
	if invoked {
		t.Error("callback was invoked on a destroyed container")
	}

	// Len must report 0.
	if l := sb.Len(); l != 0 {
		t.Errorf("Len() after Destroy = %d, want 0", l)
	}

	// Render methods must still return [redacted].
	if got := sb.String(); got != "[redacted]" {
		t.Errorf("String() after Destroy = %q, want [redacted]", got)
	}
	j, err := sb.MarshalJSON()
	if err != nil {
		t.Fatalf("MarshalJSON after Destroy error: %v", err)
	}
	if string(j) != `"[redacted]"` {
		t.Errorf("MarshalJSON after Destroy = %s, want \"[redacted]\"", j)
	}
	if got := sb.LogValue(); !got.Equal(slog.StringValue("[redacted]")) {
		t.Errorf("LogValue() after Destroy = %v, want [redacted]", got)
	}
}

// TestSecureBytes_FinalizerZerosOnGC covers G7 (FR-013, SC-003).
func TestSecureBytes_FinalizerZerosOnGC(t *testing.T) {
	var finalized atomic.Bool

	allocAndForget := func() {
		sb, err := New([]byte("forgotten-secret"))
		if err != nil {
			t.Errorf("New error: %v", err)
			return
		}
		// Go 1.21+ throws fatally if a finalizer is already set when setting
		// a new one. Clear the production finalizer first with a truly-nil
		// (untyped) interface value so the runtime enters its "clear" branch,
		// then set the test-only wrapper that also records the flag.
		runtime.SetFinalizer(sb, nil)
		runtime.SetFinalizer(sb, func(s *SecureBytes) {
			s.finalize()
			finalized.Store(true)
		})
		// sb goes out of scope here.
	}
	allocAndForget()

	// Two GC cycles is the canonical pattern for finalizer tests.
	runtime.GC()
	runtime.GC()

	deadline := time.Now().Add(2 * time.Second)
	for !finalized.Load() {
		if time.Now().After(deadline) {
			t.Fatal("finalizer did not trigger within 2s")
		}
		runtime.Gosched()
		runtime.GC()
	}
}

// TestSecureBytes_ConcurrentUse covers G8 (FR-008, SC-010).
func TestSecureBytes_ConcurrentUse(t *testing.T) { //nolint:gocognit // goroutine fan-out + atomic checks; complexity is the race-detector test pattern
	payload := []byte("concurrent-secret")
	input := make([]byte, len(payload))
	copy(input, payload)

	sb, err := New(input)
	if err != nil {
		t.Fatalf("New error: %v", err)
	}
	defer func() { _ = sb.Destroy() }()

	n := runtime.GOMAXPROCS(0) * 4
	if n < 16 {
		n = 16
	}
	const iterations = 1000

	var wg sync.WaitGroup
	var counter atomic.Int64
	var failed atomic.Bool

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				if err := sb.Use(func(b []byte) {
					if !bytes.Equal(b, payload) {
						failed.Store(true)
					}
					counter.Add(1)
				}); err != nil {
					failed.Store(true)
				}
			}
		}()
	}

	wg.Wait()

	if failed.Load() {
		t.Error("concurrent Use observed wrong bytes or error")
	}
	if got := counter.Load(); got != int64(n*iterations) {
		t.Errorf("counter = %d, want %d", got, n*iterations)
	}
}

// TestSecureBytes_New_MlockError exercises the mlock error path in New.
func TestSecureBytes_New_MlockError(t *testing.T) {
	restore := SetMLock(func(_ []byte) error { return errTestMLock })
	defer restore()

	_, err := New([]byte("secret"))
	if err == nil {
		t.Fatal("expected error from mlock failure, got nil")
	}
	if !strings.Contains(err.Error(), "mlock") {
		t.Errorf("error %q does not mention mlock", err.Error())
	}
}

// TestSecureBytes_Destroy_MunlockError exercises the munlock error path in Destroy.
func TestSecureBytes_Destroy_MunlockError(t *testing.T) {
	sb, err := New([]byte("secret"))
	if err != nil {
		t.Fatalf("New error: %v", err)
	}

	restore := SetMUnlock(func(_ []byte) error { return errTestMUnlock })
	defer restore()

	destroyErr := sb.Destroy()
	if destroyErr == nil {
		t.Fatal("expected error from munlock failure, got nil")
	}
	if !strings.Contains(destroyErr.Error(), "munlock") {
		t.Errorf("error %q does not mention munlock", destroyErr.Error())
	}
	// Container must be marked destroyed despite the munlock error.
	if useErr := sb.Use(func(_ []byte) {}); !errors.Is(useErr, ErrDestroyed) {
		t.Errorf("Use after failed Destroy = %v, want ErrDestroyed", useErr)
	}
}
