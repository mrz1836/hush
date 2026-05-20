package audit

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bwmarrin/discordgo"
)

// MirrorSession is the narrow seam over *discordgo.Session that the
// mirror invokes.  *discordgo.Session satisfies it structurally so
// production wiring needs no adapter; tests wire a stub.
type MirrorSession interface {
	ChannelMessageSendComplex(channelID string, data *discordgo.MessageSend, opts ...discordgo.RequestOption) (*discordgo.Message, error)
}

// mirrorBufferSize is the depth of the writer→mirror buffered channel.
// Reaching the bound drops the mirror copy of the offending event (the
// on-disk chain is unaffected) and a WARN is logged.
const mirrorBufferSize = 64

// mirrorShutdownTimeout caps how long the mirror goroutine waits to
// drain its buffer when its ctx cancels.
const mirrorShutdownTimeout = 5 * time.Second

// DiscordMirror is the optional best-effort chat-platform publisher.
// Pass nil to NewWriter to disable mirroring entirely.
type DiscordMirror struct {
	channelID string
	session   MirrorSession
	ch        chan Event
	logger    *slog.Logger

	// failureSink is invoked synchronously when a publish fails.  Used by
	// the writer-side wiring to emit `audit_mirror_failed` chain events.
	// Nil in tests that don't want the loop.
	failureSink func(seq uint64, action, errClass string)

	once       sync.Once
	shutdownCh chan struct{}

	// test hook: when non-nil, the mirror goroutine calls it before each
	// publish attempt.  Allows tests to pause the goroutine.
	hookBeforePublish func()

	// counters for tests / observability.
	dropped atomic.Uint64
}

// NewDiscordMirror constructs a DiscordMirror.  An empty channelID OR
// nil session disables the mirror entirely — the writer skips
// dispatch and never spawns the goroutine.
func NewDiscordMirror(channelID string, session MirrorSession) *DiscordMirror {
	if channelID == "" || session == nil {
		return &DiscordMirror{}
	}
	return &DiscordMirror{
		channelID:  channelID,
		session:    session,
		ch:         make(chan Event, mirrorBufferSize),
		shutdownCh: make(chan struct{}),
	}
}

// enabled reports whether the mirror should run.
func (m *DiscordMirror) enabled() bool { return m != nil && m.channelID != "" && m.session != nil }

// attach sets the logger pointer used by the mirror goroutine.  Called
// once from Writer.Run before the goroutine is spawned.
func (m *DiscordMirror) attach(logger *slog.Logger) {
	if !m.enabled() {
		return
	}
	m.logger = logger
}

// publish is the writer-goroutine-side, non-blocking send to the mirror
// channel.  Drop-on-full.
func (m *DiscordMirror) publish(ev Event) {
	if !m.enabled() {
		return
	}
	select {
	case m.ch <- ev:
	default:
		m.dropped.Add(1)
		if m.logger != nil {
			m.logger.Warn("audit mirror buffer full; dropping mirror copy",
				slog.Uint64("seq", ev.Seq),
				slog.String("action", ev.Action))
		}
	}
}

// run is the long-lived mirror goroutine.  Reads events from the
// buffered channel, calls session.ChannelMessageSendComplex, logs WARN
// on failure (never the bot token, never the event signature), and
// never retries.
func (m *DiscordMirror) run(ctx context.Context) {
	if !m.enabled() {
		return
	}
	for {
		select {
		case <-ctx.Done():
			m.drain()
			return
		case <-m.shutdownCh:
			m.drain()
			return
		case ev := <-m.ch:
			m.send(ev)
		}
	}
}

// drain consumes any remaining buffered events on shutdown, bounded by
// mirrorShutdownTimeout.
func (m *DiscordMirror) drain() {
	deadline := time.NewTimer(mirrorShutdownTimeout)
	defer deadline.Stop()
	for {
		select {
		case ev := <-m.ch:
			m.send(ev)
		case <-deadline.C:
			return
		default:
			return
		}
	}
}

// shutdown signals the run goroutine to drain and exit.  Idempotent.
func (m *DiscordMirror) shutdown() {
	if !m.enabled() {
		return
	}
	m.once.Do(func() {
		close(m.shutdownCh)
	})
}

// send performs one publish attempt.  On failure the mirror logs WARN
// and (if configured) calls failureSink so the writer can emit an
// `audit_mirror_failed` chain event.  No retries.
func (m *DiscordMirror) send(ev Event) {
	if m.hookBeforePublish != nil {
		m.hookBeforePublish()
	}
	msg := &discordgo.MessageSend{
		Content: m.renderMessage(ev),
	}
	_, err := m.session.ChannelMessageSendComplex(m.channelID, msg)
	if err != nil {
		errClass := classifyMirrorErr(err)
		if m.logger != nil {
			m.logger.Warn("audit mirror publish failed",
				slog.Uint64("seq", ev.Seq),
				slog.String("action", ev.Action),
				slog.String("err_class", errClass))
		}
		if m.failureSink != nil {
			m.failureSink(ev.Seq, ev.Action, errClass)
		}
	}
}

// renderMessage formats a human-readable mirror message from an event.
// The message intentionally carries only seq + time + action + a short
// stable subset of Data keys — never signatures, never raw bytes.
func (m *DiscordMirror) renderMessage(ev Event) string {
	return formatMirrorMessage(ev)
}

func formatMirrorMessage(ev Event) string {
	const layout = "2006-01-02T15:04:05Z"
	out := "audit seq=" + uint64ToA(ev.Seq) + " time=" + ev.Time.UTC().Format(layout) + " action=" + ev.Action
	return out
}

// uint64ToA is a small alloc-free uint64-to-decimal-string helper.  Used
// only by formatMirrorMessage.
func uint64ToA(n uint64) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// classifyMirrorErr categorises an underlying transport error into a
// short stable label suitable for logging.  Never echoes the underlying
// message verbatim — that could leak the bot token via discordgo's
// formatted errors.
func classifyMirrorErr(err error) string {
	if err == nil {
		return ""
	}
	// discordgo wraps RESTError values; we don't introspect the body.
	// A short class label is the contract.
	return "mirror_publish_failed"
}
