package server

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// TestDefaultClockSyncProbe_Darwin_Parses covers the read-only sntp probe used
// on macOS. Regression: `systemsetup -getusingnetworktime` can print
// administrator-access text for non-root users, which made guided setup fail even
// after `sudo sntp -sS time.apple.com` succeeded.
//
//nolint:gocognit // table-driven test exercises many sntp parsing branches
func TestDefaultClockSyncProbe_Darwin_Parses(t *testing.T) {
	old := execClockOffset
	oldProviders := DefaultClockSyncProviders
	t.Cleanup(func() {
		execClockOffset = old
		DefaultClockSyncProviders = oldProviders
	})
	DefaultClockSyncProviders = []string{"time.apple.com"}

	cases := []struct {
		name      string
		stub      func(ctx context.Context, provider string, timeout time.Duration) (string, error)
		wantSync  bool
		wantDrift time.Duration
		wantErr   bool
	}{
		{
			name: "positive offset",
			stub: func(_ context.Context, _ string, _ time.Duration) (string, error) {
				return "+0.029191 +/- 0.001 time.apple.com 17.253.6.37\n", nil
			},
			wantSync:  true,
			wantDrift: 29191 * time.Microsecond,
		},
		{
			name: "negative offset",
			stub: func(_ context.Context, _ string, _ time.Duration) (string, error) {
				return "-0.125000 +/- 0.002 time.apple.com\n", nil
			},
			wantSync:  true,
			wantDrift: -125 * time.Millisecond,
		},
		{
			name: "exec error",
			stub: func(_ context.Context, _ string, _ time.Duration) (string, error) {
				return "timeout", errClockProbeStub
			},
			wantErr: true,
		},
		{
			name:    "junk",
			stub:    func(_ context.Context, _ string, _ time.Duration) (string, error) { return "garbage\n", nil },
			wantErr: true,
		},
		{
			name: "systemsetup admin text is not parsed as clock state",
			stub: func(_ context.Context, _ string, _ time.Duration) (string, error) {
				return "systemsetup \"You need administrator access to run this tool... exiting!\"", nil
			},
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			execClockOffset = tc.stub
			synced, drift, err := DefaultClockSyncProbe(context.Background())
			if (err != nil) != tc.wantErr {
				t.Fatalf("err=%v wantErr=%v", err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}
			if synced != tc.wantSync {
				t.Fatalf("synced=%v want %v", synced, tc.wantSync)
			}
			if drift != tc.wantDrift {
				t.Fatalf("drift=%v want %v", drift, tc.wantDrift)
			}
		})
	}
}

func TestDefaultClockSyncProbe_Darwin_ErrorIncludesSNTPOutput(t *testing.T) {
	old := execClockOffset
	oldProviders := DefaultClockSyncProviders
	t.Cleanup(func() {
		execClockOffset = old
		DefaultClockSyncProviders = oldProviders
	})
	DefaultClockSyncProviders = []string{"time.apple.com"}
	execClockOffset = func(_ context.Context, _ string, _ time.Duration) (string, error) {
		return "timeout from sntp", errors.New("exit status 1")
	}

	_, _, err := DefaultClockSyncProbe(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "timeout from sntp") {
		t.Fatalf("error should include sntp output, got %v", err)
	}
}

func TestDefaultClockSyncProbe_Darwin_FirstProviderSuccessShortCircuits(t *testing.T) {
	old := execClockOffset
	oldProviders := DefaultClockSyncProviders
	t.Cleanup(func() {
		execClockOffset = old
		DefaultClockSyncProviders = oldProviders
	})
	DefaultClockSyncProviders = []string{"time.apple.com", "time.cloudflare.com"}
	var calls []string
	execClockOffset = func(_ context.Context, provider string, timeout time.Duration) (string, error) {
		if timeout != DefaultClockSyncProviderTimeout {
			t.Fatalf("timeout=%v want %v", timeout, DefaultClockSyncProviderTimeout)
		}
		calls = append(calls, provider)
		return "+0.010000 +/- 0.001 " + provider + "\n", nil
	}

	synced, drift, err := DefaultClockSyncProbe(context.Background())
	if err != nil {
		t.Fatalf("DefaultClockSyncProbe err=%v", err)
	}
	if !synced || drift != 10*time.Millisecond {
		t.Fatalf("synced=%v drift=%v", synced, drift)
	}
	if got := strings.Join(calls, ","); got != "time.apple.com" {
		t.Fatalf("providers called=%s", got)
	}
}

func TestDefaultClockSyncProbe_Darwin_SecondProviderSuccessAfterTimeout(t *testing.T) {
	old := execClockOffset
	oldProviders := DefaultClockSyncProviders
	t.Cleanup(func() {
		execClockOffset = old
		DefaultClockSyncProviders = oldProviders
	})
	DefaultClockSyncProviders = []string{"time.apple.com", "time.cloudflare.com"}
	execClockOffset = func(_ context.Context, provider string, _ time.Duration) (string, error) {
		if provider == "time.apple.com" {
			return "", context.DeadlineExceeded
		}
		return "-0.250000 +/- 0.001 " + provider + "\n", nil
	}

	synced, drift, err := DefaultClockSyncProbe(context.Background())
	if err != nil {
		t.Fatalf("DefaultClockSyncProbe err=%v", err)
	}
	if !synced || drift != -250*time.Millisecond {
		t.Fatalf("synced=%v drift=%v", synced, drift)
	}
}

func TestDefaultClockSyncProbe_Darwin_AllProvidersUnavailable(t *testing.T) {
	old := execClockOffset
	oldProviders := DefaultClockSyncProviders
	t.Cleanup(func() {
		execClockOffset = old
		DefaultClockSyncProviders = oldProviders
	})
	DefaultClockSyncProviders = []string{"time.apple.com", "time.cloudflare.com"}
	execClockOffset = func(_ context.Context, _ string, _ time.Duration) (string, error) {
		return "", context.DeadlineExceeded
	}

	_, _, err := DefaultClockSyncProbe(context.Background())
	if !errors.Is(err, ErrClockProbeUnavailable) {
		t.Fatalf("err=%v want ErrClockProbeUnavailable", err)
	}
}
