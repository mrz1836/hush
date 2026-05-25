// reload-child is the real HTTP test binary the integration scenario 16
// suite supervises through hush's HTTP-proxy reload flow. It is NOT a
// production binary; the file lives under tests/integration/testdata so
// `go vet ./...` and `go test ./...` ignore it.
//
// Behavior:
//   - Binds 127.0.0.1:$HUSH_BIND_PORT (required; the supervisor's reload
//     contract injects this env var).
//   - Serves /health: 200 OK ("ready") by default. When the env knob
//     HUSH_CHILD_FORCE_UNREADY is set, returns 503 forever — used by the
//     readiness-failure scenario to assert the supervisor rolls back.
//   - Serves /pid: the OS PID as a plain-text body.
//   - Serves /version: HUSH_CHILD_VERSION (default "v0") so the integration
//     test can assert "new" vs "old" child after a swap.
//   - Optionally ignores SIGTERM (HUSH_CHILD_IGNORE_SIGTERM=1) so the
//     SIGKILL escalation path in lifecycle_swap is exercisable.
//
// Exits 0 on a clean SIGTERM (when not ignoring). On unhandled signals or
// listener errors, exits non-zero. No secret material ever touches stdout
// or stderr: the binary only writes the bind address + a single ready
// line on stdout, and any error to stderr.
package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"
)

func main() {
	portStr := os.Getenv("HUSH_BIND_PORT")
	if portStr == "" {
		fmt.Fprintln(os.Stderr, "reload-child: HUSH_BIND_PORT empty")
		os.Exit(2)
	}
	port, err := strconv.ParseUint(portStr, 10, 16)
	if err != nil {
		fmt.Fprintf(os.Stderr, "reload-child: HUSH_BIND_PORT %q parse: %v\n", portStr, err)
		os.Exit(2)
	}
	version := os.Getenv("HUSH_CHILD_VERSION")
	if version == "" {
		version = "v0"
	}
	forceUnready := os.Getenv("HUSH_CHILD_FORCE_UNREADY") != ""
	ignoreSIGTERM := os.Getenv("HUSH_CHILD_IGNORE_SIGTERM") != ""

	addr := fmt.Sprintf("127.0.0.1:%d", port)
	var lc net.ListenConfig
	listener, lerr := lc.Listen(context.Background(), "tcp", addr)
	if lerr != nil {
		fmt.Fprintf(os.Stderr, "reload-child: listen %s: %v\n", addr, lerr)
		os.Exit(3)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		if forceUnready {
			w.Header().Set("X-Child-Version", version)
			w.Header().Set("X-Child-Pid", strconv.Itoa(os.Getpid()))
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("not-ready"))
			return
		}
		w.Header().Set("X-Child-Version", version)
		w.Header().Set("X-Child-Pid", strconv.Itoa(os.Getpid()))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ready"))
	})
	mux.HandleFunc("/pid", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(strconv.Itoa(os.Getpid())))
	})
	mux.HandleFunc("/version", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(version))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Child-Version", version)
		w.Header().Set("X-Child-Pid", strconv.Itoa(os.Getpid()))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 2 * time.Second}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(listener) }()

	sigCh := make(chan os.Signal, 1)
	if ignoreSIGTERM {
		signal.Ignore(syscall.SIGTERM)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGKILL)
	} else {
		signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	}

	fmt.Fprintf(os.Stdout, "reload-child %s ready on %s pid=%d\n", version, addr, os.Getpid())

	select {
	case <-sigCh:
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			fmt.Fprintf(os.Stderr, "reload-child: serve: %v\n", err)
			os.Exit(4)
		}
	}
	_ = srv.Close()
}
