package pipeline

import (
	"fmt"
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
func renderContextXML(rc RenderedContext, afterMs int64) string {
	var sb strings.Builder
	for _, seg := range rc {
		if seg.ReceivedAtMs <= afterMs || seg.IsMyself {
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

// buildLateBindingPrompt constructs the late-binding system prompt injected
// as the final user message in discuss mode.
func buildLateBindingPrompt(isMentioned bool) string {
	now := time.Now().Format(time.RFC3339)
	var sb strings.Builder
	sb.WriteString("Current time: ")
	sb.WriteString(now)
	sb.WriteString("\n\n")
	sb.WriteString("Reminder: Your text output is internal monologue — invisible to everyone. To speak, call the `send` tool. ")
	sb.WriteString("Call `send` at most ONCE per turn. Do NOT send multiple messages with similar content.")
	sb.WriteString("\n\nException: For image generation requests, call the `generate_image` tool directly — do NOT describe images via `send`.")

	if isMentioned {
		sb.WriteString("\n\nYou were mentioned or replied to. You should respond by calling the `send` tool now.")
	}

	return sb.String()
}
