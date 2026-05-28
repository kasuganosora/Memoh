package pipeline

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"time"

	agentpkg "github.com/memohai/memoh/internal/agent"
	"github.com/memohai/memoh/internal/channel"
	"github.com/memohai/memoh/internal/chattiming"
	"github.com/memohai/memoh/internal/conversation"
	"github.com/memohai/memoh/internal/memory/adapters"
	sessionpkg "github.com/memohai/memoh/internal/session"
)

// DiscussTrigger manages discuss-mode sessions. It is a pure strategy object
// that decides *when* and *how* to trigger the LLM, delegating the actual
// chat execution to the injected ChatRunner (flow.Resolver).
//
// Goroutine-safe: all exported methods may be called concurrently.
type DiscussTrigger struct {
	deps         DiscussTriggerDeps
	mu           sync.Mutex
	sessions     map[string]*discussSession
	logger       *slog.Logger
	parentCtx    context.Context
	parentCancel context.CancelFunc
}

// NewDiscussTrigger creates a new DiscussTrigger.
func NewDiscussTrigger(deps DiscussTriggerDeps) *DiscussTrigger {
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	parentCtx, parentCancel := context.WithCancel(context.Background())
	return &DiscussTrigger{
		deps:         deps,
		sessions:     make(map[string]*discussSession),
		logger:       logger.With(slog.String("service", "discuss_trigger")),
		parentCtx:    parentCtx,
		parentCancel: parentCancel,
	}
}

// ---------------------------------------------------------------------------
// Dependency setters (break DI cycles)
// ---------------------------------------------------------------------------

func (d *DiscussTrigger) SetResolver(r RunConfigResolver)            { d.deps.Resolver = r }
func (d *DiscussTrigger) SetChatRunner(r DiscussChatRunner)          { d.deps.ChatRunner = r }
func (d *DiscussTrigger) SetBroadcaster(b DiscussStreamBroadcaster)  { d.deps.Broadcaster = b }
func (d *DiscussTrigger) SetStreamObserver(o channel.StreamObserver) { d.deps.StreamObserver = o }
func (d *DiscussTrigger) SetChannelSender(s DiscussChannelSender)    { d.deps.ChannelSender = s }
func (d *DiscussTrigger) SetReactor(r DiscussReactor)                { d.deps.Reactor = r }
func (d *DiscussTrigger) SetDispatcher(disp DiscussDispatcher)       { d.deps.Dispatcher = disp }

// ---------------------------------------------------------------------------
// Session lifecycle
// ---------------------------------------------------------------------------

// NotifyRC pushes a new RenderedContext to the discuss session.
// If the session goroutine is not running, it starts one.
func (d *DiscussTrigger) NotifyRC(ctx context.Context, sessionID string, rc RenderedContext, config DiscussSessionConfig) {
	isNew := false
	d.mu.Lock()
	sess, ok := d.sessions[sessionID]
	if !ok {
		isNew = true
		sessCtx, cancel := context.WithCancel(d.parentCtx) //nolint:gosec // G118: cancel is stored in sess.cancel
		// Initialize lastProcessedMs so the new session does not re-detect
		// old @mentions or count all historical segments as "new" messages.
		// We subtract 1ms from the latest segment timestamp: this excludes
		// all truly-historical segments while still including the triggering
		// note itself (the one that caused this session creation). Without
		// the -1, a real @mention arriving as the latest segment would be
		// missed because wasRecentlyMentioned uses strict ">".
		latestMs := latestRCReceivedAtMs(rc)
		initialProcessedMs := latestMs
		if latestMs > 0 {
			initialProcessedMs = latestMs - 1
		}
		sess = &discussSession{
			config:          config,
			rcCh:            make(chan RenderedContext, 16),
			stopCh:          make(chan struct{}),
			cancel:          cancel,
			lastProcessedMs: initialProcessedMs,
		}
		d.wireSmartTiming(ctx, sess, config.BotID)
		d.sessions[sessionID] = sess
		go d.runSession(sessCtx, sess) //nolint:contextcheck // long-lived goroutine
	} else if config.ReplyTarget != "" {
		// Update ReplyTarget to track the latest inbound message's note ID.
		// This ensures fallback reply mechanisms target the most recent message.
		sess.configMu.Lock()
		sess.config.ReplyTarget = config.ReplyTarget
		sess.configMu.Unlock()
	}
	d.mu.Unlock()

	d.logger.Info("discuss: NotifyRC received",
		slog.String("session_id", sessionID),
		slog.String("bot_id", config.BotID),
		slog.String("platform", config.CurrentPlatform),
		slog.Bool("new_session", isNew),
		slog.Int("rc_segments", len(rc)),
		slog.Int("rc_channel_depth", len(sess.rcCh)),
	)

	// Attempt interrupt if the session is actively generating a response.
	if ok && sess.interrupt != nil && sess.chatTimingCfg != nil && sess.chatTimingCfg.Enabled {
		if sess.interrupt.RequestInterrupt() {
			d.logger.Info("chat timing: planner interrupt triggered",
				slog.String("session_id", sessionID))
		}
	}

	// Use mutex to make the drop-oldest operation atomic, preventing
	// concurrent callers from both draining the channel and losing RCs.
	sess.rcMu.Lock()
	select {
	case sess.rcCh <- rc:
	default:
		// Channel full: drop oldest, push newest.
		select {
		case <-sess.rcCh:
		default:
		}
		sess.rcCh <- rc
	}
	sess.rcMu.Unlock()
}

// StopSession stops a single discuss session goroutine.
func (d *DiscussTrigger) StopSession(sessionID string) {
	d.mu.Lock()
	sess, ok := d.sessions[sessionID]
	if ok {
		sess.cancel()
		sess.stopOnce.Do(func() { close(sess.stopCh) })
		if sess.debounce != nil {
			sess.debounce.Stop()
		}
		delete(d.sessions, sessionID)
	}
	d.mu.Unlock()
}

// StopAll stops all discuss session goroutines and cancels the parent context.
func (d *DiscussTrigger) StopAll() {
	d.mu.Lock()
	for id, sess := range d.sessions {
		sess.cancel()
		sess.stopOnce.Do(func() { close(sess.stopCh) })
		if sess.debounce != nil {
			sess.debounce.Stop()
		}
		delete(d.sessions, id)
	}
	d.mu.Unlock()
	d.parentCancel()
}

// HasSession returns true if a discuss session goroutine is running.
func (d *DiscussTrigger) HasSession(sessionID string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, ok := d.sessions[sessionID]
	return ok
}

// ---------------------------------------------------------------------------
// Session goroutine — accumulate + debounce + timing gate
// ---------------------------------------------------------------------------

const discussIdleTimeout = 10 * time.Minute

// discussWatchdogTimeout is the maximum lifetime of a session goroutine.
// It acts as a safety net in case the idle timer fails to fire due to
// runtime edge cases (e.g. GC pressure, scheduling delays).
const discussWatchdogTimeout = discussIdleTimeout*2 + 1*time.Minute // ~21 minutes

func (d *DiscussTrigger) runSession(ctx context.Context, sess *discussSession) {
	sessionID := sess.config.SessionID
	log := d.logger.With(slog.String("session_id", sessionID), slog.String("bot_id", sess.config.BotID))
	log.Info("discuss session started")

	startTime := time.Now()
	var messagesProcessed int

	// Watchdog: set a hard deadline on the session context as a backup exit mechanism.
	// Even if the idle timer fails to fire, this guarantees the goroutine exits.
	ctx, watchdogCancel := context.WithTimeout(ctx, discussWatchdogTimeout)
	defer watchdogCancel()

	defer func() {
		elapsed := time.Since(startTime)
		log.Info("discuss session stopped",
			slog.Duration("alive_duration", elapsed),
			slog.Int("messages_processed", messagesProcessed),
		)
		d.mu.Lock()
		if cur, ok := d.sessions[sessionID]; ok && cur == sess {
			delete(d.sessions, sessionID)
		}
		d.mu.Unlock()
	}()

	// Use AfterFunc for idle detection — avoids time.Timer.Reset races.
	idleTimeout := discussIdleTimeout
	if sess.idleTimeout > 0 {
		idleTimeout = sess.idleTimeout
	}
	idleCh := make(chan time.Time, 1)
	idleTimer := time.AfterFunc(idleTimeout, func() {
		select {
		case idleCh <- time.Now():
		default:
		}
	})
	defer idleTimer.Stop()

	resetIdle := func() {
		idleTimer.Reset(idleTimeout)
	}

	var latestRC RenderedContext

	for {
		select {
		case <-sess.stopCh:
			return
		case <-ctx.Done():
			log.Warn("discuss session watchdog timeout, forcing exit",
				slog.Duration("alive_duration", time.Since(startTime)),
				slog.Int("messages_processed", messagesProcessed),
				slog.Time("last_agent_call_at", sess.lastAgentCallAt),
			)
			return
		case <-idleCh:
			log.Info("discuss session idle timeout, exiting",
				slog.Duration("alive_duration", time.Since(startTime)),
				slog.Int("messages_processed", messagesProcessed),
			)
			return
		case rc := <-sess.rcCh:
			latestRC = rc
			messagesProcessed++
			log.Info("discuss: received new RC in session loop",
				slog.Int("rc_segments", len(rc)),
				slog.Int("queue_depth", len(sess.rcCh)))
		}

		// Smart timing: debounce — wait for quiet period before processing.
		// @-mentions bypass debounce so users get immediate responses.
		if sess.debounce != nil && !wasRecentlyMentioned(latestRC, sess.lastProcessedMs) {
			log.Info("discuss_step: debounce wait start",
				slog.String("step", "debounce_wait_start"))
			sess.debounce.Reset()
			if err := sess.debounce.Wait(ctx); err != nil {
				log.Info("discuss_step: debounce wait interrupted",
					slog.String("step", "debounce_wait_interrupted"),
					slog.Any("error", err))
				continue
			}
			log.Info("discuss_step: debounce wait completed",
				slog.String("step", "debounce_wait_end"))
		}

		// Drain any additional RCs that arrived during the debounce window.
		drainCount := 0
	drain:
		for {
			select {
			case rc := <-sess.rcCh:
				latestRC = rc
				drainCount++
				if sess.debounce != nil {
					sess.debounce.Reset()
				}
			default:
				break drain
			}
		}
		if drainCount > 0 {
			log.Info("discuss_step: drained additional RCs after debounce",
				slog.String("step", "post_debounce_drain"),
				slog.Int("drain_count", drainCount))
		}

		if len(latestRC) == 0 {
			continue
		}
		if LatestExternalEventMs(latestRC, sess.lastProcessedMs) == 0 {
			continue
		}

		// Compute message inter-arrival intervals for idle compensation.
		if sess.idleCompensate != nil {
			sess.msgIntervals = computeMsgIntervals(latestRC, sess.lastProcessedMs)
		}

		// Run handleReply in a goroutine so the session loop can still respond
		// to watchdog/stop signals even if handleReply blocks on an operation
		// that doesn't respect context cancellation (e.g. sync.Mutex.Lock).
		// This is a defensive measure — all current code paths in handleReply
		// should respect ctx, but this guarantees the session always exits.
		//
		// We create a per-call cancellable context so that if the session exits
		// via stopCh (while the watchdog ctx is still alive), we can immediately
		// cancel the handleReply goroutine instead of letting it run until the
		// watchdog timeout (~21 min), which would be a goroutine leak.
		log.Info("discuss_step: dispatching handleReply goroutine",
			slog.String("step", "handleReply_dispatch"),
			slog.Int("messages_processed", messagesProcessed),
			slog.Int("rc_segments", len(latestRC)))
		replyCtx, replyCancel := context.WithCancel(ctx)
		replyDone := make(chan bool, 1)
		go func() {
			replyDone <- d.handleReply(replyCtx, sess, latestRC, log)
		}()

		select {
		case meaningful := <-replyDone:
			replyCancel()
			// Only reset idle timer when handleReply triggers a meaningful interaction
			// (i.e. the agent was actually called). When timing gate returns no_reply
			// or threshold is not met, the session should still be allowed to idle out.
			if meaningful {
				resetIdle()
			}
		case <-ctx.Done():
			replyCancel()
			log.Warn("discuss session watchdog timeout, forcing exit (handleReply in-flight)",
				slog.Duration("alive_duration", time.Since(startTime)),
				slog.Int("messages_processed", messagesProcessed),
			)
			return
		case <-sess.stopCh:
			replyCancel()
			log.Info("discuss session stop requested, cancelling in-flight handleReply")
			return
		}
	}
}

// ---------------------------------------------------------------------------
// Timing strategy — decides whether to trigger the LLM
// ---------------------------------------------------------------------------

// handleReply evaluates whether to trigger the LLM agent. Returns true if the
// agent was actually called (meaningful interaction), false if the message was
// skipped (threshold not met, timing gate no_reply, cooldown, etc.).
// The caller uses this to decide whether to reset the idle timer.
func (d *DiscussTrigger) handleReply(ctx context.Context, sess *discussSession, rc RenderedContext, log *slog.Logger) bool {
	isMentioned := wasRecentlyMentioned(rc, sess.lastProcessedMs)
	newMsgCount := countNewMessages(rc, sess.lastProcessedMs)
	handleReplyStart := time.Now()

	log.Info("discuss_step: handleReply entered",
		slog.String("step", "handleReply_enter"),
		slog.String("session_id", sess.config.SessionID),
		slog.Bool("is_mentioned", isMentioned),
		slog.Int("new_msg_count", newMsgCount),
		slog.Bool("timing_enabled", sess.chatTimingCfg != nil && sess.chatTimingCfg.Enabled),
	)

	// Minimum cooldown between agent calls (mentions bypass).
	const minAgentCooldown = 15 * time.Second
	if !isMentioned && !sess.lastAgentCallAt.IsZero() {
		if elapsed := time.Since(sess.lastAgentCallAt); elapsed < minAgentCooldown {
			log.Info("discuss_step: handleReply exit (cooldown)",
				slog.String("step", "handleReply_exit_cooldown"),
				slog.Duration("elapsed", elapsed),
				slog.Duration("cooldown", minAgentCooldown),
				slog.Duration("step_duration", time.Since(handleReplyStart)))
			return false
		}
	}

	// Smart timing: talk_value threshold check.
	if sess.chatTimingCfg != nil && sess.chatTimingCfg.Enabled && !isMentioned {
		threshold := sess.chatTimingCfg.TriggerThreshold()

		if newMsgCount < threshold && sess.idleCompensate != nil {
			lastMsgMs := LatestExternalEventMs(rc, 0)
			if lastMsgMs > 0 {
				idleDuration := time.Since(time.UnixMilli(lastMsgMs))
				credit := sess.idleCompensate.ComputeCredit(idleDuration, chattiming.ComputeCreditRateFromIntervals(sess.msgIntervals))
				if credit > 0 {
					log.Debug("chat timing: idle compensation applied",
						slog.Int("credit", credit), slog.Duration("idle", idleDuration))
					newMsgCount += credit
				}
			}
		}

		if newMsgCount < threshold {
			log.Info("discuss_step: handleReply exit (threshold not met)",
				slog.String("step", "handleReply_exit_threshold"),
				slog.Int("new_messages", newMsgCount),
				slog.Int("threshold", threshold),
				slog.Duration("step_duration", time.Since(handleReplyStart)))
			d.extractPassiveMemory(ctx, sess, rc, log)
			return false
		}
	}

	// Smart timing: timing gate — lightweight LLM check before full agent call.
	if sess.timingGate != nil && sess.chatTimingCfg.TimingGate && !isMentioned {
		log.Info("discuss_step: entering timing gate",
			slog.String("step", "timing_gate_enter"),
			slog.Duration("step_duration", time.Since(handleReplyStart)))
		if d.evaluateTimingGate(ctx, sess, rc, newMsgCount, isMentioned, log) {
			log.Info("discuss_step: handleReply exit (timing gate no_reply/wait)",
				slog.String("step", "handleReply_exit_timing_gate"),
				slog.Duration("step_duration", time.Since(handleReplyStart)))
			return false // gate decided no_reply or wait-then-return
		}
	}

	// Guard: if context was cancelled (e.g. watchdog timeout) during timing gate
	// evaluation or earlier steps, abort rather than wasting an agent call that
	// would immediately fail with "context deadline exceeded".
	if ctx.Err() != nil {
		log.Info("discuss_step: handleReply exit (context cancelled before agent call)",
			slog.String("step", "handleReply_exit_ctx_cancelled"),
			slog.Any("reason", ctx.Err()),
			slog.Duration("step_duration", time.Since(handleReplyStart)))
		return false
	}

	log.Info("discuss_step: entering agent call",
		slog.String("step", "agent_call_enter"),
		slog.Duration("step_duration", time.Since(handleReplyStart)))
	d.handleReplyWithAgent(ctx, sess, rc, log)
	log.Info("discuss_step: handleReply exit (agent call completed)",
		slog.String("step", "handleReply_exit_agent_done"),
		slog.Duration("step_duration", time.Since(handleReplyStart)))
	return true
}

// evaluateTimingGate runs the lightweight LLM timing gate. Returns true if the
// caller should NOT proceed to the full agent call (no_reply or wait handled).
func (d *DiscussTrigger) evaluateTimingGate(ctx context.Context, sess *discussSession, rc RenderedContext, newMsgCount int, isMentioned bool, log *slog.Logger) bool {
	gateStart := time.Now()
	log.Info("discuss_step: timing gate evaluate start",
		slog.String("step", "timing_gate_evaluate_start"),
		slog.Int("new_msg_count", newMsgCount))

	lastMsgMs := LatestExternalEventMs(rc, 0)
	var timeSinceLast float64
	if lastMsgMs > 0 {
		timeSinceLast = time.Since(time.UnixMilli(lastMsgMs)).Seconds()
	}
	params := chattiming.TimingGateParams{
		RenderedContextXML:      renderContextXML(rc, sess.lastProcessedMs),
		IsMentioned:             isMentioned,
		NewMessageCount:         newMsgCount,
		TimeSinceLastMessageSec: timeSinceLast,
		TalkValue:               sess.chatTimingCfg.EffectiveTalkValue(),
		BotName:                 sess.config.ConversationName,
	}

	probeCfg := d.resolveProbeConfig(ctx, sess)
	result := sess.timingGate.Evaluate(ctx, params, probeCfg)

	log.Info("discuss_step: timing gate evaluate completed",
		slog.String("step", "timing_gate_evaluate_end"),
		slog.String("decision", string(result.Decision)),
		slog.String("reason", result.Reason),
		slog.Int("wait_seconds", result.WaitSeconds),
		slog.Duration("gate_duration", time.Since(gateStart)))

	switch result.Decision {
	case chattiming.TimingNoReply:
		log.Info("chat timing: gate decided no_reply", slog.String("reason", result.Reason))
		sess.lastProcessedMs = time.Now().UnixMilli()
		d.extractPassiveMemory(ctx, sess, rc, log)
		return true
	case chattiming.TimingWait:
		log.Info("discuss_step: timing gate wait start",
			slog.String("step", "timing_gate_wait_start"),
			slog.Int("wait_seconds", result.WaitSeconds),
			slog.String("reason", result.Reason))
		select {
		case <-time.After(time.Duration(result.WaitSeconds) * time.Second):
			log.Info("discuss_step: timing gate wait completed",
				slog.String("step", "timing_gate_wait_end"),
				slog.Duration("total_gate_duration", time.Since(gateStart)))
		case <-ctx.Done():
			log.Info("discuss_step: timing gate wait cancelled by context",
				slog.String("step", "timing_gate_wait_cancelled"),
				slog.Duration("total_gate_duration", time.Since(gateStart)))
			return true
		}
	}
	return false
}

// resolveProbeConfig returns a lightweight RunConfig for the timing gate probe.
func (d *DiscussTrigger) resolveProbeConfig(ctx context.Context, sess *discussSession) agentpkg.RunConfig {
	if sess.cachedProbeModel != nil {
		return *sess.cachedProbeModel
	}
	probeCfg := agentpkg.RunConfig{SupportsToolCall: false}
	if d.deps.Resolver != nil {
		sess.configMu.RLock()
		botID, sessionID, channelID := sess.config.BotID, sess.config.SessionID, sess.config.ChannelIdentityID
		platform, replyTarget := sess.config.CurrentPlatform, sess.config.ReplyTarget
		convType, token := sess.config.ConversationType, sess.config.SessionToken
		sess.configMu.RUnlock()
		resolved, err := d.deps.Resolver.ResolveRunConfig(ctx,
			botID, sessionID, channelID,
			platform, replyTarget,
			convType, token)
		if err == nil {
			probeCfg = resolved.RunConfig
			sess.cachedProbeModel = &agentpkg.RunConfig{Model: resolved.RunConfig.Model}
		}
	}
	return probeCfg
}

// ---------------------------------------------------------------------------
// Agent execution — delegates to ChatRunner (flow.Resolver.StreamChat)
// ---------------------------------------------------------------------------

func (d *DiscussTrigger) handleReplyWithAgent(ctx context.Context, sess *discussSession, rc RenderedContext, log *slog.Logger) {
	// Hard deadline so the session loop always returns to select, even if the
	// LLM provider or outbound send hangs indefinitely.
	const maxAgentCallDuration = 5 * time.Minute
	agentCallStart := time.Now()
	var cancel context.CancelFunc
	ctx, cancel = context.WithTimeout(ctx, maxAgentCallDuration)
	defer func() {
		cancel()
		elapsed := time.Since(agentCallStart)
		log.Info("discuss_step: handleReplyWithAgent exit",
			slog.String("step", "agent_call_exit"),
			slog.Duration("agent_call_duration", elapsed),
			slog.Bool("timeout", ctx.Err() == context.DeadlineExceeded),
		)
		if ctx.Err() == context.DeadlineExceeded {
			log.Error("discuss: handleReplyWithAgent hard timeout triggered",
				slog.Duration("elapsed", elapsed),
				slog.Duration("max_duration", maxAgentCallDuration),
				slog.Time("last_agent_call_at", sess.lastAgentCallAt),
				slog.String("session_id", sess.config.SessionID),
			)
		}
	}()

	sess.configMu.RLock()
	cfg := sess.config
	sess.configMu.RUnlock()
	sess.lastAgentCallAt = time.Now()
	isMentioned := wasRecentlyMentioned(rc, sess.lastProcessedMs)

	// Update ReplyTarget to the best candidate from the current RC.
	// Prefer the latest mentions_me/replies_to_me segment's target; fall back
	// to the latest non-self segment's target.
	if bestTarget := latestReplyTarget(rc, sess.lastProcessedMs); bestTarget != "" {
		cfg.ReplyTarget = bestTarget
		sess.configMu.Lock()
		sess.config.ReplyTarget = bestTarget
		sess.configMu.Unlock()
	}

	// Mark route active in the dispatcher for inject/queue support.
	routeID := cfg.RouteID
	var injectCh <-chan conversation.InjectMessage
	if d.deps.Dispatcher != nil && routeID != "" {
		log.Info("discuss_step: marking route active",
			slog.String("step", "route_mark_active"),
			slog.String("route_id", routeID))
		injectCh = d.deps.Dispatcher.MarkActive(routeID)
		defer d.drainDiscussQueue(ctx, routeID, log)
	}

	// Smart timing: interrupt-retry loop.
	maxRounds := 1
	if sess.interrupt != nil {
		sess.interrupt.ResetRounds()
		maxRounds = 7
	}

	for round := 0; round < maxRounds; round++ {
		var agentCtx context.Context
		var agentCancel context.CancelFunc

		if sess.interrupt != nil {
			log.Info("discuss_step: binding interrupt controller",
				slog.String("step", "interrupt_bind"),
				slog.Int("round", round))
			agentCtx, agentCancel = sess.interrupt.Bind(ctx)
		} else {
			agentCtx = ctx
			agentCancel = func() {}
		}

		if d.deps.ChatRunner == nil {
			log.Error("discuss trigger: chat runner not configured")
			agentCancel()
			if sess.interrupt != nil {
				sess.interrupt.Unbind(true)
			}
			// Advance cursor to the latest RC segment consumed, not wall-clock.
			consumedMs := latestRCReceivedAtMs(rc)
			if consumedMs > sess.lastProcessedMs {
				sess.lastProcessedMs = consumedMs
			}
			return
		}

		log.Info("triggering discuss LLM call via StreamChat",
			slog.Int("round", round), slog.String("session_id", cfg.SessionID))

		chatReq := conversation.ChatRequest{
			BotID:                    cfg.BotID,
			ChatID:                   cfg.BotID,
			SessionID:                cfg.SessionID,
			SourceChannelIdentityID:  cfg.ChannelIdentityID,
			ReplyTarget:              cfg.ReplyTarget,
			IsMentioned:              isMentioned,
			CurrentChannel:           cfg.CurrentPlatform,
			ConversationType:         cfg.ConversationType,
			ConversationName:         cfg.ConversationName,
			ChatToken:                cfg.SessionToken,
			SessionType:              sessionpkg.TypeDiscuss,
			UserMessagePersisted:     true,
			DiscussLateBindingPrompt: buildLateBindingPrompt(isMentioned),
			InjectCh:                 injectCh,
		}

		log.Info("discuss: dispatching StreamChat",
			slog.String("bot_id", cfg.BotID),
			slog.String("session_id", cfg.SessionID),
			slog.String("platform", cfg.CurrentPlatform),
			slog.String("reply_target", cfg.ReplyTarget),
			slog.Bool("is_mentioned", isMentioned),
			slog.Int("inject_ch_available", func() int {
				if injectCh != nil {
					return 1
				}
				return 0
			}()),
			slog.Int("late_binding_prompt_len", len(chatReq.DiscussLateBindingPrompt)),
		)

		log.Info("discuss_step: StreamChat dispatched, consuming stream",
			slog.String("step", "stream_consume_start"),
			slog.Int("round", round),
			slog.Duration("elapsed_since_agent_start", time.Since(agentCallStart)))

		chunkCh, errCh := d.deps.ChatRunner.StreamChat(agentCtx, chatReq)

		outStream := d.openOutboundStream(agentCtx, cfg, log)
		hadOutput, finalMessages, streamErr := d.consumeStream(agentCtx, cfg, chunkCh, errCh, outStream, log)
		d.finalizeOutboundStream(agentCtx, ctx, cfg, outStream, finalMessages, log)

		log.Info("discuss_step: stream consumption completed",
			slog.String("step", "stream_consume_end"),
			slog.Int("round", round),
			slog.Bool("had_output", hadOutput),
			slog.Bool("has_error", streamErr != nil),
			slog.Duration("elapsed_since_agent_start", time.Since(agentCallStart)))

		agentCancel()

		if streamErr != nil {
			log.Error("discuss stream error", slog.Any("error", streamErr))
		}

		// Detect empty response (no output, no error, no interrupt) — likely
		// caused by context window overflow. Log a detailed warning for
		// observability so operators can identify and tune the context budget.
		if !hadOutput && streamErr == nil {
			log.Warn("discuss: agent produced empty response (possible context overflow)",
				slog.String("bot_id", cfg.BotID),
				slog.String("session_id", cfg.SessionID),
				slog.Int("rc_segments", len(rc)),
				slog.Int("round", round),
				slog.Duration("elapsed", time.Since(agentCallStart)),
			)
		}

		// Check interrupt state.
		wasInterrupted := false
		if sess.interrupt != nil {
			wasInterrupted = sess.interrupt.ConsumeInterrupted()
			sess.interrupt.Unbind(!wasInterrupted)
		}

		if wasInterrupted && sess.interrupt != nil && sess.interrupt.CanRetry() {
			if hadOutput {
				log.Info("discuss_step: interrupted but had output, no retry",
					slog.String("step", "interrupt_skip_retry"),
					slog.Int("round", round))
			} else {
				log.Info("discuss_step: interrupted, waiting for quiet period before retry",
					slog.String("step", "interrupt_retry_debounce"),
					slog.Int("round", round))
				if sess.debounce != nil {
					sess.debounce.Reset()
					_ = sess.debounce.Wait(ctx)
				}
				// Drain new RCs that arrived during the interrupted call.
				for {
					select {
					case newRC := <-sess.rcCh:
						rc = newRC
						_ = rc
					default:
						goto retryDone
					}
				}
			retryDone:
				continue
			}
		}

		// Advance cursor to the latest RC segment consumed, not wall-clock.
		// Messages arriving DURING LLM generation will have ReceivedAtMs >
		// this cursor and correctly trigger another round.
		consumedMs := latestRCReceivedAtMs(rc)
		if consumedMs > sess.lastProcessedMs {
			sess.lastProcessedMs = consumedMs
		}
		return
	}
}

// ---------------------------------------------------------------------------
// Outbound stream helpers
// ---------------------------------------------------------------------------

func (d *DiscussTrigger) openOutboundStream(ctx context.Context, cfg DiscussSessionConfig, log *slog.Logger) channel.OutboundStream {
	if d.deps.ChannelSender == nil || cfg.CurrentPlatform == "" || cfg.ReplyTarget == "" {
		return nil
	}
	sender, err := d.deps.ChannelSender.GetReplySender(cfg.BotID, channel.ChannelType(cfg.CurrentPlatform))
	if err != nil || sender == nil {
		return nil
	}
	s, err := sender.OpenStream(ctx, cfg.ReplyTarget, channel.StreamOptions{})
	if err != nil {
		log.Warn("discuss: failed to open outbound stream", slog.Any("error", err))
		return nil
	}
	if d.deps.StreamObserver != nil {
		s = channel.NewTeeStream(s, d.deps.StreamObserver, cfg.BotID, channel.ChannelType(cfg.CurrentPlatform))
	}
	return s
}

func (d *DiscussTrigger) consumeStream(
	ctx context.Context,
	cfg DiscussSessionConfig,
	chunkCh <-chan conversation.StreamChunk,
	errCh <-chan error,
	outStream channel.OutboundStream,
	log *slog.Logger,
) (hadOutput bool, finalMessages []conversation.ModelMessage, streamErr error) {
	var textBuf strings.Builder
	firstChunkReceived := false
	streamStart := time.Now()
	for chunkCh != nil || errCh != nil {
		select {
		case <-ctx.Done():
			// Context cancelled (interrupt, timeout, or watchdog). Stop consuming
			// immediately rather than relying on upstream to close channels.
			log.Info("discuss: consumeStream context cancelled, stopping",
				slog.Any("reason", ctx.Err()))
			return
		case chunk, ok := <-chunkCh:
			if !ok {
				chunkCh = nil
				log.Info("discuss_step: stream chunkCh closed",
					slog.String("step", "stream_chunk_ch_closed"),
					slog.Duration("stream_duration", time.Since(streamStart)))
				continue
			}
			if !firstChunkReceived {
				firstChunkReceived = true
				log.Info("discuss_step: first stream chunk received",
					slog.String("step", "stream_first_chunk"),
					slog.Duration("time_to_first_chunk", time.Since(streamStart)))
			}
			if len(chunk) == 0 || d.deps.StreamChunkParser == nil {
				continue
			}
			events, messages, parseErr := d.deps.StreamChunkParser(chunk)
			if parseErr != nil {
				log.Warn("discuss: stream chunk parse failed", slog.Any("error", parseErr))
				continue
			}
			if len(messages) > 0 {
				finalMessages = messages
			}
			for _, event := range events {
				hadOutput = true
				if event.Type == channel.StreamEventReaction && len(event.Reactions) > 0 {
					d.dispatchDiscussReactions(ctx, cfg, event.Reactions, log)
					continue
				}
				// Log tool-call and send-related events for debugging.
				switch {
				case event.Type == channel.StreamEventToolCallStart && event.ToolCall != nil:
					log.Info("discuss: tool call detected in stream",
						slog.String("tool_name", event.ToolCall.Name),
						slog.String("tool_call_id", event.ToolCall.CallID))
				case event.Type == channel.StreamEventToolCallEnd && event.ToolCall != nil && event.ToolCall.Name == "send":
					log.Info("discuss: send tool completed in stream",
						slog.String("tool_call_id", event.ToolCall.CallID),
						slog.Bool("has_error", event.Error != ""))
				case event.Type == channel.StreamEventAttachment:
					log.Info("discuss: attachment event in stream",
						slog.Int("attachment_count", len(event.Attachments)))
				}
				// In discuss mode, text deltas are internal monologue —
				// only the send/reply tool delivers visible messages.
				// Skip text-phase deltas to prevent leaking monologue to the channel.
				if event.Type == channel.StreamEventDelta && event.Phase == channel.StreamPhaseText {
					textBuf.WriteString(event.Delta)
					continue
				}
				if outStream != nil {
					if pushErr := outStream.Push(ctx, event); pushErr != nil {
						log.Warn("discuss: outbound stream push failed",
							slog.String("event_type", string(event.Type)), slog.Any("error", pushErr))
					}
				}
				if d.deps.Broadcaster != nil {
					broadcastEvent := event
					// Reasoning content may contain raw XML tags (e.g. <parameter>,
					// <function_call>) that the LLM emits as plain text inside reasoning
					// deltas. Strip them before broadcasting to the UI to prevent tag
					// artefacts from leaking into the user-visible thinking display.
					if event.Phase == channel.StreamPhaseReasoning && event.Delta != "" {
						broadcastEvent.Delta = channel.FilterThinkingTags(broadcastEvent.Delta)
						broadcastEvent.Delta = channel.FilterReasoningArray(broadcastEvent.Delta)
						broadcastEvent.Delta = channel.FilterToolCallXML(broadcastEvent.Delta)
					}
					d.deps.Broadcaster.PublishEvent(cfg.BotID, broadcastEvent)
				}
			}
		case err, ok := <-errCh:
			if !ok {
				errCh = nil
				continue
			}
			if err != nil {
				streamErr = err
			}
		}
		if streamErr != nil {
			break
		}
	}

	return
}

func (*DiscussTrigger) finalizeOutboundStream(
	_, parentCtx context.Context,
	_ DiscussSessionConfig,
	outStream channel.OutboundStream,
	_ []conversation.ModelMessage,
	log *slog.Logger,
) {
	if outStream == nil {
		return
	}
	// In discuss mode, pure text assistant output is internal monologue —
	// only the send/reply tool delivers visible messages via SendDirect.
	// Skip pushing assistant outputs to the outbound stream entirely.
	if closeErr := outStream.Close(parentCtx); closeErr != nil {
		log.Warn("discuss: outbound stream close failed", slog.Any("error", closeErr))
	}
}

// ---------------------------------------------------------------------------
// Dispatcher queue drain
// ---------------------------------------------------------------------------

func (d *DiscussTrigger) drainDiscussQueue(ctx context.Context, routeID string, log *slog.Logger) {
	if d.deps.Dispatcher == nil || routeID == "" {
		return
	}
	result := d.deps.Dispatcher.MarkDone(routeID)
	for _, notif := range result.QueuedNotifications {
		log.Info("discuss: replaying queued notification",
			slog.String("route_id", routeID), slog.String("session_id", notif.SessionID))
		d.NotifyRC(ctx, notif.SessionID, notif.RC, notif.Config)
	}
}

func (d *DiscussTrigger) dispatchDiscussReactions(ctx context.Context, cfg DiscussSessionConfig, reactions []channel.ReactRequest, log *slog.Logger) {
	if d.deps.Reactor == nil || cfg.CurrentPlatform == "" || cfg.ReplyTarget == "" {
		return
	}
	for _, r := range reactions {
		r.Target = cfg.ReplyTarget
		// In discuss mode, the stream-parsed ReactRequest has no MessageID
		// (it's lost in the agent→stream serialization layer). Use the session's
		// ReplyTarget (the latest triggering note ID) as the reaction target.
		if r.MessageID == "" {
			r.MessageID = cfg.ReplyTarget
		}
		if err := d.deps.Reactor.React(ctx, cfg.BotID, channel.ChannelType(cfg.CurrentPlatform), r); err != nil {
			log.Warn("discuss: reaction dispatch failed",
				slog.String("emoji", r.Emoji), slog.Any("error", err))
		}
	}
}

// ---------------------------------------------------------------------------
// Passive memory extraction
// ---------------------------------------------------------------------------

func (d *DiscussTrigger) extractPassiveMemory(_ context.Context, sess *discussSession, rc RenderedContext, log *slog.Logger) {
	if d.deps.MemoryFormation == nil && d.deps.ExpressionAccumulator == nil {
		return
	}
	var messages []adapters.Message
	for _, seg := range rc {
		if seg.ReceivedAtMs <= sess.lastProcessedMs || seg.IsMyself {
			continue
		}
		var textParts []string
		for _, piece := range seg.Content {
			if piece.Text != "" {
				textParts = append(textParts, piece.Text)
			}
		}
		if len(textParts) == 0 {
			continue
		}
		messages = append(messages, adapters.Message{
			Role:    "user",
			Content: strings.Join(textParts, "\n"),
		})
	}
	if len(messages) == 0 {
		return
	}

	// Passive memory formation.
	if d.deps.MemoryFormation != nil {
		// In group chats, messages come from multiple senders so we cannot
		// attribute them to a single UserID. Use the conversation name as
		// DisplayName so stored memories are at least linked to the group.
		displayName := strings.TrimSpace(sess.config.ConversationName)
		req := adapters.AfterChatRequest{
			BotID:             sess.config.BotID,
			Messages:          messages,
			ChannelIdentityID: sess.config.ChannelIdentityID,
			DisplayName:       displayName,
			SourcePlatform:    sess.config.CurrentPlatform,
			SourceSessionID:   sess.config.SessionID,
		}
		// Goroutine leak protection: the goroutine is bounded by a 2-minute
		// timeout AND inherits d.parentCtx which is cancelled by StopAll(),
		// ensuring timely exit on shutdown.
		go func(parentCtx context.Context) { //nolint:contextcheck // intentionally detached from request context
			memCtx, memCancel := context.WithTimeout(parentCtx, 2*time.Minute)
			defer memCancel()
			if err := d.deps.MemoryFormation.OnAfterChat(memCtx, req); err != nil {
				log.Warn("passive memory extraction failed", slog.Any("error", err))
			}
		}(d.parentCtx)
	}

	// Expression/jargon learning — accumulate messages for offline extraction.
	// Run in a goroutine to prevent blocking the session loop. The underlying
	// Learner.Accumulate() may block on a mutex held by a long-running
	// LearnFromHistory LLM call, which would deadlock the session.
	if d.deps.ExpressionAccumulator != nil {
		go func(parentCtx context.Context) { //nolint:contextcheck // intentionally detached from request context
			exprCtx, exprCancel := context.WithTimeout(parentCtx, 2*time.Minute)
			defer exprCancel()
			d.deps.ExpressionAccumulator(exprCtx, sess.config.BotID, sess.config.SessionID, messages)
		}(d.parentCtx)
	}
}

// ---------------------------------------------------------------------------
// Smart timing wiring
// ---------------------------------------------------------------------------

func (d *DiscussTrigger) wireSmartTiming(ctx context.Context, sess *discussSession, botID string) {
	if d.deps.ChatTimingService == nil {
		return
	}
	cfg := chattiming.DefaultConfig()
	if d.deps.SettingsService != nil {
		if raw, err := d.deps.SettingsService.GetBotChatTiming(ctx, botID); err == nil && len(raw) > 0 {
			_ = json.Unmarshal(raw, &cfg)
		}
	}
	sess.chatTimingCfg = &cfg
	if cfg.Enabled {
		sess.debounce = d.deps.ChatTimingService.NewDebouncer(cfg)
		sess.interrupt = d.deps.ChatTimingService.NewInterruptController(cfg)
		sess.timingGate = d.deps.ChatTimingService.NewTimingGate()
		sess.idleCompensate = d.deps.ChatTimingService.NewIdleCompensator(cfg)
	}
}
