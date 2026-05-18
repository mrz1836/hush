package server

import (
	"context"
	"testing"
)

// TestDefaultClockSyncProbe_Darwin_Parses covers each parse branch of the
// darwin probe: synced=true, synced=false, exec error, junk output.
func TestDefaultClockSyncProbe_Darwin_Parses(t *testing.T) {
	old := execNetworkTime
	t.Cleanup(func() { execNetworkTime = old })

	cases := []struct {
		name     string
		stub     func(ctx context.Context) (string, error)
		wantSync bool
		wantErr  bool
	}{
		{
			name:     "on",
			stub:     func(_ context.Context) (string, error) { return "Network Time: On\n", nil },
			wantSync: true,
		},
		{
			name:     "off",
			stub:     func(_ context.Context) (string, error) { return "Network Time: Off\n", nil },
			wantSync: false,
		},
		{
			name:    "exec error",
			stub:    func(_ context.Context) (string, error) { return "", errClockProbeStub },
			wantErr: true,
		},
		{
			name:    "junk",
			stub:    func(_ context.Context) (string, error) { return "garbage\n", nil },
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			execNetworkTime = tc.stub
			synced, _, err := DefaultClockSyncProbe(context.Background())
			if (err != nil) != tc.wantErr {
				t.Fatalf("err=%v wantErr=%v", err, tc.wantErr)
			}
			if !tc.wantErr && synced != tc.wantSync {
				t.Fatalf("synced=%v want %v", synced, tc.wantSync)
			}
		})
	}
}
