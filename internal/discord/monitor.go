package discord

import (
	"context"
	"log/slog"
	"time"
)

// runMonitor owns the WebSocket-health side of the BotApprover. It is
// spawned by newBotApproverWithSession and cancelled by the
// constructor's ctx (Constitution IX).
//
// Termination contract: on ctx cancellation, the goroutine closes the
// session (idempotent), drains any pending request channels with
// ErrDiscordUnavailable, then closes monitorDone.
func (a *BotApprover) runMonitor(ctx context.Context) {
	defer close(a.monitorDone)
	defer func() {
		_ = a.session.Close()
		a.drainPending()
	}()
	defer a.recoverGoroutine("monitor", nil)

	for {
		select {
		case <-ctx.Done():
			return
		case <-a.reconnectSignal:
		}
		if a.runReconnectLoop(ctx) {
			return
		}
	}
}

// runReconnectLoop attempts session.Open() repeatedly with
// exponential backoff capped at reconnectMaxDelay until either
// (a) the available flag flips to true (Ready handler fired) or
// (b) the parent ctx is cancelled. Returns true when ctx cancelled.
func (a *BotApprover) runReconnectLoop(ctx context.Context) bool {
	failures := uint32(0)
	for !a.available.Load() {
		if ctx.Err() != nil {
			return true
		}
		delay := backoffDelay(failures, a.reconnectBaseDelay, a.reconnectMaxDelay)
		failures++
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return true
		case <-timer.C:
		}
		if a.available.Load() {
			return false
		}
		if err := a.session.Open(); err != nil {
			a.logger.Warn("hush/discord: reconnect attempt failed",
				slog.String("err_class", "discord_unavailable"))
			continue
		}
		// Open() succeeded; the Ready/Resumed handler will flip
		// available asynchronously. The next iteration's available
		// check will exit the loop. If discordgo never delivers
		// Ready, a subsequent Disconnect re-enters runReconnectLoop.
	}
	return false
}

// backoffDelay computes min(2^failures * base, max). failures==0 returns base.
func backoffDelay(failures uint32, base, maxDelay time.Duration) time.Duration {
	if base <= 0 {
		base = time.Second
	}
	if maxDelay <= 0 {
		maxDelay = 60 * time.Second
	}
	d := base
	for i := uint32(0); i < failures; i++ {
		if d >= maxDelay {
			return maxDelay
		}
		d *= 2
	}
	if d > maxDelay {
		d = maxDelay
	}
	return d
}

// drainPending sends a decisionUnavailable event to every pending
// request channel and removes the entry. Caller MUST have already
// flipped available to false (otherwise a concurrent RequestApproval
// could insert a new entry that races the drain). The monitor and
// onDisconnect both honor this ordering.
func (a *BotApprover) drainPending() {
	a.pending.Range(func(k, v any) bool {
		entry, ok := v.(*pendingEntry)
		if !ok {
			a.pending.Delete(k)
			return true
		}
		select {
		case entry.ch <- decisionEvent{kind: decisionUnavailable}:
		default:
		}
		a.pending.Delete(k)
		return true
	})
}

// onReady fires when discordgo emits a Ready event (initial connect
// completed without resume). Flip available to true and INFO-log.
func (a *BotApprover) onReady() {
	prev := a.available.Swap(true)
	if !prev {
		a.logger.Info("hush/discord: session ready")
	}
}

// onResumed fires when discordgo successfully resumes a previously
// dropped connection. Same effect as Ready.
func (a *BotApprover) onResumed() {
	prev := a.available.Swap(true)
	if !prev {
		a.logger.Info("hush/discord: session resumed")
	}
}

// onDisconnect fires on unexpected WebSocket close. Flip available
// to false, drain in-flight requests, signal the monitor to begin a
// reconnect loop.
func (a *BotApprover) onDisconnect() {
	prev := a.available.Swap(false)
	if prev {
		a.logger.Warn("hush/discord: session disconnected",
			slog.String("err_class", "discord_unavailable"))
	}
	a.drainPending()
	select {
	case a.reconnectSignal <- struct{}{}:
	default:
		// A reconnect signal is already pending — coalesce.
	}
}
