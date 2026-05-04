package flow

import (
	"context"
	"log/slog"
	"strings"
	"sync"
)

func sessionTurnKey(botID, sessionID string) string {
	return strings.TrimSpace(botID) + ":" + strings.TrimSpace(sessionID)
}

func (r *Resolver) enterSessionTurn(ctx context.Context, botID, sessionID string) func() {
	botID = strings.TrimSpace(botID)
	sessionID = strings.TrimSpace(sessionID)
	if botID == "" || sessionID == "" {
		return func() {}
	}

	key := sessionTurnKey(botID, sessionID)

	// Acquire exclusive per-session access via a channel semaphore.
	// This prevents concurrent LLM calls for the same session.
	r.sessionTurnMu.Lock()
	if r.sessionTurnLocks == nil {
		r.sessionTurnLocks = make(map[string]chan struct{})
	}
	lockCh, exists := r.sessionTurnLocks[key]
	if !exists {
		lockCh = make(chan struct{}, 1)
		lockCh <- struct{}{} // pre-fill with one token
		r.sessionTurnLocks[key] = lockCh
	}
	r.sessionTurnMu.Unlock()

	// Block until we acquire the token or the context is cancelled.
	select {
	case <-lockCh:
		// Acquired exclusively
	case <-ctx.Done():
		return func() {} // context cancelled, do not proceed
	}

	// Increment ref-count for background notification tracking.
	r.sessionTurnMu.Lock()
	if r.sessionTurnRefs == nil {
		r.sessionTurnRefs = make(map[string]int)
	}
	r.sessionTurnRefs[key]++
	r.sessionTurnMu.Unlock()

	return r.makeSessionTurnReleaser(ctx, key, botID, sessionID)
}

func (r *Resolver) tryEnterIdleSessionTurn(ctx context.Context, botID, sessionID string) (func(), bool) {
	botID = strings.TrimSpace(botID)
	sessionID = strings.TrimSpace(sessionID)
	if botID == "" || sessionID == "" {
		return nil, false
	}

	key := sessionTurnKey(botID, sessionID)

	// Non-blocking acquire of the per-session semaphore.
	// If we fail to acquire immediately, the session is busy.
	r.sessionTurnMu.Lock()
	if r.sessionTurnLocks == nil {
		r.sessionTurnLocks = make(map[string]chan struct{})
	}
	lockCh, exists := r.sessionTurnLocks[key]
	if !exists {
		lockCh = make(chan struct{}, 1)
		lockCh <- struct{}{} // pre-fill with one token
		r.sessionTurnLocks[key] = lockCh
	}
	r.sessionTurnMu.Unlock()

	select {
	case <-lockCh:
		// Acquired — session is idle, proceed.
	default:
		// Session is busy, do not enter.
		return nil, false
	}

	// Increment ref-count for background notification tracking.
	r.sessionTurnMu.Lock()
	if r.sessionTurnRefs == nil {
		r.sessionTurnRefs = make(map[string]int)
	}
	r.sessionTurnRefs[key]++
	r.sessionTurnMu.Unlock()

	return r.makeSessionTurnReleaser(ctx, key, botID, sessionID), true
}

func (r *Resolver) makeSessionTurnReleaser(ctx context.Context, key, botID, sessionID string) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			becameIdle := false

			r.sessionTurnMu.Lock()
			switch refs := r.sessionTurnRefs[key] - 1; {
			case refs > 0:
				r.sessionTurnRefs[key] = refs
			default:
				delete(r.sessionTurnRefs, key)
				becameIdle = true
			}

			if becameIdle {
				// Release the semaphore token back to the channel.
				// Do NOT delete the entry from sessionTurnLocks — other
				// goroutines may still be waiting on the same channel.
				// The channel binary semaphore (1 token = idle, 0 tokens = busy)
				// correctly serializes access without map cleanup.
				if lockCh, ok := r.sessionTurnLocks[key]; ok {
					select {
					case lockCh <- struct{}{}:
					default:
						// Token already present (should not happen), nothing to do.
					}
				}
			}
			r.sessionTurnMu.Unlock()

			if becameIdle {
				r.maybeTriggerDeferredBackgroundNotifications(ctx, botID, sessionID)
			}
		})
	}
}

func (r *Resolver) markDeferredBackgroundNotification(botID, sessionID string) {
	botID = strings.TrimSpace(botID)
	sessionID = strings.TrimSpace(sessionID)
	if botID == "" || sessionID == "" {
		return
	}
	r.bgNotifDeferred.Store(sessionTurnKey(botID, sessionID), true)
}

func (r *Resolver) takeDeferredBackgroundNotification(botID, sessionID string) bool {
	botID = strings.TrimSpace(botID)
	sessionID = strings.TrimSpace(sessionID)
	if botID == "" || sessionID == "" {
		return false
	}
	_, loaded := r.bgNotifDeferred.LoadAndDelete(sessionTurnKey(botID, sessionID))
	return loaded
}

func (r *Resolver) maybeTriggerDeferredBackgroundNotifications(ctx context.Context, botID, sessionID string) {
	if !r.takeDeferredBackgroundNotification(botID, sessionID) {
		return
	}
	if r.bgManager == nil || !r.bgManager.HasNotifications(botID, sessionID) {
		return
	}

	r.logger.Info("background notification trigger queued after session became idle",
		slog.String("bot_id", botID),
		slog.String("session_id", sessionID),
	)
	if ctx == nil {
		return
	}
	go r.TriggerBackgroundNotification(context.WithoutCancel(ctx), botID, sessionID)
}
