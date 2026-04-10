package tools

import (
	textprune "github.com/memohai/memoh/internal/prune"
)

const (
	toolOutputHeadBytes = 4 * 1024
	toolOutputTailBytes = 1 * 1024
	toolOutputHeadLines = 150
	toolOutputTailLines = 50

	listMaxEntries        = 200
	listCollapseThreshold = 50
)

func pruneToolOutputText(text, label string) string {
	return textprune.PruneWithEdges(text, label, textprune.Config{
		MaxBytes:  textprune.DefaultMaxBytes,
		MaxLines:  textprune.DefaultMaxLines,
		HeadBytes: toolOutputHeadBytes,
		TailBytes: toolOutputTailBytes,
		HeadLines: toolOutputHeadLines,
		TailLines: toolOutputTailLines,
		Marker:    textprune.DefaultMarker,
	})
}
