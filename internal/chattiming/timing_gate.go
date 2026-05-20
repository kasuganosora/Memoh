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
// On error, it fails open by returning TimingContinue.
//
// The call uses streaming with an activity-based timeout: as long as the LLM
// keeps producing tokens the idle timer resets. Only if no token arrives for
// timingGateIdleTimeout (or the absolute timingGateMaxTimeout is reached) does
// the call abort.
func (tg *TimingGate) Evaluate(ctx context.Context, params TimingGateParams, runConfig agentpkg.RunConfig) TimingGateResult {
	// @mention always forces a response.
	if params.IsMentioned {
		return TimingGateResult{Decision: TimingContinue, Reason: "mentioned"}
	}

	// Absolute deadline to prevent unbounded waits.
	ctx, cancel := context.WithTimeout(ctx, timingGateMaxTimeout)
	defer cancel()

	prompt := buildTimingGatePrompt(params)

	cfg := runConfig
	cfg.System = prompt
	cfg.Messages = []sdk.Message{sdk.UserMessage("Decide now. Respond with JSON only.")}
	cfg.SupportsToolCall = false // No tools — simple JSON response.
	cfg.LoopDetection.Enabled = false
	cfg.Retry = agentpkg.RetryConfig{MaxAttempts: 1}

	// Use streaming so we can detect token activity and apply an idle timeout.
	ch := tg.agent.Stream(ctx, cfg)

	var textBuilder strings.Builder
	idleTimer := time.NewTimer(timingGateIdleTimeout)
	defer idleTimer.Stop()

	for {
		select {
		case evt, ok := <-ch:
			if !ok {
				// Stream closed — parse whatever we collected.
				return tg.finalizeResult(textBuilder.String())
			}
			// Reset idle timer on any meaningful activity.
			switch evt.Type {
			case agentpkg.EventTextDelta, agentpkg.EventReasoningDelta:
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
				return tg.finalizeResult(textBuilder.String())
			case agentpkg.EventError:
				tg.logger.Warn("timing gate stream error, failing open to continue",
					slog.String("error", evt.Error))
				return TimingGateResult{Decision: TimingContinue, Reason: "error: " + evt.Error}
			}

		case <-idleTimer.C:
			// No token activity for timingGateIdleTimeout — treat as stall.
			tg.logger.Warn("timing gate idle timeout, failing open to continue",
				slog.Duration("idle_timeout", timingGateIdleTimeout))
			cancel()
			// Drain remaining events to avoid goroutine leak.
			go func() {
				for range ch {
				}
			}()
			if textBuilder.Len() > 0 {
				return tg.finalizeResult(textBuilder.String())
			}
			return TimingGateResult{Decision: TimingContinue, Reason: "idle timeout"}

		case <-ctx.Done():
			// Absolute timeout or parent context cancelled.
			tg.logger.Warn("timing gate absolute timeout, failing open to continue",
				slog.Duration("max_timeout", timingGateMaxTimeout))
			go func() {
				for range ch {
				}
			}()
			if textBuilder.Len() > 0 {
				return tg.finalizeResult(textBuilder.String())
			}
			return TimingGateResult{Decision: TimingContinue, Reason: "timeout"}
		}
	}
}

// finalizeResult parses the collected text and logs the decision.
func (tg *TimingGate) finalizeResult(text string) TimingGateResult {
	parsed := parseTimingGateResult(text)
	tg.logger.Info("timing gate decision",
		slog.String("decision", string(parsed.Decision)),
		slog.Int("wait_seconds", parsed.WaitSeconds),
		slog.String("reason", parsed.Reason))
	return parsed
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
		if waitSec <= 0 {
			waitSec = 5
		}
		if waitSec > 30 {
			waitSec = 30
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
