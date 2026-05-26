// Package client is the public Go SDK for interacting with hush from an
// agent process. It exposes typed access to a supervisor's status socket
// so an agent can monitor its session without exec'ing the hush CLI.
//
// # Stability
//
// All exported identifiers in this package are part of hush's v1 public
// API. Breaking changes follow semantic-versioning rules at the module
// level. Wire-format additions appear as new optional fields with
// omitempty so older SDK builds continue to decode newer servers.
//
// # Example
//
//	sup := client.NewSupervisorStatus("/var/run/hush/supervise-hermes.sock")
//	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
//	defer cancel()
//	status, err := sup.Snapshot(ctx)
//	if err != nil {
//	    log.Fatal(err)
//	}
//	fmt.Println("expires at:", status.SessionExpiresAt)
//
// The SDK applies a defensive default deadline when ctx carries none,
// but callers should supply their own context.WithTimeout matched to
// their latency budget.
//
// # Scope
//
// PR 1 (this release) ships SupervisorStatus with Snapshot, SnapshotRaw,
// and Refresh. Later releases extend the type with a Subscribe method
// for streaming lifecycle events.
package client
