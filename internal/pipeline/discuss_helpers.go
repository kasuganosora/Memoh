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

// buildLateBindingPrompt constructs the late-binding system prompt injected
// as the final user message in discuss mode.
func buildLateBindingPrompt(isMentioned bool) string {
	now := time.Now().Format(time.RFC3339)
	var sb strings.Builder
	sb.WriteString("Current time: ")
	sb.WriteString(now)
	sb.WriteString("\n\n")
	sb.WriteString("REMEMBER: End your response with a <DECISION> block. This is the ONLY way to be heard.\n")
	sb.WriteString("<DECISION>\n<ACTION>reply</ACTION>\n<CONTENT>your reasoning, the replyer will polish it</CONTENT>\n</DECISION>\n")
	sb.WriteString("Text outside the block is invisible. No block = silent.")

	if isMentioned {
		sb.WriteString("\n\nURGENT: You were @mentioned. You MUST include a <DECISION> block. Silent is not acceptable.")
	}

	return sb.String()
}
