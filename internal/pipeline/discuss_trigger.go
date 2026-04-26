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
	d.mu.Lock()
	sess, ok := d.sessions[sessionID]
	if !ok {
		sessCtx, cancel := context.WithCancel(d.parentCtx) //nolint:gosec // G118: cancel is stored in sess.cancel
		sess = &discussSession{
			config: config,
			rcCh:   make(chan RenderedContext, 16),
			stopCh: make(chan struct{}),
			cancel: cancel,
		}
		d.wireSmartTiming(ctx, sess, config.BotID)
		d.sessions[sessionID] = sess
		go d.runSession(sessCtx, sess) //nolint:contextcheck // long-lived goroutine
	}
	d.mu.Unlock()

	// Attempt interrupt if the session is actively generating a response.
	if ok && sess.interrupt != nil && sess.chatTimingCfg != nil && sess.chatTimingCfg.Enabled {
		if sess.interrupt.RequestInterrupt() {
			d.logger.Info("chat timing: planner interrupt triggered",
				slog.String("session_id", sessionID))
		}
	}

	select {
	case sess.rcCh <- rc:
	default:
		// Drop oldest, push newest.
		select {
		case <-sess.rcCh:
		default:
		}
		select {
		case sess.rcCh <- rc:
		default:
		}
	}
}

// StopSession stops a single discuss session goroutine.
func (d *DiscussTrigger) StopSession(sessionID string) {
	d.mu.Lock()
	sess, ok := d.sessions[sessionID]
	if ok {
		sess.cancel()
		close(sess.stopCh)
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
		close(sess.stopCh)
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

func (d *DiscussTrigger) runSession(ctx context.Context, sess *discussSession) {
	sessionID := sess.config.SessionID
	log := d.logger.With(slog.String("session_id", sessionID), slog.String("bot_id", sess.config.BotID))
	log.Info("discuss session started")
	defer func() {
		log.Info("discuss session stopped")
		d.mu.Lock()
		if cur, ok := d.sessions[sessionID]; ok && cur == sess {
			delete(d.sessions, sessionID)
		}
		d.mu.Unlock()
	}()

	idle := time.NewTimer(discussIdleTimeout)
	defer idle.Stop()

	var latestRC RenderedContext

	for {
		select {
		case <-sess.stopCh:
			return
		case <-idle.C:
			log.Info("discuss session idle timeout, exiting")
			return
		case rc := <-sess.rcCh:
			latestRC = rc
			idle.Reset(discussIdleTimeout)
		}

		// Smart timing: debounce — wait for quiet period before processing.
		if sess.debounce != nil {
			sess.debounce.Reset()
			if err := sess.debounce.Wait(ctx); err != nil {
				continue
			}
		}

		// Drain any additional RCs that arrived during the debounce window.
	drain:
		for {
			select {
			case rc := <-sess.rcCh:
				latestRC = rc
				if sess.debounce != nil {
					sess.debounce.Reset()
				}
			default:
				break drain
			}
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

		d.handleReply(ctx, sess, latestRC, log)
	}
}

// ---------------------------------------------------------------------------
// Timing strategy — decides whether to trigger the LLM
// ---------------------------------------------------------------------------

func (d *DiscussTrigger) handleReply(ctx context.Context, sess *discussSession, rc RenderedContext, log *slog.Logger) {
	isMentioned := wasRecentlyMentioned(rc, sess.lastProcessedMs)
	newMsgCount := countNewMessages(rc, sess.lastProcessedMs)

	// Minimum cooldown between agent calls (mentions bypass).
	const minAgentCooldown = 15 * time.Second
	if !isMentioned && !sess.lastAgentCallAt.IsZero() {
		if elapsed := time.Since(sess.lastAgentCallAt); elapsed < minAgentCooldown {
			log.Debug("discuss: agent cooldown active, skipping",
				slog.Duration("elapsed", elapsed),
				slog.Duration("cooldown", minAgentCooldown))
			return
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
			log.Debug("chat timing: talk_value threshold not met",
				slog.Int("new_messages", newMsgCount), slog.Int("threshold", threshold))
			d.extractPassiveMemory(ctx, sess, rc, log)
			return
		}
	}

	// Smart timing: timing gate — lightweight LLM check before full agent call.
	if sess.timingGate != nil && sess.chatTimingCfg.TimingGate && !isMentioned {
		if d.evaluateTimingGate(ctx, sess, rc, newMsgCount, isMentioned, log) {
			return // gate decided no_reply or wait-then-return
		}
	}

	d.handleReplyWithAgent(ctx, sess, rc, log)
}

// evaluateTimingGate runs the lightweight LLM timing gate. Returns true if the
// caller should NOT proceed to the full agent call (no_reply or wait handled).
func (d *DiscussTrigger) evaluateTimingGate(ctx context.Context, sess *discussSession, rc RenderedContext, newMsgCount int, isMentioned bool, log *slog.Logger) bool {
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

	switch result.Decision {
	case chattiming.TimingNoReply:
		log.Info("chat timing: gate decided no_reply", slog.String("reason", result.Reason))
		sess.lastProcessedMs = time.Now().UnixMilli()
		d.extractPassiveMemory(ctx, sess, rc, log)
		return true
	case chattiming.TimingWait:
		log.Info("chat timing: gate decided wait",
			slog.Int("wait_seconds", result.WaitSeconds), slog.String("reason", result.Reason))
		select {
		case <-time.After(time.Duration(result.WaitSeconds) * time.Second):
		case <-ctx.Done():
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
		resolved, err := d.deps.Resolver.ResolveRunConfig(ctx,
			sess.config.BotID, sess.config.SessionID, sess.config.ChannelIdentityID,
			sess.config.CurrentPlatform, sess.config.ReplyTarget,
			sess.config.ConversationType, sess.config.SessionToken)
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
	cfg := sess.config
	sess.lastAgentCallAt = time.Now()
	isMentioned := wasRecentlyMentioned(rc, sess.lastProcessedMs)

	// Mark route active in the dispatcher for inject/queue support.
	routeID := cfg.RouteID
	var injectCh <-chan conversation.InjectMessage
	if d.deps.Dispatcher != nil && routeID != "" {
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
			CurrentChannel:           cfg.CurrentPlatform,
			ConversationType:         cfg.ConversationType,
			ConversationName:         cfg.ConversationName,
			ChatToken:                cfg.SessionToken,
			SessionType:              sessionpkg.TypeDiscuss,
			UserMessagePersisted:     true,
			DiscussLateBindingPrompt: buildLateBindingPrompt(isMentioned),
			InjectCh:                 injectCh,
		}

		chunkCh, errCh := d.deps.ChatRunner.StreamChat(agentCtx, chatReq)

		outStream := d.openOutboundStream(agentCtx, cfg, log)
		hadOutput, finalMessages, streamErr := d.consumeStream(agentCtx, cfg, chunkCh, errCh, outStream, log)
		d.finalizeOutboundStream(agentCtx, ctx, cfg, outStream, finalMessages, log)

		agentCancel()

		if streamErr != nil {
			log.Error("discuss stream error", slog.Any("error", streamErr))
		}

		// Check interrupt state.
		wasInterrupted := false
		if sess.interrupt != nil {
			wasInterrupted = sess.interrupt.ConsumeInterrupted()
			sess.interrupt.Unbind(!wasInterrupted)
		}

		if wasInterrupted && sess.interrupt != nil && sess.interrupt.CanRetry() {
			if hadOutput {
				log.Info("chat timing: agent interrupted but already produced output, skipping retry")
			} else {
				log.Info("chat timing: agent interrupted, waiting for quiet period before retry")
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
	for chunkCh != nil || errCh != nil {
		select {
		case chunk, ok := <-chunkCh:
			if !ok {
				chunkCh = nil
				continue
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
				// In discuss mode, text deltas are internal monologue —
				// only the send/reply tool delivers visible messages.
				// Skip text-phase deltas to prevent leaking monologue to the channel.
				if event.Type == channel.StreamEventDelta && event.Phase == channel.StreamPhaseText {
					continue
				}
				if outStream != nil {
					if pushErr := outStream.Push(ctx, event); pushErr != nil {
						log.Warn("discuss: outbound stream push failed",
							slog.String("event_type", string(event.Type)), slog.Any("error", pushErr))
					}
				}
				if d.deps.Broadcaster != nil {
					d.deps.Broadcaster.PublishEvent(cfg.BotID, event)
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
	if closeErr := outStream.Close(context.WithoutCancel(parentCtx)); closeErr != nil {
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

// ---------------------------------------------------------------------------
// Reactions
// ---------------------------------------------------------------------------

func (d *DiscussTrigger) dispatchDiscussReactions(ctx context.Context, cfg DiscussSessionConfig, reactions []channel.ReactRequest, log *slog.Logger) {
	if d.deps.Reactor == nil || cfg.CurrentPlatform == "" || cfg.ReplyTarget == "" {
		return
	}
	for _, r := range reactions {
		r.Target = cfg.ReplyTarget
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
		req := adapters.AfterChatRequest{
			BotID:             sess.config.BotID,
			Messages:          messages,
			ChannelIdentityID: sess.config.ChannelIdentityID,
		}
		go func(parentCtx context.Context) { //nolint:contextcheck // intentionally detached from request context
			memCtx, memCancel := context.WithTimeout(parentCtx, 2*time.Minute)
			defer memCancel()
			if err := d.deps.MemoryFormation.OnAfterChat(memCtx, req); err != nil {
				log.Warn("passive memory extraction failed", slog.Any("error", err))
			}
		}(d.parentCtx)
	}

	// Expression/jargon learning — accumulate messages for offline extraction.
	if d.deps.ExpressionAccumulator != nil {
		d.deps.ExpressionAccumulator(context.WithoutCancel(d.parentCtx), sess.config.BotID, sess.config.SessionID, messages) //nolint:contextcheck // intentionally detached from request context
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
