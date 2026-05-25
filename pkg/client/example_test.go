package client_test

import (
	"context"
	"fmt"
	"time"

	"github.com/mrz1836/hush/pkg/client"
)

// ExampleSupervisorStatus_Snapshot shows the typical agent pattern:
// before doing work, ask the supervisor whether the credentials it
// holds are healthy and when they expire.
func ExampleSupervisorStatus_Snapshot() {
	// The socket path is typically passed to the child via the
	// HUSH_STATUS_SOCKET environment variable (PR 3).
	sup := client.NewSupervisorStatus("/var/run/hush/supervise-hermes.sock")
	defer func() { _ = sup.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	status, err := sup.Snapshot(ctx)
	if err != nil {
		fmt.Println("status unavailable:", err)
		return
	}

	if len(status.ScopeStale) > 0 {
		fmt.Println("refusing to run — stale scopes:", status.ScopeStale)
		return
	}
	fmt.Println("scopes healthy; expires at", status.SessionExpiresAt)
}
