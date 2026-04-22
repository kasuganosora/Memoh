package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	sdk "github.com/memohai/twilight-ai/sdk"

	agentpkg "github.com/memohai/memoh/internal/agent"
	"github.com/memohai/memoh/internal/channel"
	"github.com/memohai/memoh/internal/chattiming"
	"github.com/memohai/memoh/internal/conversation"
	"github.com/memohai/memoh/internal/memory/adapters"
	messagepkg "github.com/memohai/memoh/internal/message"
	sessionpkg "github.com/memohai/memoh/internal/session"
)

// ResolveRunConfigResult holds the output of ResolveRunConfig.
type ResolveRunConfigResult struct {
	RunConfig agentpkg.RunConfig
	ModelID   string // database UUID of the selected model
}

// RunConfigResolver resolves a complete agent RunConfig and persists output
// rounds. Implemented by flow.Resolver.
type RunConfigResolver interface {
	ResolveRunConfig(ctx context.Context, botID, sessionID, channelIdentityID, currentPlatform, replyTarget, conversationType, chatToken string) (ResolveRunConfigResult, error)
	InlineImageAttachments(ctx context.Context, botID string, refs []ImageAttachmentRef) []sdk.ImagePart
	StoreRound(ctx context.Context, botID, sessionID, channelIdentityID, currentPlatform string, messages []sdk.Message, modelID string) error
}

// discussStreamer abstracts the agent streaming capability for testability.
type discussStreamer interface {
	Stream(ctx context.Context, cfg agentpkg.RunConfig) <-chan agentpkg.StreamEvent
}

// DiscussStreamBroadcaster publishes stream events to local UI subscribers.
// Implemented by local.RouteHub.
type DiscussStreamBroadcaster interface {
	PublishEvent(routeKey string, event channel.StreamEvent)
}

// DiscussDriverDeps holds dependencies injected into the DiscussDriver.
type DiscussDriverDeps struct {
	Pipeline       *Pipeline
	EventStore     *EventStore
	Agent          *agentpkg.Agent
	MessageService messagepkg.Service
	Resolver       RunConfigResolver
	Broadcaster    DiscussStreamBroadcaster
	Logger         *slog.Logger

	// ChatTimingService provides smart timing components. When nil, all timing
	// features are disabled and behavior is identical to the legacy implementation.
	ChatTimingService *chattiming.Service

	// SettingsService loads per-bot settings including chat_timing config.
	SettingsService settingsLoader

	// MemoryFormation extracts facts from conversations passively.
	// When nil, passive memory extraction is disabled.
	MemoryFormation memoryFormationRunner
}

// memoryFormationRunner runs the Extract -> Decide -> Apply pipeline on messages.
type memoryFormationRunner interface {
	OnAfterChat(ctx context.Context, req adapters.AfterChatRequest) error
}

// settingsLoader is a minimal interface for loading bot settings.
type settingsLoader interface {
	GetBotChatTiming(ctx context.Context, botID string) (json.RawMessage, error)
}

// DiscussSessionConfig holds per-session configuration for discuss mode.
type DiscussSessionConfig struct {
	BotID             string
	SessionID         string
	ChannelIdentityID string
	ReplyTarget       string
	CurrentPlatform   string
	ConversationType  string
	ConversationName  string
	SessionToken      string //nolint:gosec // session credential material
}

// DiscussDriver manages discuss-mode sessions. It is goroutine-safe.
type DiscussDriver struct {
	deps     DiscussDriverDeps
	mu       sync.Mutex
	sessions map[string]*discussSession
	logger   *slog.Logger
}

type discussSession struct {
	config          DiscussSessionConfig
	rcCh            chan RenderedContext
	stopCh          chan struct{}
	cancel          context.CancelFunc
	lastProcessedMs int64

	// Smart timing state (nil when feature disabled).
	chatTimingCfg    *chattiming.Config
	debounce         *chattiming.Debouncer
	interrupt        *chattiming.InterruptController
	timingGate       *chattiming.TimingGate
	idleCompensate   *chattiming.IdleCompensator
	cachedProbeModel *agentpkg.RunConfig // cached model config for timing gate probe
	msgIntervals     []time.Duration     // recent message inter-arrival times for idle compensation
}

// NewDiscussDriver creates a new DiscussDriver.
func NewDiscussDriver(deps DiscussDriverDeps) *DiscussDriver {
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &DiscussDriver{
		deps:     deps,
		sessions: make(map[string]*discussSession),
		logger:   logger.With(slog.String("service", "discuss_driver")),
	}
}

// SetResolver sets the RunConfigResolver after construction (breaks DI cycles).
func (d *DiscussDriver) SetResolver(r RunConfigResolver) {
	d.deps.Resolver = r
}

// SetBroadcaster sets the stream broadcaster after construction so that
// discuss-mode agent events are forwarded to the Web UI in real time.
func (d *DiscussDriver) SetBroadcaster(b DiscussStreamBroadcaster) {
	d.deps.Broadcaster = b
}

// NotifyRC pushes a new RenderedContext to the discuss session.
// If the session goroutine is not running, it starts one.
func (d *DiscussDriver) NotifyRC(ctx context.Context, sessionID string, rc RenderedContext, config DiscussSessionConfig) {
	d.mu.Lock()
	sess, ok := d.sessions[sessionID]
	if !ok {
		sessCtx, cancel := context.WithCancel(context.Background()) //nolint:gosec // G118: cancel is stored in sess.cancel
		sess = &discussSession{
			config: config,
			rcCh:   make(chan RenderedContext, 16),
			stopCh: make(chan struct{}),
			cancel: cancel,
		}

		// Wire smart timing if the service is available.
		if d.deps.ChatTimingService != nil {
			cfg := chattiming.DefaultConfig()
			// Load per-bot config from DB if settings service is available.
			if d.deps.SettingsService != nil {
				if raw, err := d.deps.SettingsService.GetBotChatTiming(ctx, config.BotID); err == nil && len(raw) > 0 {
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

		d.sessions[sessionID] = sess
		go d.runSession(sessCtx, sess) //nolint:contextcheck // long-lived goroutine; must outlive the inbound HTTP request
	}
	d.mu.Unlock()

	// Attempt interrupt if the session is actively generating a response and timing is enabled.
	if ok && sess.interrupt != nil && sess.chatTimingCfg != nil && sess.chatTimingCfg.Enabled {
		if sess.interrupt.RequestInterrupt() {
			d.logger.Info("chat timing: planner interrupt triggered",
				slog.String("session_id", sessionID))
		}
	}

	select {
	case sess.rcCh <- rc:
	default:
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

// StopSession stops a discuss session goroutine.
func (d *DiscussDriver) StopSession(sessionID string) {
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

// StopAll stops all discuss session goroutines.
func (d *DiscussDriver) StopAll() {
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
}

// HasSession returns true if a discuss session goroutine is running.
func (d *DiscussDriver) HasSession(sessionID string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, ok := d.sessions[sessionID]
	return ok
}

const discussIdleTimeout = 10 * time.Minute

func (d *DiscussDriver) runSession(ctx context.Context, sess *discussSession) {
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

		// Compute message inter-arrival intervals from the latest RC for idle compensation.
		if sess.idleCompensate != nil {
			sess.msgIntervals = computeMsgIntervals(latestRC, sess.lastProcessedMs)
		}

		d.handleReply(ctx, sess, latestRC, log)
	}
}

func (d *DiscussDriver) handleReply(ctx context.Context, sess *discussSession, rc RenderedContext, log *slog.Logger) {
	isMentioned := wasRecentlyMentioned(rc, sess.lastProcessedMs)
	newMsgCount := countNewMessages(rc, sess.lastProcessedMs)

	// Smart timing: talk_value threshold check.
	if sess.chatTimingCfg != nil && sess.chatTimingCfg.Enabled && !isMentioned {
		threshold := sess.chatTimingCfg.TriggerThreshold()

		// Idle compensation: supplement message count with idle time credit.
		if newMsgCount < threshold && sess.idleCompensate != nil {
			lastMsgMs := LatestExternalEventMs(rc, 0)
			if lastMsgMs > 0 {
				idleDuration := time.Since(time.UnixMilli(lastMsgMs))
				credit := sess.idleCompensate.ComputeCredit(idleDuration, chattiming.ComputeCreditRateFromIntervals(sess.msgIntervals))
				if credit > 0 {
					log.Debug("chat timing: idle compensation applied",
						slog.Int("credit", credit),
						slog.Duration("idle", idleDuration))
					newMsgCount += credit
				}
			}
		}

		if newMsgCount < threshold {
			log.Debug("chat timing: talk_value threshold not met",
				slog.Int("new_messages", newMsgCount),
				slog.Int("threshold", threshold))
			d.extractPassiveMemory(ctx, sess, rc, log)
			return
		}
	}

	// Smart timing: timing gate — lightweight LLM check before full agent call.
	if sess.timingGate != nil && sess.chatTimingCfg.TimingGate && !isMentioned {
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
		// Use cached probe model if available, otherwise resolve fresh (first call only).
		probeCfg := agentpkg.RunConfig{SupportsToolCall: false}
		if sess.cachedProbeModel != nil {
			probeCfg = *sess.cachedProbeModel
		} else if d.deps.Resolver != nil {
			resolved, err := d.deps.Resolver.ResolveRunConfig(ctx,
				sess.config.BotID, sess.config.SessionID, sess.config.ChannelIdentityID,
				sess.config.CurrentPlatform, sess.config.ReplyTarget,
				sess.config.ConversationType, sess.config.SessionToken)
			if err == nil {
				probeCfg = resolved.RunConfig
				// Cache for future timing gate calls — only the model/provider is needed.
				sess.cachedProbeModel = &agentpkg.RunConfig{Model: resolved.RunConfig.Model}
			}
		}
		result := sess.timingGate.Evaluate(ctx, params, probeCfg)
		switch result.Decision {
		case chattiming.TimingNoReply:
			log.Info("chat timing: gate decided no_reply",
				slog.String("reason", result.Reason))
			// Mark as processed so we don't re-evaluate these messages.
			sess.lastProcessedMs = time.Now().UnixMilli()
			d.extractPassiveMemory(ctx, sess, rc, log)
			return
		case chattiming.TimingWait:
			log.Info("chat timing: gate decided wait",
				slog.Int("wait_seconds", result.WaitSeconds),
				slog.String("reason", result.Reason))
			// Wait the suggested duration, then fall through to the agent call.
			select {
			case <-time.After(time.Duration(result.WaitSeconds) * time.Second):
			case <-ctx.Done():
				return
			}
		}
	}

	d.handleReplyWithAgent(ctx, sess, rc, log, d.deps.Agent)
}

func (d *DiscussDriver) handleReplyWithAgent(ctx context.Context, sess *discussSession, rc RenderedContext, log *slog.Logger, agent discussStreamer) {
	cfg := sess.config

	isMentioned := wasRecentlyMentioned(rc, sess.lastProcessedMs)

	// Smart timing: interrupt-retry loop.
	maxRounds := 1
	if sess.interrupt != nil {
		sess.interrupt.ResetRounds()
		maxRounds = 7 // loop bound; CanRetry() caps at 1 initial + 5 retries
	}

	for round := 0; round < maxRounds; round++ {
		var agentCtx context.Context
		var agentCancel context.CancelFunc

		if sess.interrupt != nil {
			agentCtx, agentCancel = sess.interrupt.Bind(ctx)
		} else {
			agentCtx = ctx
			agentCancel = func() {} // no-op for non-interrupt path
		}

		// Resolve run config fresh each round (context may have changed after interrupt).
		trs := d.loadTurnResponses(agentCtx, cfg.SessionID)
		composed := ComposeContext(rc, trs, "")
		if composed == nil {
			if sess.interrupt != nil {
				sess.interrupt.Unbind(true)
			}
			return
		}

		log.Info("triggering discuss LLM call",
			slog.Int("round", round),
			slog.Int("messages", len(composed.Messages)),
			slog.Int("estimated_tokens", composed.EstimatedTokens))

		if d.deps.Resolver == nil {
			log.Error("discuss driver: resolver not configured")
			if sess.interrupt != nil {
				sess.interrupt.Unbind(true)
			}
			return
		}
		resolved, err := d.deps.Resolver.ResolveRunConfig(agentCtx,
			cfg.BotID, cfg.SessionID, cfg.ChannelIdentityID,
			cfg.CurrentPlatform, cfg.ReplyTarget, cfg.ConversationType, cfg.SessionToken)
		if err != nil {
			log.Error("discuss: resolve run config failed", slog.Any("error", err))
			if sess.interrupt != nil {
				sess.interrupt.Unbind(true)
			}
			return
		}
		runConfig := resolved.RunConfig

		// Cache the resolved model for timing gate probe reuse.
		if sess.cachedProbeModel == nil || sess.cachedProbeModel.Model != runConfig.Model {
			sess.cachedProbeModel = &agentpkg.RunConfig{Model: runConfig.Model}
		}
		runConfig.Messages = contextMessagesToSDK(composed.Messages)
		runConfig.SessionType = sessionpkg.TypeDiscuss
		runConfig.Query = ""

		if runConfig.SupportsImageInput && d.deps.Resolver != nil {
			imageRefs := extractNewImageRefs(rc, sess.lastProcessedMs)
			if len(imageRefs) > 0 {
				imageParts := d.deps.Resolver.InlineImageAttachments(agentCtx, cfg.BotID, imageRefs)
				injectImagePartsIntoLastUserMessage(runConfig.Messages, imageParts)
			}
		}

		lateBinding := buildLateBindingPrompt(isMentioned)
		runConfig.Messages = append(runConfig.Messages, sdk.UserMessage(lateBinding))

		eventCh := agent.Stream(agentCtx, runConfig)

		var finalMessages json.RawMessage
		for event := range eventCh {
			d.broadcastDiscussEvent(cfg.BotID, event)

			switch event.Type {
			case agentpkg.EventError:
				log.Error("discuss stream error", slog.String("error", event.Error))
			case agentpkg.EventAgentEnd, agentpkg.EventAgentAbort:
				finalMessages = event.Messages
			}
		}

		// Cancel the agent context now (not deferred) to release resources
		// immediately rather than accumulating across loop iterations.
		agentCancel()

		// Check if the agent was interrupted via the controller's state.
		// Must capture before Unbind() resets the flag.
		wasInterrupted := false
		if sess.interrupt != nil {
			wasInterrupted = sess.interrupt.ConsumeInterrupted()
			sess.interrupt.Unbind(!wasInterrupted)
		}

		if wasInterrupted && sess.interrupt != nil && sess.interrupt.CanRetry() {
			log.Info("chat timing: agent interrupted, waiting for quiet period before retry")

			// Debounce: wait for message quiet period.
			if sess.debounce != nil {
				sess.debounce.Reset()
				_ = sess.debounce.Wait(ctx)
			}

			// Drain any new RCs that arrived during the interrupted agent call.
			drained := true
			for drained {
				drained = false
				select {
				case newRC := <-sess.rcCh:
					rc = newRC // Use updated context for retry.
					drained = true
				default:
				}
			}

			continue // Retry with fresh Bind().
		}

		// Normal completion or non-retriable abort — store results.
		now := time.Now()

		if d.deps.Resolver != nil && len(finalMessages) > 0 {
			var sdkMsgs []sdk.Message
			if json.Unmarshal(finalMessages, &sdkMsgs) == nil && len(sdkMsgs) > 0 {
				if storeErr := d.deps.Resolver.StoreRound(ctx,
					cfg.BotID, cfg.SessionID, cfg.ChannelIdentityID, cfg.CurrentPlatform,
					sdkMsgs, resolved.ModelID,
				); storeErr != nil {
					log.Error("discuss: store round failed", slog.Any("error", storeErr))
				}
			}
		}

		sess.lastProcessedMs = now.UnixMilli()
		return
	}
}

// extractPassiveMemory runs lightweight fact extraction on accumulated messages
// when the bot decides not to respond. This allows learning community knowledge,
// user patterns, and slang without actively participating in the conversation.
func (d *DiscussDriver) extractPassiveMemory(ctx context.Context, sess *discussSession, rc RenderedContext, log *slog.Logger) {
	if d.deps.MemoryFormation == nil {
		return
	}

	// Collect new messages since last processing, excluding bot's own messages.
	var messages []adapters.Message
	for _, seg := range rc {
		if seg.ReceivedAtMs <= sess.lastProcessedMs {
			continue
		}
		if seg.IsMyself {
			continue
		}
		// Extract text content from rendered segments.
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

	req := adapters.AfterChatRequest{
		BotID:             sess.config.BotID,
		Messages:          messages,
		ChannelIdentityID: sess.config.ChannelIdentityID,
	}

	go func() {
		if err := d.deps.MemoryFormation.OnAfterChat(context.Background(), req); err != nil {
			log.Warn("passive memory extraction failed", slog.Any("error", err))
		}
	}()
}

// broadcastDiscussEvent forwards an agent stream event to the RouteHub so the
// Web UI can display thinking, tool calls, and text deltas in real time.
func (d *DiscussDriver) broadcastDiscussEvent(botID string, event agentpkg.StreamEvent) {
	if d.deps.Broadcaster == nil {
		return
	}
	se, ok := agentEventToChannelEvent(event)
	if !ok {
		return
	}
	d.deps.Broadcaster.PublishEvent(botID, se)
}

func agentEventToChannelEvent(e agentpkg.StreamEvent) (channel.StreamEvent, bool) {
	switch e.Type {
	case agentpkg.EventAgentStart:
		return channel.StreamEvent{Type: channel.StreamEventAgentStart}, true
	case agentpkg.EventTextStart:
		return channel.StreamEvent{Type: channel.StreamEventPhaseStart, Phase: channel.StreamPhaseText}, true
	case agentpkg.EventTextDelta:
		return channel.StreamEvent{Type: channel.StreamEventDelta, Delta: e.Delta}, true
	case agentpkg.EventTextEnd:
		return channel.StreamEvent{Type: channel.StreamEventPhaseEnd, Phase: channel.StreamPhaseText}, true
	case agentpkg.EventReasoningStart:
		return channel.StreamEvent{Type: channel.StreamEventPhaseStart, Phase: channel.StreamPhaseReasoning}, true
	case agentpkg.EventReasoningDelta:
		return channel.StreamEvent{Type: channel.StreamEventDelta, Delta: e.Delta, Phase: channel.StreamPhaseReasoning}, true
	case agentpkg.EventReasoningEnd:
		return channel.StreamEvent{Type: channel.StreamEventPhaseEnd, Phase: channel.StreamPhaseReasoning}, true
	case agentpkg.EventToolCallStart:
		return channel.StreamEvent{
			Type:     channel.StreamEventToolCallStart,
			ToolCall: &channel.StreamToolCall{Name: e.ToolName, CallID: e.ToolCallID, Input: e.Input},
		}, true
	case agentpkg.EventToolCallEnd:
		return channel.StreamEvent{
			Type:     channel.StreamEventToolCallEnd,
			ToolCall: &channel.StreamToolCall{Name: e.ToolName, CallID: e.ToolCallID, Input: e.Input, Result: e.Result},
		}, true
	case agentpkg.EventAgentEnd:
		return channel.StreamEvent{Type: channel.StreamEventAgentEnd}, true
	case agentpkg.EventAgentAbort:
		return channel.StreamEvent{Type: channel.StreamEventAgentEnd}, true
	case agentpkg.EventError:
		return channel.StreamEvent{Type: channel.StreamEventError, Error: e.Error}, true
	default:
		return channel.StreamEvent{}, false
	}
}

func (d *DiscussDriver) loadTurnResponses(ctx context.Context, sessionID string) []TurnResponseEntry {
	if d.deps.MessageService == nil {
		return nil
	}

	since := time.Now().UTC().Add(-24 * time.Hour)
	msgs, err := d.deps.MessageService.ListActiveSinceBySession(ctx, sessionID, since)
	if err != nil {
		d.logger.Warn("load TRs failed", slog.String("session_id", sessionID), slog.Any("error", err))
		return nil
	}

	var trs []TurnResponseEntry
	for _, m := range msgs {
		if m.Role != "assistant" && m.Role != "tool" {
			continue
		}
		var mm conversation.ModelMessage
		if err := json.Unmarshal(m.Content, &mm); err != nil {
			continue
		}
		contentStr := mm.TextContent()
		if contentStr == "" {
			continue
		}
		trs = append(trs, TurnResponseEntry{
			RequestedAtMs: m.CreatedAt.UnixMilli(),
			Role:          m.Role,
			Content:       contentStr,
		})
	}
	return trs
}

// extractNewImageRefs collects ImageAttachmentRef entries from RC segments
// that arrived after afterMs (i.e. new since the last LLM call).
func extractNewImageRefs(rc RenderedContext, afterMs int64) []ImageAttachmentRef {
	var refs []ImageAttachmentRef
	for _, seg := range rc {
		if seg.ReceivedAtMs > afterMs && !seg.IsMyself {
			refs = append(refs, seg.ImageRefs...)
		}
	}
	return refs
}

// injectImagePartsIntoLastUserMessage appends ImageParts to the last user
// message in msgs so the model receives inline vision input.
func injectImagePartsIntoLastUserMessage(msgs []sdk.Message, parts []sdk.ImagePart) {
	if len(parts) == 0 {
		return
	}
	extra := make([]sdk.MessagePart, 0, len(parts))
	for _, p := range parts {
		if strings.TrimSpace(p.Image) != "" {
			extra = append(extra, p)
		}
	}
	if len(extra) == 0 {
		return
	}
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == sdk.MessageRoleUser {
			msgs[i].Content = append(msgs[i].Content, extra...)
			return
		}
	}
}

func wasRecentlyMentioned(rc RenderedContext, afterMs int64) bool {
	for _, seg := range rc {
		if seg.ReceivedAtMs > afterMs && (seg.MentionsMe || seg.RepliesToMe) {
			return true
		}
	}
	return false
}

// renderContextXML formats recent context segments as XML for the timing gate prompt.
// Only includes segments after afterMs from external (non-self) senders.
func renderContextXML(rc RenderedContext, afterMs int64) string {
	var sb strings.Builder
	for _, seg := range rc {
		if seg.ReceivedAtMs <= afterMs {
			continue
		}
		if seg.IsMyself {
			continue
		}
		ts := time.UnixMilli(seg.ReceivedAtMs).Format(time.RFC3339)
		for _, piece := range seg.Content {
			if piece.Type == "text" && piece.Text != "" {
				fmt.Fprintf(&sb, "<msg time=\"%s\">%s</msg>\n", ts, piece.Text)
			}
		}
	}
	return sb.String()
}

// countNewMessages counts external (non-self) message segments in the RC
// that arrived after the given timestamp.
func countNewMessages(rc RenderedContext, afterMs int64) int {
	count := 0
	for _, seg := range rc {
		if seg.ReceivedAtMs > afterMs && !seg.IsMyself {
			count++
		}
	}
	return count
}

// computeMsgIntervals extracts inter-arrival durations between external
// message segments in the RC. Returns at most 20 intervals (most recent).
func computeMsgIntervals(rc RenderedContext, afterMs int64) []time.Duration {
	var timestamps []int64
	for _, seg := range rc {
		if seg.ReceivedAtMs > afterMs && !seg.IsMyself {
			timestamps = append(timestamps, seg.ReceivedAtMs)
		}
	}
	if len(timestamps) < 2 {
		return nil
	}
	// Compute intervals between consecutive timestamps.
	intervals := make([]time.Duration, 0, len(timestamps)-1)
	for i := 1; i < len(timestamps); i++ {
		d := time.Duration(timestamps[i]-timestamps[i-1]) * time.Millisecond
		if d > 0 {
			intervals = append(intervals, d)
		}
	}
	// Keep at most 20 most recent intervals.
	if len(intervals) > 20 {
		intervals = intervals[len(intervals)-20:]
	}
	return intervals
}

func buildLateBindingPrompt(isMentioned bool) string {
	now := time.Now().Format(time.RFC3339)
	var sb strings.Builder
	sb.WriteString("Current time: ")
	sb.WriteString(now)
	sb.WriteString("\n\n")
	sb.WriteString("IMPORTANT: You MUST use the `send` tool to speak. Your text output is invisible to everyone — it is only internal monologue. ")
	sb.WriteString("If you want to say something, you MUST call the `send` tool. Writing text without a tool call means absolute silence — no one will see it.")

	if isMentioned {
		sb.WriteString("\n\nYou were mentioned or replied to. You should respond by calling the `send` tool now.")
	}

	return sb.String()
}

func contextMessagesToSDK(messages []ContextMessage) []sdk.Message {
	result := make([]sdk.Message, 0, len(messages))
	for _, m := range messages {
		switch m.Role {
		case "user":
			result = append(result, sdk.UserMessage(m.Content))
		case "assistant":
			result = append(result, sdk.AssistantMessage(m.Content))
		default:
			result = append(result, sdk.UserMessage(m.Content))
		}
	}
	return result
}
