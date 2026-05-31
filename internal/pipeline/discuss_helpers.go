package pipeline

import (
	"strings"
	"time"
)

// wasRecentlyMentioned returns true if any segment after afterMs mentions or
// replies to the bot.
func wasRecentlyMentioned(rc RenderedContext, afterMs int64) bool {
	for _, seg := range rc {
		if seg.ReceivedAtMs > afterMs && (seg.MentionsMe || seg.RepliesToMe) {
			return true
		}
	}
	return false
}

// renderContextXML formats recent context segments as XML for the timing gate prompt.
// Each segment's Content already contains fully-rendered XML (with sender, timestamp,
// etc.), so we output them directly without extra wrapping.
func renderContextXML(rc RenderedContext, afterMs int64) string {
	var sb strings.Builder
	for _, seg := range rc {
		if seg.ReceivedAtMs <= afterMs || seg.IsMyself {
			continue
		}
		for _, piece := range seg.Content {
			if piece.Type == "text" && piece.Text != "" {
				sb.WriteString(piece.Text)
				sb.WriteByte('\n')
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
	intervals := make([]time.Duration, 0, len(timestamps)-1)
	for i := 1; i < len(timestamps); i++ {
		d := time.Duration(timestamps[i]-timestamps[i-1]) * time.Millisecond
		if d > 0 {
			intervals = append(intervals, d)
		}
	}
	if len(intervals) > 20 {
		intervals = intervals[len(intervals)-20:]
	}
	return intervals
}

// latestRCReceivedAtMs returns the maximum ReceivedAtMs across all segments
// in the given RC, or 0 if the RC is empty.
func latestRCReceivedAtMs(rc RenderedContext) int64 {
	var latest int64
	for _, seg := range rc {
		if seg.ReceivedAtMs > latest {
			latest = seg.ReceivedAtMs
		}
	}
	return latest
}

// replyTargetMaxAge is the maximum age of a segment to be considered as a
// reply target when afterMs is 0 (new session). This prevents the bot from
// replying to very old notes that happen to mention it.
const replyTargetMaxAge = 5 * time.Minute

// latestReplyTarget extracts the best reply target from a RenderedContext.
// It prefers the latest non-self segment with MentionsMe or RepliesToMe set.
// If none found, it falls back to the latest non-self segment's Target.
// Returns empty string if no suitable target is found.
//
// When afterMs is 0 (new session), only segments received within the last
// replyTargetMaxAge are considered, preventing replies to stale notes.
func latestReplyTarget(rc RenderedContext, afterMs int64) string {
	// When afterMs is 0 (new session), compute a floor timestamp to avoid
	// picking up very old segments as reply targets.
	effectiveAfterMs := afterMs
	if effectiveAfterMs == 0 {
		floor := time.Now().Add(-replyTargetMaxAge).UnixMilli()
		effectiveAfterMs = floor
	}

	var mentionTarget string
	var mentionMs int64
	var latestTarget string
	var latestMs int64

	for _, seg := range rc {
		if seg.IsMyself || seg.Target == "" {
			continue
		}
		if seg.ReceivedAtMs <= effectiveAfterMs {
			continue
		}
		if seg.ReceivedAtMs > latestMs {
			latestMs = seg.ReceivedAtMs
			latestTarget = seg.Target
		}
		if (seg.MentionsMe || seg.RepliesToMe) && seg.ReceivedAtMs > mentionMs {
			mentionMs = seg.ReceivedAtMs
			mentionTarget = seg.Target
		}
	}

	if mentionTarget != "" {
		return mentionTarget
	}
	return latestTarget
}

// buildLateBindingPrompt constructs the late-binding system prompt injected
// as the final user message in discuss mode.
func buildLateBindingPrompt(isMentioned bool) string {
	now := time.Now().Format(time.RFC3339)
	var sb strings.Builder
	sb.WriteString("Current time: ")
	sb.WriteString(now)
	sb.WriteString("\n\n")
	sb.WriteString("Reminder: Your text output is internal monologue — invisible to everyone. To speak, call the `send` tool. ")
	sb.WriteString("Call `send` at most ONCE per turn. Do NOT send multiple messages with similar content. ")
	sb.WriteString("Focus on the MOST RECENT messages in the conversation. ")
	sb.WriteString("If you already replied to an earlier question or @mention in a previous turn, do NOT re-reply to it — the conversation has moved on. ")
	sb.WriteString("If new messages are unrelated to you or don't warrant a response, staying silent is the right choice — trust your judgment.")

	if isMentioned {
		sb.WriteString("\n\nYou were @mentioned or replied to in the LATEST messages. Consider responding — but only if you have something meaningful to say.")
	}

	return sb.String()
}
