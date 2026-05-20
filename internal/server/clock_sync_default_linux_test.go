package server

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestDefaultClockSyncProbe_Linux_Parses covers the read-only timedatectl
// probe used on Linux. It exercises the NTPSynchronized=yes/no verdicts and
// the unexpected-output and exec-failure branches.
//
//nolint:gocognit // table-driven test exercises many timedatectl branches
func TestDefaultClockSyncProbe_Linux_Parses(t *testing.T) {
	old := execNTPSynchronised
	t.Cleanup(func() { execNTPSynchronised = old })

	cases := []struct {
		name     string
		stub     func(ctx context.Context) (string, error)
		wantSync bool
		wantErr  bool
	}{
		{
			name:     "synchronised",
			stub:     func(_ context.Context) (string, error) { return "yes\n", nil },
			wantSync: true,
		},
		{
			name:     "not synchronised",
			stub:     func(_ context.Context) (string, error) { return "no\n", nil },
			wantSync: false,
		},
		{
			name:    "exec error",
			stub:    func(_ context.Context) (string, error) { return "", errClockProbeStub },
			wantErr: true,
		},
		{
			name:    "junk",
			stub:    func(_ context.Context) (string, error) { return "maybe\n", nil },
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			execNTPSynchronised = tc.stub
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
			if drift != 0 {
				t.Fatalf("drift=%v want 0", drift)
			}
		})
	}
}

func TestDefaultClockSyncProbe_Linux_ErrorIncludesTimedatectlOutput(t *testing.T) {
	old := execNTPSynchronised
	t.Cleanup(func() { execNTPSynchronised = old })
	execNTPSynchronised = func(_ context.Context) (string, error) {
		return "Failed to query server: connection refused", errors.New("exit status 1")
	}

	_, _, err := DefaultClockSyncProbe(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("error should include timedatectl output, got %v", err)
	}
}

func TestDefaultClockSyncProbe_Linux_JunkIsUnexpectedOutput(t *testing.T) {
	old := execNTPSynchronised
	t.Cleanup(func() { execNTPSynchronised = old })
	execNTPSynchronised = func(_ context.Context) (string, error) { return "garbage\n", nil }

	_, _, err := DefaultClockSyncProbe(context.Background())
	if !errors.Is(err, ErrClockProbeUnexpectedOutput) {
		t.Fatalf("err=%v want ErrClockProbeUnexpectedOutput", err)
	}
}
