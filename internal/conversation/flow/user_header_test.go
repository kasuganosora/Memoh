package flow

import (
	"strings"
	"testing"
	"time"
)

func TestFormatUserHeaderIncludesAttachments(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 4, 6, 10, 0, 0, 0, time.UTC)
	header := FormatUserHeader(UserMessageHeaderInput{
		MessageID:         "msg_1",
		ChannelIdentityID: "cid_1",
		DisplayName:       "Alice",
		Channel:           "feishu",
		ConversationType:  "group",
		ConversationName:  "Team Chat",
		AttachmentPaths:   []string{"/tmp/a.txt"},
		Time:              now,
		Timezone:          "UTC",
	}, "hello")

	if !strings.Contains(header, "attachments:\n  - /tmp/a.txt\n") {
		t.Fatalf("expected attachment path in header: %s", header)
	}
}

func TestFormatUserHeaderWithoutAttachmentsUsesEmptyList(t *testing.T) {
	t.Parallel()

	header := FormatUserHeader(UserMessageHeaderInput{
		ChannelIdentityID: "cid_1",
		DisplayName:       "Alice",
		Channel:           "feishu",
		ConversationType:  "group",
		ConversationName:  "Team Chat",
		Time:              time.Now().UTC(),
	}, "hello")

	if !strings.Contains(header, "attachments: []\n") {
		t.Fatalf("expected empty attachments list in header: %s", header)
	}
}
