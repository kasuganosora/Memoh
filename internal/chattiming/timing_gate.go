package chattiming

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	sdk "github.com/memohai/twilight-ai/sdk"

	agentpkg "github.com/memohai/memoh/internal/agent"
)

// TimingDecision represents the LLM's decision about whether the bot should speak.
type TimingDecision string

const (
	// TimingContinue means the bot should proceed to respond.
	TimingContinue TimingDecision = "continue"
	// TimingWait means the bot should wait N seconds then re-evaluate.
	TimingWait TimingDecision = "wait"
	// TimingNoReply means the bot should stay silent this round.
	TimingNoReply TimingDecision = "no_reply"
)

// TimingGateResult holds the decision and metadata from a timing gate evaluation.
type TimingGateResult struct {
	Decision    TimingDecision
	WaitSeconds int
	Reason      string
}

// TimingGateParams holds the input parameters for timing gate evaluation.
type TimingGateParams struct {
	// RenderedContextXML is the recent conversation context in XML format.
	RenderedContextXML string
	// IsMentioned is true if the bot was @mentioned or replied to.
	IsMentioned bool
	// NewMessageCount is the number of new messages since the bot last spoke.
	NewMessageCount int
	// TimeSinceLastMessageSec is seconds since the last message was received.
	TimeSinceLastMessageSec float64
	// TalkValue is the current effective talk_value (0.0–1.0).
	TalkValue float64
	// BotName is the bot's display name for context.
	BotName string
}

// timingGateResponse is the expected JSON response from the LLM.
type timingGateResponse struct {
	Decision    string `json:"decision"`
	WaitSeconds int    `json:"wait_seconds,omitempty"`
	Reason      string `json:"reason"`
}

// TimingGate uses a lightweight LLM call to decide whether the bot should
// respond in a group conversation. It returns one of three decisions:
// continue (respond now), wait (wait for more messages), or no_reply (stay silent).
type TimingGate struct {
	agent  *agentpkg.Agent
	logger *slog.Logger
}

// NewTimingGate creates a new TimingGate.
func NewTimingGate(agent *agentpkg.Agent, logger *slog.Logger) *TimingGate {
	return &TimingGate{
		agent:  agent,
		logger: logger.With(slog.String("component", "timing_gate")),
	}
}

const (
	// timingGateMaxTimeout is the absolute maximum time allowed for a timing gate call.
	timingGateMaxTimeout = 3 * time.Minute
	// timingGateIdleTimeout is how long we wait without receiving any token
	// before considering the LLM stalled. As long as tokens keep arriving,
	// the idle timer resets and we never hit this.
	timingGateIdleTimeout = 30 * time.Second
)

// Evaluate runs the timing gate check. If isMentioned is true, it skips the
// LLM call entirely and returns TimingContinue immediately (the bot must respond).
//
// The LLM call is wrapped in a separate goroutine with a wall-clock hard timeout
// so that the caller is not blocked even if the SDK's HTTP client ignores context
// cancellation. The caller (evaluateTimingGate) applies fail-closed semantics on
// failure results.
func (tg *TimingGate) Evaluate(ctx context.Context, params TimingGateParams, runConfig agentpkg.RunConfig) TimingGateResult {
	// @mention always forces a response.
	if params.IsMentioned {
		return TimingGateResult{Decision: TimingContinue, Reason: "mentioned"}
	}

	// Create a bounded context for the internal evaluation goroutine.
	evalCtx, cancel := context.WithTimeout(ctx, timingGateMaxTimeout)
	defer cancel()

	// Channel for the goroutine to return its result.
	resultCh := make(chan TimingGateResult, 1)
	go func() {
		resultCh <- tg.doEvaluate(evalCtx, params, runConfig)
	}()

	// Wall-clock hard timeout — independent of context so we can distinguish
	// "parent cancelled" from "gate took too long".
	hardTimer := time.NewTimer(timingGateMaxTimeout)
	defer hardTimer.Stop()

	select {
	case result := <-resultCh:
		return result
	case <-hardTimer.C:
		// The goroutine exceeded the hard timeout. Cancel its context to unblock it.
		cancel()
		tg.logger.Warn("timing gate goroutine hard timeout",
			slog.Duration("max_timeout", timingGateMaxTimeout),
			slog.String("bot_name", params.BotName))
		return TimingGateResult{Decision: TimingContinue, Reason: "goroutine timeout"}
	case <-ctx.Done():
		// Parent context cancelled (e.g., session shutdown, new message arrived).
		cancel()
		tg.logger.Warn("timing gate parent context cancelled",
			slog.String("bot_name", params.BotName),
			slog.Any("cause", ctx.Err()))
		return TimingGateResult{Decision: TimingNoReply, Reason: "context cancelled"}
	}
}

// doEvaluate performs the actual timing gate evaluation — streaming an LLM call
// with activity-based idle timeout. It is called inside a goroutine by Evaluate.
func (tg *TimingGate) doEvaluate(ctx context.Context, params TimingGateParams, runConfig agentpkg.RunConfig) TimingGateResult {
	prompt := buildTimingGatePrompt(params)

	cfg := runConfig
	cfg.System = prompt
	cfg.Messages = []sdk.Message{sdk.UserMessage("Decide now. Respond with JSON only.")}
	cfg.SupportsToolCall = false // No tools — simple JSON response.
	cfg.LoopDetection.Enabled = false
	cfg.Retry = agentpkg.RetryConfig{MaxAttempts: 1}

	startTime := time.Now()

	// Log entry point with model info for tracing.
	modelName := ""
	if cfg.Model != nil {
		modelName = cfg.Model.DisplayName
		if modelName == "" {
			modelName = cfg.Model.ID
		}
	}
	tg.logger.Info("timing gate LLM call start",
		slog.String("model", modelName),
		slog.String("bot_name", params.BotName),
		slog.Int("new_message_count", params.NewMessageCount),
		slog.Float64("talk_value", params.TalkValue))

	// Use streaming so we can detect token activity and apply an idle timeout.
	ch := tg.agent.Stream(ctx, cfg)

	var textBuilder strings.Builder
	var ttft time.Duration // Time to first token.
	var firstTokenReceived bool
	idleTimer := time.NewTimer(timingGateIdleTimeout)
	defer idleTimer.Stop()

	var usageRaw json.RawMessage

	for {
		select {
		case evt, ok := <-ch:
			if !ok {
				// Stream closed — parse whatever we collected.
				return tg.finalizeResultWithMetrics(textBuilder.String(), usageRaw, startTime, ttft)
			}
			// Reset idle timer on any meaningful activity.
			switch evt.Type {
			case agentpkg.EventTextDelta, agentpkg.EventReasoningDelta:
				if !firstTokenReceived {
					ttft = time.Since(startTime)
					firstTokenReceived = true
				}
				if !idleTimer.Stop() {
					select {
					case <-idleTimer.C:
					default:
					}
				}
				idleTimer.Reset(timingGateIdleTimeout)
				if evt.Type == agentpkg.EventTextDelta {
					textBuilder.WriteString(evt.Delta)
				}
			case agentpkg.EventAgentEnd, agentpkg.EventAgentAbort:
				usageRaw = evt.Usage
				return tg.finalizeResultWithMetrics(textBuilder.String(), usageRaw, startTime, ttft)
			case agentpkg.EventError:
				tg.logger.Warn("timing gate stream error, failing open to continue",
					slog.String("error", evt.Error),
					slog.Duration("elapsed", time.Since(startTime)))
				return TimingGateResult{Decision: TimingContinue, Reason: "error: " + evt.Error}
			}

		case <-idleTimer.C:
			// No token activity for timingGateIdleTimeout — treat as stall.
			// Note: caller (evaluateTimingGate) applies fail-closed semantics on
			// failure results, so "TimingContinue" here is overridden upstream.
			tg.logger.Warn("timing gate idle timeout (gate failure)",
				slog.Duration("idle_timeout", timingGateIdleTimeout),
				slog.Duration("elapsed", time.Since(startTime)),
				slog.Int("text_length", textBuilder.Len()))
			// Drain remaining events to avoid goroutine leak.
			go func() {
				for range ch {
				}
			}()
			if textBuilder.Len() > 0 {
				return tg.finalizeResultWithMetrics(textBuilder.String(), nil, startTime, ttft)
			}
			return TimingGateResult{Decision: TimingContinue, Reason: "idle timeout"}

		case <-ctx.Done():
			// Context cancelled (hard timeout or parent shutdown).
			// Caller applies fail-closed semantics on this result.
			tg.logger.Warn("timing gate context cancelled (gate failure)",
				slog.Duration("elapsed", time.Since(startTime)),
				slog.Int("text_length", textBuilder.Len()),
				slog.Any("cause", ctx.Err()))
			go func() {
				for range ch {
				}
			}()
			if textBuilder.Len() > 0 {
				return tg.finalizeResultWithMetrics(textBuilder.String(), nil, startTime, ttft)
			}
			return TimingGateResult{Decision: TimingContinue, Reason: "timeout"}
		}
	}
}

// timingGateUsage holds token usage from a timing gate LLM call.
type timingGateUsage struct {
	InputTokens     int `json:"input_tokens"`
	OutputTokens    int `json:"output_tokens"`
	TotalTokens     int `json:"total_tokens"`
	CachedTokens    int `json:"cached_input_tokens"`
	ReasoningTokens int `json:"reasoning_tokens"`
}

// finalizeResultWithMetrics parses the collected text, extracts token usage,
// and logs a comprehensive decision record with latency breakdown.
func (tg *TimingGate) finalizeResultWithMetrics(text string, usageRaw json.RawMessage, startTime time.Time, ttft time.Duration) TimingGateResult {
	totalDuration := time.Since(startTime)
	parseStart := time.Now()
	parsed := parseTimingGateResult(text)
	parseDuration := time.Since(parseStart)

	// Extract usage from the terminal event.
	var usage timingGateUsage
	if len(usageRaw) > 0 {
		_ = json.Unmarshal(usageRaw, &usage)
	}

	// Compute streaming duration (total - ttft - parse).
	streamDuration := totalDuration - ttft - parseDuration
	if streamDuration < 0 {
		streamDuration = 0
	}

	tg.logger.Info("timing gate decision",
		slog.String("decision", string(parsed.Decision)),
		slog.Int("wait_seconds", parsed.WaitSeconds),
		slog.String("reason", parsed.Reason),
		// Token usage
		slog.Int("input_tokens", usage.InputTokens),
		slog.Int("output_tokens", usage.OutputTokens),
		slog.Int("cached_tokens", usage.CachedTokens),
		slog.Int("reasoning_tokens", usage.ReasoningTokens),
		// Latency breakdown
		slog.Duration("total_duration", totalDuration),
		slog.Duration("ttft", ttft),
		slog.Duration("stream_duration", streamDuration),
		slog.Duration("parse_duration", parseDuration),
		// Raw output for debugging (truncated to prevent log bloat)
		slog.String("raw_output", truncateForLog(text, 500)),
	)
	return parsed
}

// truncateForLog truncates a string to maxLen characters, appending "..." if truncated.
func truncateForLog(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

func buildTimingGatePrompt(params TimingGateParams) string {
	var sb strings.Builder
	sb.WriteString("You are a conversation timing evaluator. Decide whether the bot should respond now.\n")
	sb.WriteString("You MUST respond with ONLY a JSON object, no other text.\n\n")

	fmt.Fprintf(&sb, "Bot name: %s\n", params.BotName)
	fmt.Fprintf(&sb, "Bot was mentioned: %v\n", params.IsMentioned)
	fmt.Fprintf(&sb, "New messages since bot last spoke: %d\n", params.NewMessageCount)
	fmt.Fprintf(&sb, "Seconds since last message: %.1f\n", params.TimeSinceLastMessageSec)
	fmt.Fprintf(&sb, "Bot chattiness (talk_value): %.2f\n", params.TalkValue)
	fmt.Fprintf(&sb, "Current time: %s\n", time.Now().Format(time.RFC3339))

	sb.WriteString("\n## Recent conversation\n\n")
	if params.RenderedContextXML != "" {
		sb.WriteString(params.RenderedContextXML)
	} else {
		sb.WriteString("(no recent messages)")
	}

	sb.WriteString(`

## Instructions

Respond with ONLY this JSON format:
{"decision": "continue|wait|no_reply", "wait_seconds": N, "reason": "brief explanation"}

Decisions:
- **continue**: The bot should respond now. The topic is relevant and the bot's input would add value.
- **wait**: Wait for more messages. Users may still be typing. Set wait_seconds (1-30).
- **no_reply**: Stay silent. Users are talking among themselves or the topic is unrelated.

## Rules
1. Is someone talking TO the bot or chatting with others?
2. Would the bot's response genuinely add value?
3. If the bot already responded and users are continuing among themselves, prefer wait or no_reply.
4. Don't blindly interject into unrelated conversations.
5. If the topic is relevant and users seem to expect a response, use continue.`)

	return sb.String()
}

func parseTimingGateResult(text string) TimingGateResult {
	cleaned := strings.TrimSpace(text)

	// Try to extract JSON from the text (may be wrapped in markdown code blocks).
	if idx := strings.Index(cleaned, "{"); idx >= 0 {
		if end := strings.LastIndex(cleaned, "}"); end > idx {
			cleaned = cleaned[idx : end+1]
		}
	}

	var resp timingGateResponse
	if err := json.Unmarshal([]byte(cleaned), &resp); err == nil {
		decision := TimingContinue
		switch strings.ToLower(resp.Decision) {
		case "wait":
			decision = TimingWait
		case "no_reply":
			decision = TimingNoReply
		}
		waitSec := resp.WaitSeconds
		// Only apply default/clamp for "wait" decisions; other decisions don't use WaitSeconds.
		if decision == TimingWait {
			if waitSec <= 0 {
				waitSec = 5
			}
			if waitSec > 30 {
				waitSec = 30
			}
		} else {
			waitSec = 0
		}
		return TimingGateResult{
			Decision:    decision,
			WaitSeconds: waitSec,
			Reason:      resp.Reason,
		}
	}

	// Fallback: keyword matching from raw text.
	lower := strings.ToLower(text)
	if strings.Contains(lower, "no_reply") || strings.Contains(lower, "no reply") || strings.Contains(lower, "stay silent") {
		return TimingGateResult{Decision: TimingNoReply, Reason: text}
	}
	if strings.Contains(lower, "continue") || strings.Contains(lower, "respond") || strings.Contains(lower, "reply") {
		return TimingGateResult{Decision: TimingContinue, Reason: text}
	}
	if strings.Contains(lower, "wait") {
		return TimingGateResult{Decision: TimingWait, WaitSeconds: 5, Reason: text}
	}
	return TimingGateResult{Decision: TimingContinue, Reason: text}
}
