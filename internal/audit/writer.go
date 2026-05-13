package audit

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync/atomic"
	"time"

	"golang.org/x/sys/unix"
)

// secureRandReader returns the io.Reader used by ECDSA signing.  Wrapped
// in a function-valued package variable so tests may override the source
// of randomness via export_test.go (set-once at test load).
//
//nolint:gochecknoglobals // sentinel-class test seam; production reads rand.Reader
var secureRandReader = func() io.Reader { return rand.Reader }

// Writer is the producer-facing interface.  Implementations are
// concurrency-safe; Append blocks under producer contention (FR-031) and
// returns nil only AFTER the event is on disk (FR-033).
type Writer interface {
	Append(ctx context.Context, action string, data map[string]any) error
	Run(ctx context.Context) error
}

// nowFn returns the wall-clock used for event timestamps.  Wrapped in a
// package-level function pointer so tests can drive deterministic times.
//
//nolint:gochecknoglobals // OS bridge; test-hookable for deterministic timestamps
var nowFn = time.Now

// pending is the rendezvous payload exchanged between Append and the
// writer goroutine.  Unbuffered.
type pending struct {
	action string
	data   map[string]any
	ack    chan eventAck
}

type eventAck struct {
	seq uint64
	err error
}

// writerImpl is the production [Writer].  Owns the on-disk file handle
// and the chain state (seq + prevHash) — exactly one goroutine touches
// either, the writer goroutine spawned by Run.
type writerImpl struct {
	path     string
	signKey  *ecdsa.PrivateKey
	mirror   *DiscordMirror
	logger   *slog.Logger
	accept   chan *pending
	shutdown chan struct{}

	runStarted atomic.Bool
	closed     atomic.Bool

	// state owned exclusively by the writer goroutine after Run begins.
	file     *os.File
	bw       *bufio.Writer
	seq      uint64
	prevHash []byte

	// test hooks (non-nil only in tests; see export_test.go).
	hookBeforeFlush func()
}

// NewWriter constructs a Writer.  Validates inputs synchronously; the
// long-lived goroutine starts on Run(ctx).  When the chain file exists,
// NewWriter scans the last line to recover Seq and prevHash; when it
// does not exist, NewWriter starts the chain at Seq=1 with the genesis
// predecessor hash.  mirror MAY be nil.  logger MUST NOT be nil.
//
// The supplied ctx parameter is used only for input validation
// (`ctx.Err()` short-circuit); the actual long-lived ctx is the one
// passed to [Writer.Run].
func NewWriter(
	ctx context.Context,
	path string,
	signKey *ecdsa.PrivateKey,
	mirror *DiscordMirror,
	logger *slog.Logger,
) (Writer, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if path == "" {
		return nil, ErrInvalidPath
	}
	if err := validateSigningKey(signKey); err != nil {
		return nil, err
	}
	if logger == nil {
		return nil, ErrInvalidLogger
	}

	seq, prev, err := readChainTail(path)
	if err != nil {
		return nil, err
	}

	w := &writerImpl{
		path:     path,
		signKey:  signKey,
		mirror:   mirror,
		logger:   logger,
		accept:   make(chan *pending),
		shutdown: make(chan struct{}),
		seq:      seq,
		prevHash: prev,
	}
	return w, nil
}

// Append synchronously rendezvouses with the writer goroutine.  Returns
// nil only AFTER the event has been assigned a Seq, hashed, signed, and
// persisted to disk.  Returns [ErrShutdown] if Run's ctx is cancelled.
// Returns ctx.Err() if the caller's ctx is cancelled before rendezvous.
func (w *writerImpl) Append(ctx context.Context, action string, data map[string]any) error {
	if action == "" {
		return ErrEmptyAction
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if w.closed.Load() {
		return ErrShutdown
	}

	p := &pending{
		action: action,
		data:   data,
		ack:    make(chan eventAck, 1),
	}
	select {
	case w.accept <- p:
	case <-w.shutdown:
		return ErrShutdown
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case ack := <-p.ack:
		return ack.err
	case <-ctx.Done():
		// The writer will still process the pending; the caller has
		// abandoned waiting.  We cannot prevent the disk write.
		return ctx.Err()
	}
}

// Run spawns the writer goroutine and (when configured) the mirror
// goroutine.  Blocks until ctx cancels and every in-flight pending event
// has been drained.  Returns a wrapped error if any persistence step
// failed during drain.
//
// Single-call lifecycle.  A second call returns [ErrAlreadyRun].
//
//nolint:gocyclo,gocognit,cyclop // sequential lifecycle: spawn → serve → drain → close
func (w *writerImpl) Run(ctx context.Context) error {
	if !w.runStarted.CompareAndSwap(false, true) {
		return ErrAlreadyRun
	}

	f, err := os.OpenFile(w.path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("audit: open chain file: %w", err)
	}
	// Exclusive non-blocking advisory lock on the chain file. Two Writers
	// at the same path would silently corrupt the hash chain (both read
	// the same prevHash at construction and then append in parallel);
	// the flock makes the second Run fail loudly with ErrChainLocked.
	// Released automatically when f.Close() runs in the shutdown sequence
	// (lines 222–225 below).
	if lockErr := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); lockErr != nil {
		_ = f.Close()
		if errors.Is(lockErr, unix.EWOULDBLOCK) {
			return fmt.Errorf("audit: %w (path=%s)", ErrChainLocked, w.path)
		}
		return fmt.Errorf("audit: flock chain file: %w", lockErr)
	}
	w.file = f
	w.bw = bufio.NewWriter(f)

	mirrorDone := make(chan struct{})
	if w.mirror != nil {
		w.mirror.attach(w.logger)
		go func() {
			defer close(mirrorDone)
			w.mirror.run(ctx)
		}()
	} else {
		close(mirrorDone)
	}

	var runErr error
loop:
	for {
		select {
		case <-ctx.Done():
			break loop
		case p := <-w.accept:
			if err := w.process(p); err != nil {
				runErr = err
			}
		}
	}

	w.closed.Store(true)
	close(w.shutdown)

	// Drain remaining pendings already sent.  After close(w.shutdown),
	// new Append calls return ErrShutdown immediately.  Any pending
	// already in flight on the rendezvous channel is delivered here.
drainLoop:
	for {
		select {
		case p := <-w.accept:
			_ = w.process(p) // drain on best-effort during shutdown
		default:
			break drainLoop
		}
	}

	_ = w.bw.Flush()
	_ = w.file.Sync()
	_ = w.file.Close()

	if w.mirror != nil {
		w.mirror.shutdown()
	}
	<-mirrorDone

	return runErr
}

// process is the writer goroutine's per-event handler.  Computes Seq +
// Hash + Signature, persists, signals the producer, and best-effort
// dispatches to the mirror.
func (w *writerImpl) process(p *pending) error {
	w.seq++
	ev, hashBytes, buildErr := w.buildEvent(p)
	if buildErr != nil {
		w.seq--
		p.ack <- eventAck{err: buildErr}
		return buildErr
	}

	if w.hookBeforeFlush != nil {
		w.hookBeforeFlush()
	}

	// Event is a closed struct of strings + int + map[string]any; values
	// originate from the producer (via canonical-builder helpers in the
	// caller). json.Marshal cannot fail when canonicalisation already
	// succeeded inside computeHash above.
	line, _ := json.Marshal(ev) //nolint:errchkjson // closed Event shape
	line = append(line, '\n')
	if writeErr := w.persistLine(line); writeErr != nil {
		w.seq--
		p.ack <- eventAck{err: writeErr}
		return writeErr
	}

	w.prevHash = hashBytes
	p.ack <- eventAck{seq: ev.Seq}

	if w.mirror != nil {
		w.mirror.publish(ev)
	}
	return nil
}

// buildEvent constructs an [Event] populated with Seq/Time/Action/Data/
// PrevHash, then computes Hash and Signature.  Returns the event, the
// raw 32-byte hash (to advance the prevHash chain on success), and any
// error from canonicalising the preimage (the only reachable error
// path — signEventHash with a validated key cannot fail in practice).
func (w *writerImpl) buildEvent(p *pending) (Event, []byte, error) {
	prevHashHex := hex.EncodeToString(w.prevHash)
	ev := Event{
		Seq:      w.seq,
		Time:     nowFn().UTC(),
		Action:   p.action,
		Data:     p.data,
		PrevHash: prevHashHex,
	}
	hashBytes, err := computeHash(w.prevHash, ev)
	if err != nil {
		return Event{}, nil, err
	}
	ev.Hash = hex.EncodeToString(hashBytes)
	sigB64, _ := signEventHash(w.signKey, hashBytes)
	ev.Signature = sigB64
	return ev, hashBytes, nil
}

// persistLine writes line to the bufio.Writer, Flushes, then fsyncs the
// underlying file.  The fsync upholds FR-033 ("returns nil only AFTER
// the event is on disk"): bufio.Flush only drains user-space buffers
// into the kernel page cache; without Sync, a kernel panic or power
// loss before the next periodic writeback would lose the chain tail.
// Approval volume is human-paced so the per-event sync cost is
// negligible.  Any step can fail when the underlying file is closed;
// the caller maps the failure to the producer's ack error.
func (w *writerImpl) persistLine(line []byte) error {
	if _, err := w.bw.Write(line); err != nil {
		return fmt.Errorf("audit: write: %w", err)
	}
	if err := w.bw.Flush(); err != nil {
		return fmt.Errorf("audit: flush: %w", err)
	}
	if err := w.file.Sync(); err != nil {
		return fmt.Errorf("audit: sync: %w", err)
	}
	return nil
}
